package portability

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pandadays/internal/days"
)

var (
	ErrInvalidArchive  = errors.New("import file is not a valid zip or CSV")
	ErrUnsafeArchive   = errors.New("import archive contains unsafe paths or links")
	ErrTooManyEntries  = errors.New("import archive has too many entries")
	ErrArchiveTooLarge = errors.New("import file is too large")
	ErrMissingCSV      = errors.New("import archive has no days.csv")
	ErrInvalidCSV      = errors.New("days.csv is malformed")
	ErrStagedNotFound  = errors.New("staged import not found or expired")
)

// StagedTTL is how long an uploaded import waits for confirmation.
const StagedTTL = time.Hour

// source is an opened import: days.csv plus media entries keyed by path.
type source struct {
	zr    *zip.ReadCloser
	csv   []byte
	files map[string]*zip.File
	bare  bool
}

func (src *source) Close() error {
	if src.zr != nil {
		zr := src.zr
		src.zr = nil
		return zr.Close()
	}
	return nil
}

// openSource opens a zip export or a bare days.csv, rejecting unsafe archives
// before any entry is read.
func openSource(path string, limits Limits) (*source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open import: %w", err)
	}
	magic := make([]byte, 4)
	n, _ := io.ReadFull(f, magic)
	info, statErr := f.Stat()
	if statErr != nil {
		f.Close()
		return nil, fmt.Errorf("open import: %w", statErr)
	}
	if !bytes.Equal(magic[:n], []byte("PK\x03\x04")) && !bytes.Equal(magic[:n], []byte("PK\x05\x06")) {
		defer f.Close()
		if info.Size() > limits.MaxCSVBytes {
			return nil, ErrArchiveTooLarge
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("open import: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(f, limits.MaxCSVBytes))
		if err != nil {
			return nil, fmt.Errorf("read import: %w", err)
		}
		return &source{csv: data, bare: true}, nil
	}
	f.Close()

	zr, err := zip.OpenReader(path)
	if errors.Is(err, zip.ErrInsecurePath) {
		if zr != nil {
			zr.Close()
		}
		return nil, ErrUnsafeArchive
	}
	if err != nil {
		return nil, ErrInvalidArchive
	}
	src := &source{zr: zr, files: map[string]*zip.File{}}
	if err := src.index(limits); err != nil {
		zr.Close()
		return nil, err
	}
	return src, nil
}

func (src *source) index(limits Limits) error {
	if len(src.zr.File) > limits.MaxEntries {
		return ErrTooManyEntries
	}
	seen := map[string]bool{}
	var csvFile *zip.File
	var media uint64
	for _, f := range src.zr.File {
		name := f.Name
		mode := f.Mode()
		if !safeEntryName(name) || mode&fs.ModeSymlink != 0 || seen[strings.TrimSuffix(name, "/")] {
			return ErrUnsafeArchive
		}
		seen[strings.TrimSuffix(name, "/")] = true
		if mode.IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if !mode.IsRegular() {
			return ErrUnsafeArchive
		}
		switch {
		case name == csvName:
			if f.UncompressedSize64 > uint64(limits.MaxCSVBytes) {
				return ErrArchiveTooLarge
			}
			csvFile = f
		case strings.HasPrefix(name, mediaPrefix) && !strings.Contains(name[len(mediaPrefix):], "/"):
			media += f.UncompressedSize64
			if media > uint64(limits.MaxMediaBytes) || f.UncompressedSize64 > uint64(limits.MaxMediaBytes) {
				return ErrArchiveTooLarge
			}
			src.files[name] = f
		}
	}
	if csvFile == nil {
		return ErrMissingCSV
	}
	rc, err := csvFile.Open()
	if err != nil {
		return ErrInvalidArchive
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, int64(csvFile.UncompressedSize64)+1))
	if err != nil || uint64(len(data)) != csvFile.UncompressedSize64 {
		return ErrInvalidArchive
	}
	src.csv = data
	return nil
}

// safeEntryName rejects absolute paths, drive letters, backslashes and any
// "." or ".." element (zip-slip).
func safeEntryName(name string) bool {
	if name == "" || strings.ContainsAny(name, `\:`) || strings.ContainsRune(name, 0) {
		return false
	}
	return fs.ValidPath(strings.TrimSuffix(name, "/"))
}

// Stage stores an uploaded import for the owner and returns its token. Uploads
// larger than limit fail with ErrArchiveTooLarge. Expired staged uploads of
// all users are removed on the way. The first preview or confirmation binds
// the staged upload to that request's privacy context.
func (s *Service) Stage(ownerID int64, r io.Reader, limit int64, now time.Time) (string, error) {
	if err := os.MkdirAll(s.tmpDir, 0o700); err != nil {
		return "", fmt.Errorf("create import directory: %w", err)
	}
	s.sweepStaged(now)
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate import token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	path := s.stagedPath(ownerID, token)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create staged import: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	closeErr := f.Close()
	switch {
	case err != nil:
		os.Remove(path)
		return "", fmt.Errorf("store staged import: %w", err)
	case n > limit:
		os.Remove(path)
		return "", ErrArchiveTooLarge
	case closeErr != nil:
		os.Remove(path)
		return "", fmt.Errorf("store staged import: %w", closeErr)
	}
	return token, nil
}

// Discard removes a staged upload; unknown tokens are ignored.
func (s *Service) Discard(ownerID int64, token string) {
	if validToken(token) {
		path := s.stagedPath(ownerID, token)
		os.Remove(path)
		os.Remove(path + "-normal")
		os.Remove(path + "-private")
	}
}

func (s *Service) stagedPath(ownerID int64, token string) string {
	return filepath.Join(s.tmpDir, "import-"+strconv.FormatInt(ownerID, 10)+"-"+token)
}

// staged resolves a token for its owner and privacy mode. Renaming the initial
// file atomically binds its first use, including across service instances.
func (s *Service) staged(ctx context.Context, ownerID int64, token string, now time.Time) (string, error) {
	if !validToken(token) {
		return "", ErrStagedNotFound
	}
	path := s.stagedPath(ownerID, token)
	bound := path + "-normal"
	if days.IsPrivate(ctx) {
		bound = path + "-private"
	}
	valid := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) <= StagedTTL
	}
	if valid(bound) {
		return bound, nil
	}
	if valid(path) {
		// Another request may have bound the file since the initial check.
		if err := os.Rename(path, bound); err == nil || valid(bound) {
			return bound, nil
		}
	}
	return "", ErrStagedNotFound
}

func validToken(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == token
}

func (s *Service) sweepStaged(now time.Time) {
	entries, err := os.ReadDir(s.tmpDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "import-") {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > StagedTTL {
			os.Remove(filepath.Join(s.tmpDir, e.Name()))
		}
	}
}
