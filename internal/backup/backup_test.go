package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pandadays/internal/db"
)

var testNow = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

func seed(t *testing.T, dir, title string) *sql.DB {
	t.Helper()
	conn, err := db.Open(context.Background(), filepath.Join(dir, db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(context.Background(), conn, testNow); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO users(id,username,email,password_hash,is_admin,used_bytes) VALUES(1,'admin','a@example.com','hash',1,4),(2,'bob','b@example.com','hash',0,0)`,
		`INSERT INTO collections(id,owner_id,name,is_reserved) VALUES(1,1,'Default',1),(2,2,'Default',1)`,
		`INSERT INTO tags(id,owner_id,name,is_reserved) VALUES(1,1,'Default',1)`,
		`INSERT INTO days(id,owner_id,collection_id,title,date,comment) VALUES(1,1,1,'day','2026-10-18','private'),(2,2,2,'other owner','2025-01-01',NULL)`,
		`INSERT INTO day_tags(day_id,tag_id) VALUES(1,1)`,
		`INSERT INTO attachments(id,day_id,owner_id,uuid,kind,filename,mime,size) VALUES(1,1,1,'uuid','file','test.txt','text/plain',4)`,
		`UPDATE days SET style_bg_attachment_id=1 WHERE owner_id=1 AND id=1`,
		`INSERT INTO shares(id,owner_id,target_type,target_id,show_comment) VALUES(1,1,'day',1,1)`,
		`INSERT INTO share_recipients(share_id,recipient_id) VALUES(1,2)`,
		`INSERT INTO share_hidden(user_id,day_id) VALUES(2,1)`,
		`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('secret-hash',1,9999999999)`,
	}
	for _, statement := range statements {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(`UPDATE days SET title=? WHERE owner_id=1 AND id=1`, title); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "media", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "media", "1", "uuid"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	return conn
}

func create(t *testing.T, dir string, conn *sql.DB) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := New(dir, "test-version").Create(context.Background(), conn, &out, testNow); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func dump(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"schema_migrations", "users", "settings", "collections", "tags", "days", "day_tags", "attachments", "shares", "share_recipients", "share_hidden"} {
		rows, err := conn.Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var all [][]any
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			all = append(all, values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(all)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(data)
	}
	return result
}

func assertEmptyStage(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "backup-") {
			t.Fatalf("temporary stage leaked: %s", entry.Name())
		}
	}
}

func TestCreateRestoreEquality(t *testing.T) {
	source := t.TempDir()
	conn := seed(t, source, "original")
	want := dump(t, conn)
	archive := create(t, source, conn)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			target := t.TempDir()
			if existing {
				old := seed(t, target, "replaced")
				if err := old.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "media", "1", "extra"), []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			service := New(target, "test")
			m, err := service.Validate(context.Background(), bytes.NewReader(archive))
			if err != nil || m.AppVersion != "test-version" || !m.CreatedAt.Equal(testNow) || len(m.Files) != 2 {
				t.Fatalf("manifest=%+v err=%v", m, err)
			}
			if err := service.Restore(context.Background(), bytes.NewReader(archive), testNow); err != nil {
				t.Fatal(err)
			}
			restored, err := db.Open(context.Background(), filepath.Join(target, db.FileName))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if got := dump(t, restored); !reflect.DeepEqual(got, want) {
				t.Fatalf("restored data differs:\ngot=%v\nwant=%v", got, want)
			}
			var sessions int
			if err := restored.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("sessions=%d err=%v", sessions, err)
			}
			data, err := os.ReadFile(filepath.Join(target, "media", "1", "uuid"))
			if err != nil || string(data) != "data" {
				t.Fatalf("media=%q err=%v", data, err)
			}
			if _, err := os.Stat(filepath.Join(target, "media", "1", "extra")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old media retained")
			}
			if existing {
				backups, err := filepath.Glob(filepath.Join(target, "backups", "pre-restore-*.tar.gz"))
				if err != nil || len(backups) != 1 {
					t.Fatalf("pre-restore backups=%v err=%v", backups, err)
				}
				file, err := os.Open(backups[0])
				if err != nil {
					t.Fatal(err)
				}
				_, validateErr := service.Validate(context.Background(), file)
				if err := errors.Join(validateErr, file.Close()); err != nil {
					t.Fatal(err)
				}
			}
			assertEmptyStage(t, target)
		})
	}
	assertEmptyStage(t, source)
}

type entry struct {
	header tar.Header
	body   []byte
}

func entries(t *testing.T, archive []byte) []entry {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []entry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{*h, body})
	}
}

func pack(t *testing.T, entries []entry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		e.header.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRejectInvalidArchivesBeforeSwap(t *testing.T) {
	source := t.TempDir()
	conn := seed(t, source, "source")
	archive := create(t, source, conn)
	tests := []struct {
		name string
		edit func([]entry) []entry
		want error
	}{
		{"checksum", func(es []entry) []entry { es[1].body[0] ^= 1; return es }, ErrChecksum},
		{"missing manifest", func(es []entry) []entry { return es[:len(es)-1] }, ErrInvalidArchive},
		{"duplicate", func(es []entry) []entry { return append(es, es[0]) }, ErrUnsafeArchive},
		{"unlisted", func(es []entry) []entry {
			return append(es, entry{tar.Header{Name: "media/extra", Mode: 0o600, Typeflag: tar.TypeReg}, []byte("x")})
		}, ErrInvalidArchive},
		{"new schema", func(es []entry) []entry {
			var m Manifest
			if err := json.Unmarshal(es[len(es)-1].body, &m); err != nil {
				t.Fatal(err)
			}
			m.SchemaVersion = 9999
			es[len(es)-1].body, _ = json.Marshal(m)
			return es
		}, ErrSchema},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			old := seed(t, dir, "keep")
			want := dump(t, old)
			old.Close()
			bad := pack(t, tt.edit(entries(t, archive)))
			err := New(dir, "test").Restore(context.Background(), bytes.NewReader(bad), testNow)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err=%v want=%v", err, tt.want)
			}
			unchanged, err := db.Open(context.Background(), filepath.Join(dir, db.FileName))
			if err != nil {
				t.Fatal(err)
			}
			defer unchanged.Close()
			if !reflect.DeepEqual(dump(t, unchanged), want) {
				t.Fatal("invalid archive changed live data")
			}
			if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid archive reached pre-restore step")
			}
			assertEmptyStage(t, dir)
		})
	}
	for _, bad := range [][]byte{archive[:len(archive)-4], []byte("not gzip")} {
		if _, err := New(t.TempDir(), "test").Validate(context.Background(), bytes.NewReader(bad)); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("corrupt gzip accepted: %v", err)
		}
	}
}

func TestRejectTarSlipAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `C:/escape`, `media\..\escape`, "media/../escape", "media/./file", "media/NUL.txt", "media/file.", "media/file:stream"} {
		t.Run(name, func(t *testing.T) {
			data := pack(t, []entry{{tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600}, []byte("bad")}})
			_, err := New(t.TempDir(), "test").Validate(context.Background(), bytes.NewReader(data))
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("unsafe path accepted: %v", err)
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeFifo} {
		data := pack(t, []entry{{tar.Header{Name: "media/link", Typeflag: kind, Linkname: "../escape"}, nil}})
		_, err := New(t.TempDir(), "test").Validate(context.Background(), bytes.NewReader(data))
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("unsafe type %d accepted: %v", kind, err)
		}
	}
}

func TestMidSwapRollback(t *testing.T) {
	source := t.TempDir()
	conn := seed(t, source, "source")
	archive := create(t, source, conn)
	for failAt := 1; failAt <= 4; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			target := t.TempDir()
			old := seed(t, target, "keep")
			want := dump(t, old)
			old.Close()
			if err := os.WriteFile(filepath.Join(target, "media", "1", "uuid"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			service := New(target, "test")
			injected := errors.New("simulated rename failure")
			calls := 0
			service.rename = func(from, to string) error {
				calls++
				if calls == failAt {
					return injected
				}
				return os.Rename(from, to)
			}
			err := service.Restore(context.Background(), bytes.NewReader(archive), testNow)
			if !errors.Is(err, injected) {
				t.Fatalf("err=%v", err)
			}
			checkRollback(t, target, want)
			assertEmptyStage(t, target)
		})
	}
}

func checkRollback(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	conn, err := db.Open(context.Background(), filepath.Join(dir, db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !reflect.DeepEqual(dump(t, conn), want) {
		t.Fatal("old database not restored")
	}
	var sessions int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("old sessions lost: count=%d err=%v", sessions, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "media", "1", "uuid"))
	if err != nil || string(body) != "keep" {
		t.Fatalf("old media not restored: %q %v", body, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "backups", "pre-restore-*.tar.gz"))
	if err != nil || len(files) != 1 {
		t.Fatal("pre-restore snapshot not retained")
	}
}

func TestReopenFailureRollsBack(t *testing.T) {
	source := t.TempDir()
	conn := seed(t, source, "source")
	if _, err := conn.Exec(`DROP TABLE sessions`); err != nil {
		t.Fatal(err)
	}
	archive := create(t, source, conn)
	target := t.TempDir()
	old := seed(t, target, "keep")
	want := dump(t, old)
	old.Close()
	if err := os.WriteFile(filepath.Join(target, "media", "1", "uuid"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New(target, "test").Restore(context.Background(), bytes.NewReader(archive), testNow)
	if err == nil || !strings.Contains(err.Error(), "reopen restored database") {
		t.Fatalf("err=%v", err)
	}
	checkRollback(t, target, want)
	assertEmptyStage(t, target)
}

func TestRollbackFailureRetainsRecoveryFiles(t *testing.T) {
	source := t.TempDir()
	conn := seed(t, source, "source")
	archive := create(t, source, conn)
	target := t.TempDir()
	old := seed(t, target, "keep")
	old.Close()
	service := New(target, "test")
	calls := 0
	injected := errors.New("rename unavailable")
	service.rename = func(from, to string) error {
		calls++
		if calls >= 4 {
			return injected
		}
		return os.Rename(from, to)
	}
	err := service.Restore(context.Background(), bytes.NewReader(archive), testNow)
	if !errors.Is(err, injected) || !errors.Is(err, ErrRollback) || !strings.Contains(err.Error(), "recovery files retained") {
		t.Fatalf("rollback failure not surfaced: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(target, "tmp", "backup-*"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("recovery stage lost: %v %v", stages, err)
	}
	for _, name := range []string{db.FileName + ".old", "media.old"} {
		if _, err := os.Stat(filepath.Join(stages[0], name)); err != nil {
			t.Fatalf("recovery copy %s lost: %v", name, err)
		}
	}
}

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCreateFailureAndCancellation(t *testing.T) {
	dir := t.TempDir()
	conn := seed(t, dir, "source")
	service := New(dir, "test")
	injected := errors.New("writer failed")
	if err := service.Create(context.Background(), conn, brokenWriter{injected}, testNow); !errors.Is(err, injected) {
		t.Fatalf("writer error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Create(ctx, conn, io.Discard, testNow); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	assertEmptyStage(t, dir)
}

func TestEmptyMedia(t *testing.T) {
	source := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(source, db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := db.Migrate(context.Background(), conn, testNow); err != nil {
		t.Fatal(err)
	}
	archive := create(t, source, conn)
	target := t.TempDir()
	if err := New(target, "test").Restore(context.Background(), bytes.NewReader(archive), testNow); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(target, "media")); err != nil || !info.IsDir() {
		t.Fatalf("empty media missing: %v", err)
	}
}
