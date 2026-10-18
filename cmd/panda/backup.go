package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pandadays/internal/backup"
	"pandadays/internal/config"
	"pandadays/internal/db"
)

func cmdBackup(command string, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	option := "out"
	if command == "restore" {
		option = "file"
	}
	filename := fs.String(option, "", "archive path (stop the server before running this command)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*filename) == "" {
		fmt.Fprintf(stderr, "Usage: panda %s --%s FILE (server must be stopped)\n", command, option)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "panda %s: invalid configuration: %v\n", command, err)
		return 1
	}
	service := backup.New(cfg.DataDir, version)
	ctx := context.Background()
	if command == "backup" {
		err = writeBackup(ctx, service, cfg.DataDir, *filename, time.Now())
	} else {
		file, openErr := os.Open(*filename)
		if openErr != nil {
			err = openErr
		} else {
			err = errors.Join(service.Restore(ctx, file, time.Now()), file.Close())
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "panda %s: %v\n", command, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s completed: %s\n", command, *filename)
	return 0
}

func writeBackup(ctx context.Context, service *backup.Service, dataDir, filename string, now time.Time) (err error) {
	databasePath := filepath.Join(dataDir, db.FileName)
	if info, err := os.Stat(databasePath); err != nil {
		return fmt.Errorf("no database in %s: %w", dataDir, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("database is not a regular file")
	}
	media, err := filepath.Abs(filepath.Join(dataDir, "media"))
	if err != nil {
		return err
	}
	output, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	live, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	for _, reserved := range []string{live, live + "-wal", live + "-shm", live + "-journal"} {
		if strings.EqualFold(output, reserved) {
			return fmt.Errorf("backup output must not replace database files")
		}
	}
	if strings.EqualFold(filepath.VolumeName(media), filepath.VolumeName(output)) {
		rel, err := filepath.Rel(media, output)
		if err != nil {
			return err
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("backup output must not be inside media")
		}
	}
	// Exclusive creation prevents overwriting a previous backup or live DB.
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(filename))
		}
	}()
	conn, err := db.Open(ctx, databasePath)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	err = service.Create(ctx, conn, file, now)
	return errors.Join(err, conn.Close(), file.Sync(), file.Close())
}
