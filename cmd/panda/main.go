// Command panda is the PandaDays web server and admin CLI.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"pandadays/internal/auth"
	"pandadays/internal/config"
	"pandadays/internal/db"
	"pandadays/internal/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage: panda <command> [options]

Commands:
  serve                                 Start the web server (default)
  reset-admin-password --username NAME  Set a random password for an admin
                                        and sign them out everywhere
  backup --out FILE                     Write a site backup (server stopped)
  restore --file FILE                   Restore a site backup (server stopped)
  version                               Print the version
  help                                  Show this help
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
	case "reset-admin-password":
		return cmdResetAdminPassword(args, getenv, stdout, stderr)
	case "backup", "restore":
		return cmdBackup(cmd, args, getenv, stdout, stderr)
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

	if err := issueSetupToken(ctx, database, stdout, logger, time.Now()); err != nil {
		logger.Error("issue setup token", "err", err)
		return 1
	}

	srv, err := web.New(cfg, logger, database)
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

// issueSetupToken prints a new one-time setup token while no admin exists
// (SPEC §4.1). The raw token goes only to stdout, never to the structured log.
func issueSetupToken(ctx context.Context, database *sql.DB, stdout io.Writer, logger *slog.Logger, now time.Time) error {
	token, expires, err := auth.EnsureSetupToken(ctx, database, now)
	if err != nil {
		return err
	}
	switch {
	case token != "":
		fmt.Fprintf(stdout, `
================================================================
 PandaDays initial setup
 Open /setup in your browser and enter this one-time token:

     %s

 Expires at %s (valid for 1 hour).
 If it expires, restart the server to get a new token.
================================================================

`, token, expires.Format(time.RFC3339))
		logger.Info("setup token issued", "expires_at", expires)
	case !expires.IsZero():
		logger.Warn("setup pending: a setup token printed earlier is still valid; restart after it expires to get a new one",
			"expires_at", expires)
	}
	return nil
}

// cmdResetAdminPassword implements SPEC §4.4. The new password goes to stdout
// only; it is never logged.
func cmdResetAdminPassword(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-admin-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "username of the admin account to reset")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || strings.TrimSpace(*username) == "" {
		fmt.Fprintf(stderr, "Usage: panda reset-admin-password --username <name>\n")
		return 2
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "panda reset-admin-password: invalid configuration: %v\n", err)
		return 1
	}
	// Refuse to create a fresh database when pointed at the wrong data dir.
	if _, err := os.Stat(filepath.Join(cfg.DataDir, db.FileName)); err != nil {
		fmt.Fprintf(stderr, "panda reset-admin-password: no database in %s: %v\n", cfg.DataDir, err)
		return 1
	}

	ctx := context.Background()
	database, err := openDatabase(ctx, cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "panda reset-admin-password: open database: %v\n", err)
		return 1
	}
	defer database.Close()

	u, password, err := auth.ResetAdminPassword(ctx, database, *username, time.Now())
	switch {
	case errors.Is(err, auth.ErrUserNotFound), errors.Is(err, auth.ErrNotAdmin):
		fmt.Fprintf(stderr, "panda reset-admin-password: %q: %v\n", strings.TrimSpace(*username), err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "panda reset-admin-password: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Password for admin %q has been reset.\n\n    %s\n\n"+
		"All of this account's sessions were signed out. A password change is required at next login.\n",
		u.Username, password)
	if !u.IsActive {
		fmt.Fprintln(stderr, "warning: this account is deactivated and cannot sign in until reactivated.")
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
