package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if k == "PANDA_LANG" {
			return "xx"
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
