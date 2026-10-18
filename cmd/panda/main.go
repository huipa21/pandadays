// Command panda is the PandaDays web server and admin CLI.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"pandadays/internal/config"
	"pandadays/internal/db"
	"pandadays/internal/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage: panda <command> [options]

Commands:
  serve      Start the web server (default)
  version    Print the version
  help       Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "serve":
		return cmdServe(args, getenv, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "panda %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "panda: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdServe(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "panda serve: unexpected arguments %q\n\n%s", args, usage)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, err := config.Load(getenv)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := openDatabase(ctx, cfg.DataDir)
	if err != nil {
		logger.Error("init database", "err", err)
		return 1
	}
	defer database.Close()

	srv, err := web.New(cfg, logger)
	if err != nil {
		logger.Error("init server", "err", err)
		return 1
	}

	logger.Info("starting panda", "version", version, "addr", cfg.Addr, "data_dir", cfg.DataDir)
	if err := srv.Run(ctx); err != nil {
		logger.Error("server error", "err", err)
		return 1
	}
	return 0
}

// openDatabase opens <dataDir>/pandadays.db and applies pending migrations.
func openDatabase(ctx context.Context, dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	conn, err := db.Open(ctx, filepath.Join(dataDir, db.FileName))
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, conn, time.Now()); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
