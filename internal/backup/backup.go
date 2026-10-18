// Package backup streams site backups and restores validated archives.
// Callers must stop application writes during Create and close all database
// handles and stop filesystem access before Restore.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pandadays/internal/db"
)

var (
	ErrInvalidArchive = errors.New("invalid backup archive")
	ErrUnsafeArchive  = errors.New("backup contains unsafe paths or links")
	ErrChecksum       = errors.New("backup checksum mismatch")
	ErrSchema         = errors.New("backup schema is not supported")
)

const manifestName = "manifest.json"
const maxManifestBytes = 16 << 20
const maxEntries = 100000

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	AppVersion    string    `json:"app_version"`
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	Files         []File    `json:"files"`
}

type Service struct {
	dataDir string
	version string
	rename  func(string, string) error
}

func New(dataDir, version string) *Service {
	return &Service{dataDir: dataDir, version: version, rename: os.Rename}
}

// contextReader makes large archive copies cancellable between reads.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *Service) stageDir() (string, error) {
	tmp := filepath.Join(s.dataDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(tmp, "backup-")
}

// Create streams a tar.gz without buffering the archive. Only the VACUUM INTO
// snapshot and the manifest need temporary storage.
func (s *Service) Create(ctx context.Context, database *sql.DB, w io.Writer, now time.Time) (err error) {
	stage, err := s.stageDir()
	if err != nil {
		return fmt.Errorf("stage backup: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	snapshot := filepath.Join(stage, db.FileName)
	if _, err := database.ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	conn, err := db.Open(ctx, snapshot)
	if err != nil {
		return err
	}
	schema, schemaErr := db.SchemaVersion(ctx, conn)
	if err := errors.Join(schemaErr, conn.Close()); err != nil {
		return err
	}
	m := Manifest{AppVersion: s.version, SchemaVersion: schema, CreatedAt: now.UTC()}
	seen := map[string]bool{}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, tw.Close(), gz.Close()) }()
	add := func(name, filename string) error {
		key := strings.ToLower(name)
		if seen[key] {
			return ErrUnsafeArchive
		}
		seen[key] = true
		if len(m.Files) >= maxEntries {
			return fmt.Errorf("backup exceeds %d files", maxEntries)
		}
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrUnsafeArchive
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: now.UTC()}); err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h), contextReader{ctx, f})
		if err != nil {
			return err
		}
		if n != info.Size() {
			return fmt.Errorf("backup file changed: %s", name)
		}
		m.Files = append(m.Files, File{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
		return nil
	}
	if err := add(db.FileName, snapshot); err != nil {
		return err
	}
	root := filepath.Join(s.dataDir, "media")
	if _, err := os.Lstat(root); err == nil {
		if err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return ErrUnsafeArchive
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(s.dataDir, filename)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if !safeName(name) {
				return ErrUnsafeArchive
			}
			return add(name, filename)
		}); err != nil {
			return fmt.Errorf("archive media: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(body) > maxManifestBytes {
		return fmt.Errorf("backup manifest exceeds %d bytes", maxManifestBytes)
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(body)), ModTime: now.UTC()}); err != nil {
		return err
	}
	_, err = tw.Write(body)
	return err
}

// safeName also rejects names that Windows aliases or interprets specially.
func safeName(name string) bool {
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00<>\"|?*") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	return name == db.FileName || name == manifestName || strings.HasPrefix(name, "media/")
}

// Validate fully extracts into same-volume temporary storage, verifies every
// checksum and SQLite integrity, then removes the staged files.
func (s *Service) Validate(ctx context.Context, r io.Reader) (m Manifest, err error) {
	stage, err := s.stageDir()
	if err != nil {
		return m, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	return extract(ctx, r, stage)
}

func extract(ctx context.Context, r io.Reader, stage string) (Manifest, error) {
	var m Manifest
	if err := ctx.Err(); err != nil {
		return m, err
	}
	gz, err := gzip.NewReader(contextReader{ctx, r})
	if err != nil {
		return m, fmt.Errorf("%w: %w", ErrInvalidArchive, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	actual := map[string]File{}
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("%w: %w", ErrInvalidArchive, err)
		}
		name := header.Name
		key := strings.ToLower(name)
		if !safeName(name) || seen[key] || header.Typeflag != tar.TypeReg || header.Linkname != "" {
			return m, ErrUnsafeArchive
		}
		seen[key] = true
		if len(seen) > maxEntries+1 {
			return m, fmt.Errorf("%w: too many files", ErrInvalidArchive)
		}
		if name == manifestName {
			if header.Size > maxManifestBytes {
				return m, fmt.Errorf("%w: manifest too large", ErrInvalidArchive)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				return m, err
			}
			if err := json.Unmarshal(body, &m); err != nil {
				return m, fmt.Errorf("%w: manifest: %v", ErrInvalidArchive, err)
			}
			continue
		}
		filename := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return m, err
		}
		f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return m, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, h), tr)
		if err := errors.Join(copyErr, f.Sync(), f.Close()); err != nil {
			return m, err
		}
		actual[name] = File{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	// Reading through the gzip footer detects truncated streams and CRC errors.
	n, err := io.Copy(io.Discard, gz)
	if err != nil || n != 0 {
		return m, fmt.Errorf("%w: gzip trailer: %v", ErrInvalidArchive, err)
	}
	if !seen[manifestName] || !seen[db.FileName] || m.AppVersion == "" || m.CreatedAt.IsZero() || len(m.Files) != len(actual) {
		return m, ErrInvalidArchive
	}
	listed := map[string]bool{}
	for _, file := range m.Files {
		if listed[file.Path] || !safeName(file.Path) || file.Path == manifestName {
			return m, ErrInvalidArchive
		}
		listed[file.Path] = true
		if got, ok := actual[file.Path]; !ok || got != file {
			return m, fmt.Errorf("%w: %s", ErrChecksum, file.Path)
		}
	}
	latest, err := db.LatestVersion()
	if err != nil {
		return m, err
	}
	if m.SchemaVersion < 1 || m.SchemaVersion > latest {
		return m, ErrSchema
	}
	if err := checkDatabase(ctx, filepath.Join(stage, db.FileName), m.SchemaVersion); err != nil {
		return m, err
	}
	if err := os.MkdirAll(filepath.Join(stage, "media"), 0o700); err != nil {
		return m, err
	}
	return m, nil
}

func checkDatabase(ctx context.Context, filename string, schema int) (err error) {
	conn, err := db.Open(ctx, filename)
	if err != nil {
		return fmt.Errorf("%w: sqlite: %v", ErrInvalidArchive, err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	var result string
	if err := conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("%w: sqlite integrity: %v", ErrInvalidArchive, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: sqlite integrity: %s", ErrInvalidArchive, result)
	}
	version, err := db.SchemaVersion(ctx, conn)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	if version != schema {
		return ErrSchema
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: foreign key violations", ErrInvalidArchive)
	}
	return rows.Err()
}
