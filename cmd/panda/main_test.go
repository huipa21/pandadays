package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
)

func noenv(string) string { return "" }

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, noenv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(out.String(), "panda ") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, noenv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"frobnicate"}, noenv, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") || !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunServeInvalidConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	env := func(k string) string {
		if k == "COOKIE_SECURE" {
			return "invalid"
		}
		return ""
	}
	if code := run(nil, env, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "invalid configuration") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestOpenDatabaseCreatesAndMigrates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	for i := 0; i < 2; i++ {
		conn, err := openDatabase(context.Background(), dir)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n < 1 {
			t.Fatalf("run %d: schema_migrations count=%d err=%v", i, n, err)
		}
		conn.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "pandadays.db")); err != nil {
		t.Fatalf("db file: %v", err)
	}
}

func TestRunServeRejectsArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "extra"}, noenv, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestIssueSetupTokenPrintsBannerOnce(t *testing.T) {
	conn, err := openDatabase(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	now := time.Now()

	var first bytes.Buffer
	if err := issueSetupToken(context.Background(), conn, &first, logger, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), "/setup") || !strings.Contains(first.String(), "Expires at") {
		t.Fatalf("banner = %q", first.String())
	}
	var hash string
	if err := conn.QueryRow(`SELECT value FROM settings WHERE key = 'setup_token_hash'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.String(), hash) {
		t.Fatal("banner must print the raw token, not the stored hash")
	}

	var second bytes.Buffer
	if err := issueSetupToken(context.Background(), conn, &second, logger, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if second.Len() != 0 || !strings.Contains(logs.String(), "setup pending") {
		t.Fatalf("unexpired token reissued: stdout=%q", second.String())
	}
}

func TestResetAdminPassword(t *testing.T) {
	dir := t.TempDir()
	env := func(k string) string {
		if k == "DATA_DIR" {
			return dir
		}
		return ""
	}
	reset := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(append([]string{"reset-admin-password"}, args...), env, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, _, _ := reset(); code != 2 {
		t.Fatalf("missing --username: exit = %d, want 2", code)
	}
	if code, _, errOut := reset("--username", "admin"); code != 1 || !strings.Contains(errOut, "no database") {
		t.Fatalf("missing db: exit = %d, stderr = %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "pandadays.db")); !os.IsNotExist(err) {
		t.Fatal("reset must not create a database")
	}

	ctx := context.Background()
	conn, err := openDatabase(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(conn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	admin, err := svc.CreateUser(ctx, auth.CreateUserParams{
		Username: "admin", Email: "admin@example.com", Password: "password123", IsAdmin: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, auth.CreateUserParams{
		Username: "bob", Email: "bob@example.com", Password: "password123",
	}, now); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, admin.ID, true, now)
	if err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := reset("--username", "bob"); code != 1 || !strings.Contains(errOut, "not an admin") {
		t.Fatalf("non-admin: exit = %d, stderr = %q", code, errOut)
	}
	if code, _, errOut := reset("--username", "ghost"); code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("unknown: exit = %d, stderr = %q", code, errOut)
	}

	code, out, errOut := reset("--username", "admin")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if _, err := svc.LookupSession(ctx, token, now); !errors.Is(err, auth.ErrSession) {
		t.Fatalf("session kept after reset: %v", err)
	}
	var password string
	for _, f := range strings.Fields(out) {
		if len(f) == 16 {
			if _, err := svc.Authenticate(ctx, "ip", "admin", f, now); err == nil {
				password = f
			}
		}
	}
	if password == "" {
		t.Fatalf("printed password does not authenticate: %q", out)
	}
	var must bool
	if err := conn.QueryRow(`SELECT must_change_password FROM users WHERE id = ?`, admin.ID).Scan(&must); err != nil || !must {
		t.Fatalf("must_change_password = %v, err = %v", must, err)
	}
	conn.Close()
}
