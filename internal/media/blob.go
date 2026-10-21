package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// ErrDuplicate means the owner already has an attachment with identical content.
var ErrDuplicate = errors.New("attachment already in media library")

// DuplicateError identifies the owner's existing attachment with the same
// content. ExistingID is 0 when the duplicate is another file in the same upload.
type DuplicateError struct{ ExistingID int64 }

func (e *DuplicateError) Error() string { return ErrDuplicate.Error() }
func (e *DuplicateError) Unwrap() error { return ErrDuplicate }

const blobDirName = "blobs"

// blobMu serializes blob placement with garbage collection for every Store in
// the process, so a blob is never removed while a new reference to it commits.
// Acquire it before beginning the database transaction that references a blob.
var blobMu sync.Mutex

// LockBlobs must be held while a transaction that adds blob references commits.
func (s *Store) LockBlobs() (unlock func()) {
	blobMu.Lock()
	return blobMu.Unlock
}

func validSum(sum string) bool {
	if len(sum) != sha256.Size*2 {
		return false
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) blobPath(sum string) string {
	return filepath.Join(s.root, blobDirName, sum[:2], sum)
}

// CreateTemp creates a temporary file on the same volume as the blob store.
func (s *Store) CreateTemp(pattern string) (*os.File, error) {
	dir := filepath.Join(s.root, blobDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create media blob directory: %w", err)
	}
	return os.CreateTemp(dir, pattern)
}

// Sum returns the hex SHA-256 of r.
func Sum(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PlaceBlob moves temp into the blob store unless identical content is
// already stored, and reports whether it created the blob. The caller must
// hold LockBlobs and call DiscardBlob for created blobs if its transaction fails.
func (s *Store) PlaceBlob(temp, sum string) (bool, error) {
	if !validSum(sum) {
		return false, fmt.Errorf("invalid blob hash %q", sum)
	}
	final := s.blobPath(sum)
	if _, err := os.Stat(final); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect media blob: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return false, fmt.Errorf("create media blob directory: %w", err)
	}
	if err := os.Rename(temp, final); err != nil {
		return false, fmt.Errorf("store media blob: %w", err)
	}
	return true, nil
}

// DiscardBlob removes a blob created by PlaceBlob whose transaction did not
// commit. The caller must still hold LockBlobs.
func (s *Store) DiscardBlob(sum string) error {
	if err := os.Remove(s.blobPath(sum)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove uncommitted media blob: %w", err)
	}
	return nil
}

// FindBySum returns the ID of an attachment the owner already has with the
// given content, or 0 if there is none.
func (s *Store) FindBySum(ctx context.Context, ownerID int64, sum string) (int64, error) {
	return findBySum(ctx, s.db, ownerID, sum)
}

func findBySum(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID int64, sum string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM attachments WHERE owner_id = ? AND sha256 = ?
		ORDER BY id LIMIT 1`, ownerID, sum).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find duplicate attachment: %w", err)
	}
	return id, nil
}

// Collect removes blob files that lost their last attachment reference.
// Deleting attachment rows (directly or via day/user cascades) queues their
// hashes; call Collect after such deletions commit and at startup.
func (s *Store) Collect(ctx context.Context) error {
	defer s.LockBlobs()()
	rows, err := s.db.QueryContext(ctx, `SELECT sha256 FROM blob_gc`)
	if err != nil {
		return fmt.Errorf("list unreferenced media blobs: %w", err)
	}
	var sums []string
	for rows.Next() {
		var sum string
		if err := rows.Scan(&sum); err != nil {
			rows.Close()
			return fmt.Errorf("scan unreferenced media blob: %w", err)
		}
		sums = append(sums, sum)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("list unreferenced media blobs: %w", err)
	}
	var errs []error
	for _, sum := range sums {
		var referenced bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE sha256 = ?)`, sum).
			Scan(&referenced); err != nil {
			errs = append(errs, fmt.Errorf("check media blob references: %w", err))
			continue
		}
		if !referenced && validSum(sum) {
			if err := os.Remove(s.blobPath(sum)); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove media blob: %w", err))
				continue
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM blob_gc WHERE sha256 = ?`, sum); err != nil {
			errs = append(errs, fmt.Errorf("dequeue media blob: %w", err))
		}
	}
	return errors.Join(errs...)
}

// MigrateLegacy moves files from the legacy media/{owner_id}/{uuid} layout
// into the shared blob store and records their hashes. Rows whose legacy file
// is missing are left unchanged. It is safe to rerun after an interruption.
func (s *Store) MigrateLegacy(ctx context.Context) (int, error) {
	type legacy struct {
		id, ownerID int64
		uuid        string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, uuid FROM attachments WHERE sha256 IS NULL ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list legacy attachments: %w", err)
	}
	var todo []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.ownerID, &l.uuid); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy attachment: %w", err)
		}
		todo = append(todo, l)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("list legacy attachments: %w", err)
	}
	moved := 0
	var errs []error
	owners := map[int64]bool{}
	for _, l := range todo {
		owners[l.ownerID] = true
		path := s.Path(l.ownerID, Attachment{UUID: l.uuid})
		if err := s.migrateOne(ctx, l.id, path); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("migrate attachment %d: %w", l.id, err))
			}
			continue
		}
		moved++
	}
	for ownerID := range owners {
		// Succeeds only once the legacy directory is empty.
		os.Remove(filepath.Join(s.root, strconv.FormatInt(ownerID, 10)))
	}
	return moved, errors.Join(errs...)
}

func (s *Store) migrateOne(ctx context.Context, id int64, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	temp, err := s.CreateTemp(".migrate-*")
	if err != nil {
		src.Close()
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(temp, h), src)
	src.Close()
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("copy legacy attachment: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))

	unlock := s.LockBlobs()
	created, err := s.PlaceBlob(tempName, sum)
	if err != nil {
		unlock()
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE attachments SET sha256 = ? WHERE id = ? AND sha256 IS NULL`, sum, id); err != nil {
		if created {
			err = errors.Join(err, s.DiscardBlob(sum))
		}
		unlock()
		return fmt.Errorf("record attachment hash: %w", err)
	}
	unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy attachment: %w", err)
	}
	return nil
}
