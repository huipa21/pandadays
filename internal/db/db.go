// Package db opens the SQLite database and applies embedded schema migrations.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"pandadays/migrations"
)

// FileName is the database file name inside the data directory.
const FileName = "pandadays.db"

// Pragmas are applied to every pooled connection. _txlock=immediate makes
// BEGIN take the write lock up front, avoiding SQLITE_BUSY on lock upgrades.
const dsnParams = "?_pragma=foreign_keys(1)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_txlock=immediate"

// Open opens (creating if needed) the SQLite database at file and verifies
// that foreign key enforcement is active. It does not run migrations.
func Open(ctx context.Context, file string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", file+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		db.Close()
		return nil, fmt.Errorf("check foreign_keys: %w", err)
	}
	if fk != 1 {
		db.Close()
		return nil, errors.New("sqlite: foreign key enforcement is not enabled")
	}
	return db, nil
}

// Migration is a single numbered schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrationName = regexp.MustCompile(`^(\d+)_[A-Za-z0-9_\-]+\.sql$`)

// LoadMigrations reads NNNN_name.sql files from the root of fsys, sorted by version.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %q: name must match NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: invalid version", e.Name())
		}
		if prev, ok := seen[v]; ok {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prev, e.Name(), v)
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: v, Name: e.Name(), SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// LatestVersion returns the highest migration version embedded in the binary.
func LatestVersion() (int, error) {
	ms, err := LoadMigrations(migrations.FS())
	if err != nil {
		return 0, err
	}
	if len(ms) == 0 {
		return 0, nil
	}
	return ms[len(ms)-1].Version, nil
}

// Migrate applies all pending embedded migrations.
func Migrate(ctx context.Context, db *sql.DB, now time.Time) error {
	return MigrateFS(ctx, db, migrations.FS(), now)
}

// MigrateFS applies pending migrations from fsys. Each migration runs in its
// own transaction together with its schema_migrations record, so a failure
// leaves the database at the last fully applied version. Running it again is
// a no-op.
func MigrateFS(ctx context.Context, db *sql.DB, fsys fs.FS, now time.Time) error {
	ms, err := LoadMigrations(fsys)
	if err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	current, err := SchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if len(ms) > 0 && current > ms[len(ms)-1].Version {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)",
			current, ms[len(ms)-1].Version)
	}

	for _, m := range ms {
		if err := applyMigration(ctx, db, m, now); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m Migration, now time.Time) (result error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration %s: connection: %w", m.Name, err)
	}
	defer conn.Close()
	// SQLite table rebuilds need foreign keys disabled before BEGIN; validate
	// the rebuilt schema before committing and restore enforcement afterwards.
	if strings.HasPrefix(m.SQL, "-- rebuild-tables\n") || strings.HasPrefix(m.SQL, "-- rebuild-tables\r\n") {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("migration %s: disable foreign keys: %w", m.Name, err)
		}
		defer func() {
			if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
				result = errors.Join(result, fmt.Errorf("migration %s: restore foreign keys: %w", m.Name, err))
				// A connection without enforcement must never reenter the pool.
				conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.Name, err)
	}
	defer tx.Rollback()

	// Re-check inside the write transaction in case another process applied it.
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&exists); err != nil {
		return fmt.Errorf("migration %s: check applied: %w", m.Name, err)
	}
	if exists > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("migration %s: %w", m.Name, err)
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return fmt.Errorf("migration %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, now.Unix()); err != nil {
		return fmt.Errorf("migration %s: record: %w", m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.Name, err)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign_key_check: violations found")
	}
	return rows.Err()
}

// SchemaVersion returns the highest applied migration version, or 0 if none.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}
