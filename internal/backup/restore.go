package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"pandadays/internal/db"
)

// Restore validates and stages the complete archive before changing live data.
// The caller must have stopped the server and closed all database handles.
// Existing data is backed up first; backups and tmp are never swapped.
// Each rename is atomic, with rollback across the database/media pair. This is
// not a crash-recovery journal: callers must not interrupt the process mid-swap.
func (s *Service) Restore(ctx context.Context, r io.Reader, now time.Time) (err error) {
	stage, err := s.stageDir()
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			err = errors.Join(err, os.RemoveAll(stage))
		}
	}()
	if _, err := extract(ctx, r, stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	liveDB := filepath.Join(s.dataDir, db.FileName)
	if info, statErr := os.Lstat(liveDB); statErr == nil {
		if !info.Mode().IsRegular() {
			return ErrUnsafeArchive
		}
		if err := s.preRestore(ctx, now); err != nil {
			return fmt.Errorf("pre-restore backup: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	type move struct{ from, to string }
	var moves []move
	moveFile := func(from, to string) error {
		if err := s.rename(from, to); err != nil {
			return err
		}
		moves = append(moves, move{from, to})
		return nil
	}
	defer func() {
		if err == nil {
			return
		}
		var rollbackErr error
		for i := len(moves) - 1; i >= 0; i-- {
			m := moves[i]
			rollbackErr = errors.Join(rollbackErr, s.rename(m.to, m.from))
		}
		if rollbackErr != nil {
			cleanup = false
			err = errors.Join(err, fmt.Errorf("%w; recovery files retained in %s: %w", ErrRollback, stage, rollbackErr))
		}
	}()

	for _, name := range []string{db.FileName, db.FileName + "-wal", db.FileName + "-shm", "media"} {
		from := filepath.Join(s.dataDir, name)
		info, statErr := os.Lstat(from)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || (name == "media" && !info.IsDir()) || (name != "media" && !info.Mode().IsRegular()) {
			return ErrUnsafeArchive
		}
		if err := moveFile(from, filepath.Join(stage, name+".old")); err != nil {
			return fmt.Errorf("save old %s: %w", name, err)
		}
	}
	for _, name := range []string{db.FileName, "media"} {
		if err := moveFile(filepath.Join(stage, name), filepath.Join(s.dataDir, name)); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}
	if err := reopen(ctx, liveDB, now); err != nil {
		return fmt.Errorf("reopen restored database: %w", err)
	}
	return nil
}

func reopen(ctx context.Context, filename string, now time.Time) (err error) {
	conn, err := db.Open(ctx, filename)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, conn.Close())
		// After close no handle uses these sidecars. Remove them even when
		// migration fails, before the old database can be rolled back.
		for _, suffix := range []string{"-wal", "-shm"} {
			if removeErr := os.Remove(filename + suffix); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if err := db.Migrate(ctx, conn, now); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "DELETE FROM sessions")
	return err
}

func (s *Service) preRestore(ctx context.Context, now time.Time) (err error) {
	directory := filepath.Join(s.dataDir, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "pre-restore-"+now.UTC().Format("20060102T150405Z")+"-*.tar.gz")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(file.Name()))
		}
	}()
	conn, err := db.Open(ctx, filepath.Join(s.dataDir, db.FileName))
	if err != nil {
		return errors.Join(err, file.Close())
	}
	err = s.Create(ctx, conn, file, now)
	return errors.Join(err, conn.Close(), file.Sync(), file.Close())
}
