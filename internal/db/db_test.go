package db_test

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"pandadays/internal/db"
	"pandadays/internal/db/dbtest"
	"pandadays/migrations"
)

var epoch = time.Unix(1_700_000_000, 0).UTC()

func TestFailedTableRebuildRollsBackAndRestoresForeignKeys(t *testing.T) {
	conn, _ := openRaw(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_initial.sql": {Data: []byte(`CREATE TABLE parent(id INTEGER PRIMARY KEY);
			CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id));
			INSERT INTO parent VALUES (1); INSERT INTO child VALUES (1, 1);`)},
		"0002_rebuild.sql": {Data: []byte("-- rebuild-tables\nDROP TABLE parent; SELECT nonexistent_column;")},
	}
	if err := db.MigrateFS(ctx, conn, fsys, epoch); err == nil {
		t.Fatal("invalid table rebuild succeeded")
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM parent WHERE id = 1`); n != 1 {
		t.Fatal("failed rebuild lost original data")
	}
	if n := count(t, conn, `PRAGMA foreign_keys`); n != 1 {
		t.Fatal("failed rebuild left foreign key enforcement disabled")
	}
	if _, err := conn.Exec(`INSERT INTO child VALUES (2, 999)`); err == nil {
		t.Fatal("failed rebuild allowed an invalid foreign key")
	}
	if version, err := db.SchemaVersion(ctx, conn); err != nil || version != 1 {
		t.Fatalf("failed rebuild advanced schema: %d %v", version, err)
	}
}

func TestDetachedAttachmentMigrationPreservesExistingMedia(t *testing.T) {
	conn, _ := openRaw(t)
	ctx := context.Background()
	old := fstest.MapFS{}
	all, err := db.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range all {
		if migration.Version >= 6 {
			continue
		}
		content, err := fs.ReadFile(migrations.FS(), migration.Name)
		if err != nil {
			t.Fatal(err)
		}
		old[migration.Name] = &fstest.MapFile{Data: content}
	}
	if err := db.MigrateFS(ctx, conn, old, epoch); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,username,email,password_hash,used_bytes) VALUES(1,'alice','alice@example.com','unused',4)`,
		`INSERT INTO collections(id,owner_id,name) VALUES(1,1,'Default')`,
		`INSERT INTO days(id,owner_id,collection_id,title,date) VALUES(1,1,1,'Trip','2026-10-18')`,
		`INSERT INTO attachments(id,day_id,owner_id,uuid,kind,filename,mime,size) VALUES(1,1,1,'original','image','photo.png','image/png',4)`,
		`UPDATE days SET style_bg_attachment_id = 1 WHERE id = 1`,
	} {
		if _, err := conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(ctx, conn, epoch); err != nil {
		t.Fatal(err)
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM attachments WHERE id = 1 AND detached = 0 AND uuid = 'original' AND size = 4`); n != 1 {
		t.Fatal("migration changed existing media")
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM days WHERE id = 1 AND style_bg_attachment_id = 1`); n != 1 {
		t.Fatal("migration cleared existing background")
	}
	if n := count(t, conn, `SELECT used_bytes FROM users WHERE id = 1`); n != 4 {
		t.Fatalf("migration changed account usage: %d", n)
	}
	if _, err := conn.Exec(`UPDATE attachments SET detached = 2 WHERE id = 1`); err == nil {
		t.Fatal("invalid detached state accepted")
	}
}

func TestInvitationMigrationPreservesExistingRecipients(t *testing.T) {
	conn, _ := openRaw(t)
	ctx := context.Background()
	old := fstest.MapFS{}
	all, err := db.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range all {
		if migration.Version >= 5 {
			continue
		}
		content, err := fs.ReadFile(migrations.FS(), migration.Name)
		if err != nil {
			t.Fatal(err)
		}
		old[migration.Name] = &fstest.MapFile{Data: content}
	}
	if err := db.MigrateFS(ctx, conn, old, epoch); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,username,email,password_hash) VALUES(1,'Alice','alice@example.com','unused'),(2,'Bob','bob@example.com','unused')`,
		`INSERT INTO shares(id,owner_id,target_type,target_id) VALUES(1,1,'day',1)`,
		`INSERT INTO share_recipients(share_id,recipient_id) VALUES(1,2)`,
	} {
		if _, err := conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(ctx, conn, epoch); err != nil {
		t.Fatal(err)
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM share_recipients WHERE recipient_id = 2 AND status = 'accepted'`); n != 1 {
		t.Fatalf("existing accepted recipients = %d", n)
	}
	if _, err := conn.Exec(`UPDATE share_recipients SET status = 'invalid'`); err == nil {
		t.Fatal("invalid invitation status accepted")
	}
}

func openRaw(t *testing.T) (*sql.DB, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), db.FileName)
	conn, err := db.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, file
}

func count(t *testing.T, conn *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func schemaSnapshot(t *testing.T, conn *sql.DB) string {
	t.Helper()
	rows, err := conn.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_schema ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, s string
		if err := rows.Scan(&typ, &name, &s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(typ + " " + name + " " + s + "\n")
	}
	return b.String()
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	conn, file := openRaw(t)

	if err := db.Migrate(ctx, conn, epoch); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before := schemaSnapshot(t, conn)

	if err := db.Migrate(ctx, conn, epoch.Add(time.Hour)); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if after := schemaSnapshot(t, conn); after != before {
		t.Fatalf("schema changed on re-run:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	all, err := db.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, conn, `SELECT COUNT(*) FROM schema_migrations`); got != len(all) {
		t.Fatalf("schema_migrations rows = %d, want %d", got, len(all))
	}
	if got := count(t, conn, `SELECT COUNT(*) FROM schema_migrations WHERE applied_at = ?`, epoch.Unix()); got != len(all) {
		t.Fatalf("applied_at rewritten on re-run")
	}
	if got := count(t, conn, `SELECT COUNT(*) FROM settings`); got != 5 {
		t.Fatalf("settings rows = %d, want 5 (defaults must not be duplicated)", got)
	}

	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.SchemaVersion(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if v != latest || v < 1 {
		t.Fatalf("schema version = %d, want %d", v, latest)
	}

	// Reopening the same file and migrating again is also a no-op.
	conn.Close()
	conn2, err := db.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if err := db.Migrate(ctx, conn2, epoch); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}
	if after := schemaSnapshot(t, conn2); after != before {
		t.Fatalf("schema changed after reopen")
	}
}

func TestInitSchemaTablesAndDefaults(t *testing.T) {
	conn := dbtest.Open(t)

	for _, table := range []string{
		"users", "sessions", "settings", "collections", "tags", "days",
		"day_tags", "attachments", "shares", "share_recipients", "share_hidden",
		"schema_migrations",
	} {
		if count(t, conn, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table) != 1 {
			t.Errorf("missing table %s", table)
		}
	}

	want := map[string]string{
		"session_days":           "30",
		"default_quota_days":     "1000",
		"default_quota_bytes":    "1073741824",
		"default_max_card_bytes": "104857600",
		"backup_retention":       "5",
	}
	for k, v := range want {
		var got string
		if err := conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&got); err != nil {
			t.Errorf("setting %s: %v", k, err)
			continue
		}
		if got != v {
			t.Errorf("setting %s = %q, want %q", k, got, v)
		}
	}
}

func TestPragmas(t *testing.T) {
	conn := dbtest.Open(t)
	ctx := context.Background()

	// Pragmas must hold on every pooled connection, not just the first.
	conns := make([]*sql.Conn, 3)
	for i := range conns {
		c, err := conn.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns[i] = c
	}
	for i, c := range conns {
		var fk, busy int
		var mode string
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 5000 || !strings.EqualFold(mode, "wal") {
			t.Errorf("conn %d: foreign_keys=%d busy_timeout=%d journal_mode=%s", i, fk, busy, mode)
		}
	}
}

func insertUser(t *testing.T, conn *sql.DB, name string) int64 {
	t.Helper()
	res, err := conn.Exec(`INSERT INTO users (username, email, password_hash) VALUES (?, ?, 'x')`,
		name, name+"@example.com")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestForeignKeysEnforced(t *testing.T) {
	conn := dbtest.Open(t)

	if _, err := conn.Exec(`INSERT INTO collections (owner_id, name) VALUES (999, 'x')`); err == nil {
		t.Fatal("insert with unknown owner_id succeeded; foreign keys not enforced")
	}

	uid := insertUser(t, conn, "alice")
	res, err := conn.Exec(`INSERT INTO collections (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`, uid)
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := res.LastInsertId()
	res, err = conn.Exec(`INSERT INTO tags (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`, uid)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = conn.Exec(`INSERT INTO days (owner_id, collection_id, title, date) VALUES (?, ?, 'Birthday', '2020-01-02')`, uid, cid)
	if err != nil {
		t.Fatal(err)
	}
	did, _ := res.LastInsertId()
	if _, err := conn.Exec(`INSERT INTO day_tags (day_id, tag_id) VALUES (?, ?)`, did, tid); err != nil {
		t.Fatal(err)
	}
	res, err = conn.Exec(`INSERT INTO attachments (day_id, owner_id, uuid, kind, filename, mime, size)
		VALUES (?, ?, 'u-1', 'image', 'a.png', 'image/png', 10)`, did, uid)
	if err != nil {
		t.Fatal(err)
	}
	aid, _ := res.LastInsertId()
	if _, err := conn.Exec(`UPDATE days SET style_bg_attachment_id = ? WHERE id = ?`, aid, did); err != nil {
		t.Fatal(err)
	}

	// RESTRICT: a collection with days cannot be deleted.
	if _, err := conn.Exec(`DELETE FROM collections WHERE id = ?`, cid); err == nil {
		t.Fatal("deleting a collection with days succeeded; want RESTRICT")
	}

	// SET NULL: deleting the background attachment clears the style reference.
	if _, err := conn.Exec(`DELETE FROM attachments WHERE id = ?`, aid); err != nil {
		t.Fatal(err)
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM days WHERE id = ? AND style_bg_attachment_id IS NULL`, did); n != 1 {
		t.Fatal("style_bg_attachment_id not cleared after attachment delete")
	}

	// CASCADE: deleting the user removes everything they own.
	if _, err := conn.Exec(`DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	for _, table := range []string{"collections", "tags", "days", "day_tags"} {
		if n := count(t, conn, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s rows after user delete = %d, want 0", table, n)
		}
	}
}

func TestSchemaConstraints(t *testing.T) {
	conn := dbtest.Open(t)
	uid := insertUser(t, conn, "bob")

	if _, err := conn.Exec(`INSERT INTO users (username, email, password_hash) VALUES ('BOB', 'other@example.com', 'x')`); err == nil {
		t.Error("username uniqueness should be case-insensitive")
	}
	if _, err := conn.Exec(`INSERT INTO users (username, email, password_hash) VALUES ('bob2', 'BOB@example.com', 'x')`); err == nil {
		t.Error("email uniqueness should be case-insensitive")
	}

	if _, err := conn.Exec(`INSERT INTO collections (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO collections (owner_id, name, is_reserved) VALUES (?, 'Other', 1)`, uid); err == nil {
		t.Error("a user must have at most one reserved collection")
	}
	if _, err := conn.Exec(`INSERT INTO collections (owner_id, name) VALUES (?, 'default')`, uid); err == nil {
		t.Error("collection names should be unique per owner, case-insensitively")
	}
	if _, err := conn.Exec(`INSERT INTO tags (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO tags (owner_id, name, is_reserved) VALUES (?, 'Other', 1)`, uid); err == nil {
		t.Error("a user must have at most one reserved tag")
	}

	var cid int64
	if err := conn.QueryRow(`SELECT id FROM collections WHERE owner_id = ?`, uid).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO days (owner_id, collection_id, title, date, repeat) VALUES (?, ?, 't', '2020-01-01', 'daily')`, uid, cid); err == nil {
		t.Error("repeat CHECK constraint not enforced")
	}
	if _, err := conn.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'tag', 1)`, uid); err == nil {
		t.Error("shares.target_type CHECK constraint not enforced")
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	conn, _ := openRaw(t)
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY); THIS IS NOT SQL;`)},
	}
	err := db.MigrateFS(ctx, conn, fsys, epoch)
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("err = %v, want failure naming 0002_b.sql", err)
	}
	if v, _ := db.SchemaVersion(ctx, conn); v != 1 {
		t.Fatalf("schema version = %d, want 1", v)
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM sqlite_schema WHERE name = 'b'`); n != 0 {
		t.Fatal("partial migration 0002 was not rolled back")
	}

	fsys["0002_b.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)}
	if err := db.MigrateFS(ctx, conn, fsys, epoch); err != nil {
		t.Fatalf("migrate after fix: %v", err)
	}
	if v, _ := db.SchemaVersion(ctx, conn); v != 2 {
		t.Fatalf("schema version = %d, want 2", v)
	}
}

func TestMigrateRejectsNewerDatabase(t *testing.T) {
	ctx := context.Background()
	conn, _ := openRaw(t)
	newer := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER);`)},
	}
	if err := db.MigrateFS(ctx, conn, newer, epoch); err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{"0001_a.sql": newer["0001_a.sql"]}
	if err := db.MigrateFS(ctx, conn, older, epoch); err == nil {
		t.Fatal("expected error migrating a database newer than the binary")
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"bad name":  {"init.sql": {Data: []byte(`SELECT 1;`)}},
		"duplicate": {"0001_a.sql": {Data: []byte(`SELECT 1;`)}, "001_b.sql": {Data: []byte(`SELECT 1;`)}},
		"zero":      {"0000_a.sql": {Data: []byte(`SELECT 1;`)}},
	}
	for name, fsys := range cases {
		if _, err := db.LoadMigrations(fsys); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	ms, err := db.LoadMigrations(fstest.MapFS{
		"0010_c.sql": {Data: []byte(`SELECT 1;`)},
		"0002_b.sql": {Data: []byte(`SELECT 1;`)},
		"README.md":  {Data: []byte(`ignored`)},
		"0001_a.sql": {Data: []byte(`SELECT 1;`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 || ms[0].Version != 1 || ms[1].Version != 2 || ms[2].Version != 10 {
		t.Fatalf("unexpected order: %+v", ms)
	}
}
