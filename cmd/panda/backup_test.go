package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRestoreCLI(t *testing.T) {
	dir := t.TempDir()
	env := func(key string) string {
		if key == "PANDA_DATA_DIR" {
			return dir
		}
		return ""
	}
	cli := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(args, env, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	for _, command := range []string{"backup", "restore"} {
		for _, args := range [][]string{{command}, {command, "--unknown"}, {command, "extra"}} {
			if code, _, _ := cli(args...); code != 2 {
				t.Fatalf("%v exit=%d, want 2", args, code)
			}
		}
		if code, _, errOut := cli(command, "--help"); code != 0 || !strings.Contains(errOut, "stop the server") {
			t.Fatalf("%s help: exit=%d stderr=%q", command, code, errOut)
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if code, _, errOut := cli("backup", "--out", archive); code != 1 || !strings.Contains(errOut, "no database") {
		t.Fatalf("missing database: exit=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "pandadays.db")); !os.IsNotExist(err) {
		t.Fatal("backup created a new database")
	}
	if code, _, _ := cli("restore", "--file", archive); code != 1 {
		t.Fatalf("missing restore file exit=%d", code)
	}
	conn, err := openDatabase(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO users(id,username,email,password_hash) VALUES(1,'alice','alice@example.com','hash');
		INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('session',1,9999999999)`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := cli("backup", "--out", archive); code != 0 || !strings.Contains(out, "completed") {
		t.Fatalf("backup exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := cli("backup", "--out", archive); code != 1 {
		t.Fatal("backup must refuse to overwrite existing output")
	}
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing backup modified")
	}
	for _, filename := range []string{
		filepath.Join(dir, "pandadays.db"),
		filepath.Join(dir, "pandadays.db-wal"),
		filepath.Join(dir, "pandadays.db-shm"),
		filepath.Join(dir, "media", "bad.tar.gz"),
	} {
		if code, _, _ := cli("backup", "--out", filename); code != 1 {
			t.Fatalf("unsafe output accepted: %s", filename)
		}
	}
	conn, err = openDatabase(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE users SET username='changed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := cli("restore", "--file", archive); code != 0 || !strings.Contains(out, "completed") {
		t.Fatalf("restore exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	conn, err = openDatabase(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var username string
	var sessions int
	if err := conn.QueryRow(`SELECT username FROM users WHERE id=1`).Scan(&username); err != nil || username != "alice" {
		t.Fatalf("restored username=%q err=%v", username, err)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("restored sessions=%d err=%v", sessions, err)
	}
}

func TestBackupCLIConfigurationAndWriteErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, command := range []string{"backup", "restore"} {
		option := "--out"
		if command == "restore" {
			option = "--file"
		}
		env := func(key string) string {
			if key == "PANDA_LANG" {
				return "invalid"
			}
			return ""
		}
		if code := run([]string{command, option, "test.tar.gz"}, env, &out, &errOut); code != 1 {
			t.Fatalf("invalid config exit=%d", code)
		}
	}
	dir := t.TempDir()
	// An existing SQLite file without the application schema fails after the
	// output has been created. The failed archive must then be removed.
	if err := os.WriteFile(filepath.Join(dir, "pandadays.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(key string) string {
		if key == "PANDA_DATA_DIR" {
			return dir
		}
		return ""
	}
	archive := filepath.Join(t.TempDir(), "failed.tar.gz")
	if code := run([]string{"backup", "--out", archive}, env, &out, &errOut); code != 1 {
		t.Fatalf("invalid database exit=%d", code)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("partial archive left behind: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(bad, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"restore", "--file", bad}, env, &out, &errOut); code != 1 {
		t.Fatalf("invalid restore exit=%d", code)
	}
}
