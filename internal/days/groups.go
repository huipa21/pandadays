package days

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxGroupNameRunes bounds collection and tag names.
const MaxGroupNameRunes = 100

var (
	ErrNotFound     = errors.New("not found")
	ErrReserved     = errors.New("the reserved default cannot be renamed or deleted")
	ErrNameRequired = errors.New("name is required")
	ErrNameTooLong  = errors.New("name is too long")
	ErrNameReserved = errors.New("name is reserved")
	ErrNameTaken    = errors.New("name is already in use")
	ErrColorInvalid = errors.New("color must be #rrggbb")
)

// reservedNames are the stored and localized display names of the reserved
// default; users may not create another group that would look identical.
var reservedNames = []string{"Default", "默认"}

var colorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// GroupKind selects the table a Groups store manages. Collections and tags
// share the same CRUD rules; only their delete side effects differ.
type GroupKind int

const (
	KindCollection GroupKind = iota
	KindTag
)

func (k GroupKind) table() string {
	if k == KindTag {
		return "tags"
	}
	return "collections"
}

// dayCountQuery counts days per group; for collections every day belongs to
// exactly one collection, for tags it counts tagged days.
func (k GroupKind) dayCountQuery() string {
	if k == KindTag {
		return `(SELECT COUNT(*) FROM day_tags dt WHERE dt.tag_id = g.id)`
	}
	return `(SELECT COUNT(*) FROM days d WHERE d.collection_id = g.id)`
}

type Group struct {
	ID        int64
	Kind      GroupKind
	Name      string
	Color     string // "#rrggbb" or empty
	Reserved  bool
	DayCount  int
	CreatedAt time.Time
}

// GroupInput is user-supplied data for create/update. Name is ignored when
// updating the reserved default if it is empty or unchanged.
type GroupInput struct {
	Name  string
	Color string
}

// Groups is an owner-scoped repository for collections or tags. Every method
// takes ownerID; rows of other owners behave as if they do not exist.
type Groups struct {
	db   *sql.DB
	kind GroupKind
}

func NewCollections(db *sql.DB) *Groups { return &Groups{db: db, kind: KindCollection} }
func NewTags(db *sql.DB) *Groups        { return &Groups{db: db, kind: KindTag} }

func (g *Groups) Kind() GroupKind { return g.kind }

// List returns the owner's groups, reserved default first, then by name.
func (g *Groups) List(ctx context.Context, ownerID int64) ([]Group, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT g.id, g.name, COALESCE(g.color, ''), g.is_reserved, g.created_at, `+
		g.kind.dayCountQuery()+` FROM `+g.kind.table()+` g WHERE g.owner_id = ?
		ORDER BY g.id`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", g.kind.table(), err)
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		grp, err := g.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, grp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", g.kind.table(), err)
	}
	// SQLite NOCASE only folds ASCII, so order names here.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Reserved != b.Reserved {
			return a.Reserved
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// Get returns ErrNotFound when the group does not exist or belongs to someone else.
func (g *Groups) Get(ctx context.Context, ownerID, id int64) (Group, error) {
	return g.get(ctx, g.db, ownerID, id)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

func (g *Groups) get(ctx context.Context, q querier, ownerID, id int64) (Group, error) {
	row := q.QueryRowContext(ctx, `SELECT g.id, g.name, COALESCE(g.color, ''), g.is_reserved, g.created_at, `+
		g.kind.dayCountQuery()+` FROM `+g.kind.table()+` g WHERE g.owner_id = ? AND g.id = ?`, ownerID, id)
	grp, err := g.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return grp, err
}

func (g *Groups) scan(s scanner) (Group, error) {
	grp := Group{Kind: g.kind}
	var created int64
	if err := s.Scan(&grp.ID, &grp.Name, &grp.Color, &grp.Reserved, &created, &grp.DayCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Group{}, err
		}
		return Group{}, fmt.Errorf("scan %s: %w", g.kind.table(), err)
	}
	grp.CreatedAt = time.Unix(created, 0).UTC()
	return grp, nil
}

// DefaultID returns the owner's reserved default group.
func (g *Groups) DefaultID(ctx context.Context, ownerID int64) (int64, error) {
	return g.defaultID(ctx, g.db, ownerID)
}

func (g *Groups) defaultID(ctx context.Context, q querier, ownerID int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM `+g.kind.table()+
		` WHERE owner_id = ? AND is_reserved = 1`, ownerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("owner %d has no reserved default %s", ownerID, g.kind.table())
	}
	if err != nil {
		return 0, fmt.Errorf("find default %s: %w", g.kind.table(), err)
	}
	return id, nil
}

func normalizeInput(in GroupInput) (GroupInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Color = strings.ToLower(strings.TrimSpace(in.Color))
	if in.Color != "" && !colorPattern.MatchString(in.Color) {
		return in, ErrColorInvalid
	}
	return in, nil
}

// IsReservedName reports whether name is a (localized) name of the reserved default.
func IsReservedName(name string) bool {
	name = strings.TrimSpace(name)
	for _, r := range reservedNames {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// ValidateGroupName applies the collection/tag name rules to a trimmed name.
func ValidateGroupName(name string) error { return validateName(name) }

func validateName(name string) error {
	switch {
	case name == "":
		return ErrNameRequired
	case utf8.RuneCountInString(name) > MaxGroupNameRunes:
		return ErrNameTooLong
	}
	for _, r := range reservedNames {
		if strings.EqualFold(name, r) {
			return ErrNameReserved
		}
	}
	return nil
}

// ensureUniqueName uses Unicode case folding; SQLite NOCASE only folds ASCII.
func (g *Groups) ensureUniqueName(ctx context.Context, tx *sql.Tx, ownerID, exceptID int64, name string) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM `+g.kind.table()+` WHERE owner_id = ? AND id <> ?`, ownerID, exceptID)
	if err != nil {
		return fmt.Errorf("check %s name: %w", g.kind.table(), err)
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return fmt.Errorf("check %s name: %w", g.kind.table(), err)
		}
		if strings.EqualFold(existing, name) {
			return ErrNameTaken
		}
	}
	return rows.Err()
}

func (g *Groups) Create(ctx context.Context, ownerID int64, in GroupInput, now time.Time) (Group, error) {
	in, err := normalizeInput(in)
	if err != nil {
		return Group{}, err
	}
	if err := validateName(in.Name); err != nil {
		return Group{}, err
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, fmt.Errorf("create %s: %w", g.kind.table(), err)
	}
	defer tx.Rollback()
	if err := g.ensureUniqueName(ctx, tx, ownerID, 0, in.Name); err != nil {
		return Group{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO `+g.kind.table()+
		` (owner_id, name, color, created_at) VALUES (?, ?, ?, ?)`,
		ownerID, in.Name, nullString(in.Color), now.Unix())
	if err != nil {
		return Group{}, mapConstraint(fmt.Errorf("create %s: %w", g.kind.table(), err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Group{}, fmt.Errorf("create %s: %w", g.kind.table(), err)
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("create %s: %w", g.kind.table(), err)
	}
	return Group{ID: id, Kind: g.kind, Name: in.Name, Color: in.Color, CreatedAt: time.Unix(now.Unix(), 0).UTC()}, nil
}

// Update renames and recolors a group. The reserved default can only be recolored.
func (g *Groups) Update(ctx context.Context, ownerID, id int64, in GroupInput) (Group, error) {
	in, err := normalizeInput(in)
	if err != nil {
		return Group{}, err
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, fmt.Errorf("update %s: %w", g.kind.table(), err)
	}
	defer tx.Rollback()
	cur, err := g.get(ctx, tx, ownerID, id)
	if err != nil {
		return Group{}, err
	}
	if cur.Reserved {
		if in.Name != "" && in.Name != cur.Name {
			return Group{}, ErrReserved
		}
		in.Name = cur.Name
	} else {
		if err := validateName(in.Name); err != nil {
			return Group{}, err
		}
		if err := g.ensureUniqueName(ctx, tx, ownerID, id, in.Name); err != nil {
			return Group{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+g.kind.table()+` SET name = ?, color = ? WHERE owner_id = ? AND id = ?`,
		in.Name, nullString(in.Color), ownerID, id); err != nil {
		return Group{}, mapConstraint(fmt.Errorf("update %s: %w", g.kind.table(), err))
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("update %s: %w", g.kind.table(), err)
	}
	cur.Name, cur.Color = in.Name, in.Color
	return cur, nil
}

// Delete removes a non-reserved group in one transaction. Days of a deleted
// collection move to the default collection, and shares targeting it are
// removed. Days left without tags after a tag deletion get the default tag.
func (g *Groups) Delete(ctx context.Context, ownerID, id int64) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete %s: %w", g.kind.table(), err)
	}
	defer tx.Rollback()
	cur, err := g.get(ctx, tx, ownerID, id)
	if err != nil {
		return err
	}
	if cur.Reserved {
		return ErrReserved
	}
	defaultID, err := g.defaultID(ctx, tx, ownerID)
	if err != nil {
		return err
	}
	if g.kind == KindCollection {
		if err := deleteCollection(ctx, tx, ownerID, id, defaultID); err != nil {
			return err
		}
	} else if err := deleteTag(ctx, tx, ownerID, id, defaultID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete %s: %w", g.kind.table(), err)
	}
	return nil
}

func deleteCollection(ctx context.Context, tx *sql.Tx, ownerID, id, defaultID int64) error {
	steps := []struct {
		query string
		args  []any
	}{
		{`UPDATE days SET collection_id = ? WHERE owner_id = ? AND collection_id = ?`, []any{defaultID, ownerID, id}},
		{`DELETE FROM shares WHERE owner_id = ? AND target_type = 'collection' AND target_id = ?`, []any{ownerID, id}},
		{`DELETE FROM collections WHERE owner_id = ? AND id = ?`, []any{ownerID, id}},
	}
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return fmt.Errorf("delete collection: %w", err)
		}
	}
	return nil
}

func deleteTag(ctx context.Context, tx *sql.Tx, ownerID, id, defaultID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE owner_id = ? AND id = ?`, ownerID, id); err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO day_tags (day_id, tag_id)
		SELECT d.id, ? FROM days d WHERE d.owner_id = ?
		AND NOT EXISTS (SELECT 1 FROM day_tags dt WHERE dt.day_id = d.id)`, defaultID, ownerID); err != nil {
		return fmt.Errorf("retag orphaned days: %w", err)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mapConstraint turns a racing UNIQUE(owner_id, name) violation into ErrNameTaken.
func mapConstraint(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrNameTaken
	}
	return err
}
