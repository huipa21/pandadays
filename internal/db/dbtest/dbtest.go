// Package dbtest provides helpers for tests that need a migrated database.
package dbtest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"pandadays/internal/db"
)

// Open returns a fully migrated database backed by a file in t.TempDir().
// A temp file (rather than :memory:) keeps every pooled connection on the
// same database. The database is closed when the test ends.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), db.FileName))
	if err != nil {
		t.Fatalf("dbtest: open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(ctx, conn, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("dbtest: migrate: %v", err)
	}
	return conn
}
