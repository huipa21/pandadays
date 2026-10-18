package days

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"pandadays/internal/db/dbtest"
)

var groupNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newOwner inserts a user with reserved defaults, mirroring auth.CreateUser.
func newOwner(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (username, email, password_hash) VALUES (?, ?, 'x')`, name, name+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, q := range []string{
		`INSERT INTO collections (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`,
		`INSERT INTO tags (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`,
	} {
		if _, err := db.Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func newDay(t *testing.T, db *sql.DB, owner, collection int64, tags ...int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO days (owner_id, collection_id, title, date) VALUES (?, ?, 'd', '2026-01-01')`, owner, collection)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, tag := range tags {
		if _, err := db.Exec(`INSERT INTO day_tags (day_id, tag_id) VALUES (?, ?)`, id, tag); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func dayTags(t *testing.T, db *sql.DB, day int64) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT tag_id FROM day_tags WHERE day_id = ? ORDER BY tag_id`, day)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func TestGroupsCreateValidation(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	for _, store := range []*Groups{NewCollections(db), NewTags(db)} {
		g, err := store.Create(ctx, alice, GroupInput{Name: "  Work  ", Color: "#AABBCC"}, groupNow)
		if err != nil {
			t.Fatal(err)
		}
		if g.Name != "Work" || g.Color != "#aabbcc" || g.Reserved {
			t.Fatalf("created %+v", g)
		}
		long := make([]rune, MaxGroupNameRunes+1)
		for i := range long {
			long[i] = '日'
		}
		cases := []struct {
			in   GroupInput
			want error
		}{
			{GroupInput{Name: "   "}, ErrNameRequired},
			{GroupInput{Name: string(long)}, ErrNameTooLong},
			{GroupInput{Name: "default"}, ErrNameReserved},
			{GroupInput{Name: "DEFAULT"}, ErrNameReserved},
			{GroupInput{Name: "默认"}, ErrNameReserved},
			{GroupInput{Name: "WORK"}, ErrNameTaken},
			{GroupInput{Name: "Other", Color: "red"}, ErrColorInvalid},
			{GroupInput{Name: "Other", Color: "#abc"}, ErrColorInvalid},
		}
		for _, c := range cases {
			if _, err := store.Create(ctx, alice, c.in, groupNow); !errors.Is(err, c.want) {
				t.Errorf("%v create %q: err=%v want %v", store.Kind(), c.in.Name, err, c.want)
			}
		}
		// Unicode case folding beyond SQLite's ASCII-only NOCASE.
		if _, err := store.Create(ctx, alice, GroupInput{Name: "Ärger"}, groupNow); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(ctx, alice, GroupInput{Name: "äRGER"}, groupNow); !errors.Is(err, ErrNameTaken) {
			t.Errorf("unicode duplicate: %v", err)
		}
		list, err := store.List(ctx, alice)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 3 || !list[0].Reserved || list[1].Name != "Work" || list[2].Name != "Ärger" {
			t.Fatalf("list = %+v", list)
		}
	}
}

func TestGroupsNamesUniquePerUser(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice, bob := newOwner(t, db, "alice"), newOwner(t, db, "bob")
	store := NewTags(db)
	if _, err := store.Create(ctx, alice, GroupInput{Name: "Family"}, groupNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, bob, GroupInput{Name: "family"}, groupNow); err != nil {
		t.Fatalf("other user should be able to reuse the name: %v", err)
	}
}

func TestGroupsReservedProtection(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	for _, store := range []*Groups{NewCollections(db), NewTags(db)} {
		def, err := store.DefaultID(ctx, alice)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(ctx, alice, def, GroupInput{Name: "Renamed"}); !errors.Is(err, ErrReserved) {
			t.Errorf("%v rename reserved: %v", store.Kind(), err)
		}
		if err := store.Delete(ctx, alice, def); !errors.Is(err, ErrReserved) {
			t.Errorf("%v delete reserved: %v", store.Kind(), err)
		}
		// Recolor is allowed; omitting or repeating the name keeps it.
		g, err := store.Update(ctx, alice, def, GroupInput{Color: "#112233"})
		if err != nil || g.Name != "Default" || g.Color != "#112233" {
			t.Fatalf("%v recolor reserved: %+v %v", store.Kind(), g, err)
		}
		if g, err = store.Update(ctx, alice, def, GroupInput{Name: "Default"}); err != nil || g.Color != "" {
			t.Fatalf("%v clear reserved color: %+v %v", store.Kind(), g, err)
		}
		got, err := store.Get(ctx, alice, def)
		if err != nil || got.Name != "Default" || !got.Reserved {
			t.Fatalf("%v reserved after updates: %+v %v", store.Kind(), got, err)
		}
	}
}

func TestGroupsRenameAndRecolor(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	store := NewCollections(db)
	work, _ := store.Create(ctx, alice, GroupInput{Name: "Work"}, groupNow)
	if _, err := store.Create(ctx, alice, GroupInput{Name: "Home"}, groupNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, alice, work.ID, GroupInput{Name: "home"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename to existing: %v", err)
	}
	if _, err := store.Update(ctx, alice, work.ID, GroupInput{Name: "default"}); !errors.Is(err, ErrNameReserved) {
		t.Errorf("rename to reserved: %v", err)
	}
	if _, err := store.Update(ctx, alice, work.ID, GroupInput{Name: ""}); !errors.Is(err, ErrNameRequired) {
		t.Errorf("rename to empty: %v", err)
	}
	g, err := store.Update(ctx, alice, work.ID, GroupInput{Name: "WORK", Color: "#00ff00"})
	if err != nil || g.Name != "WORK" || g.Color != "#00ff00" {
		t.Fatalf("case-only rename: %+v %v", g, err)
	}
	got, _ := store.Get(ctx, alice, work.ID)
	if got.Name != "WORK" || got.Color != "#00ff00" {
		t.Fatalf("persisted %+v", got)
	}
}

func TestGroupsCrossUserNotFound(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice, bob := newOwner(t, db, "alice"), newOwner(t, db, "bob")
	for _, store := range []*Groups{NewCollections(db), NewTags(db)} {
		g, err := store.Create(ctx, alice, GroupInput{Name: "Secret"}, groupNow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, bob, g.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v get: %v", store.Kind(), err)
		}
		if _, err := store.Update(ctx, bob, g.ID, GroupInput{Name: "Mine"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v update: %v", store.Kind(), err)
		}
		if err := store.Delete(ctx, bob, g.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v delete: %v", store.Kind(), err)
		}
		aliceDefault, _ := store.DefaultID(ctx, alice)
		if _, err := store.Get(ctx, bob, aliceDefault); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v get other default: %v", store.Kind(), err)
		}
		if got, err := store.Get(ctx, alice, g.ID); err != nil || got.Name != "Secret" {
			t.Fatalf("%v owner's group changed: %+v %v", store.Kind(), got, err)
		}
		bobList, _ := store.List(ctx, bob)
		if len(bobList) != 1 || !bobList[0].Reserved {
			t.Fatalf("%v bob sees %+v", store.Kind(), bobList)
		}
	}
}

func TestDeleteCollectionMovesDaysToDefault(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice, bob := newOwner(t, db, "alice"), newOwner(t, db, "bob")
	store := NewCollections(db)
	def, _ := store.DefaultID(ctx, alice)
	trip, _ := store.Create(ctx, alice, GroupInput{Name: "Trip"}, groupNow)
	other, _ := store.Create(ctx, alice, GroupInput{Name: "Other"}, groupNow)
	d1, d2 := newDay(t, db, alice, trip.ID), newDay(t, db, alice, trip.ID)
	d3 := newDay(t, db, alice, other.ID)
	if _, err := db.Exec(`INSERT INTO days (owner_id, collection_id, title, date, archived_at) VALUES (?, ?, 'archived', '2026-01-01', 1)`, alice, trip.ID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'collection', ?)`, alice, trip.ID)
	if err != nil {
		t.Fatal(err)
	}
	shareID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO share_recipients (share_id, recipient_id) VALUES (?, ?)`, shareID, bob); err != nil {
		t.Fatal(err)
	}
	if g, _ := store.Get(ctx, alice, trip.ID); g.DayCount != 3 {
		t.Fatalf("day count = %d", g.DayCount)
	}

	if err := store.Delete(ctx, alice, trip.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, alice, trip.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection still exists: %v", err)
	}
	var moved, inOther, shares int
	db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ? AND collection_id = ?`, alice, def).Scan(&moved)
	db.QueryRow(`SELECT collection_id FROM days WHERE id = ?`, d3).Scan(&inOther)
	db.QueryRow(`SELECT COUNT(*) FROM shares WHERE id = ?`, shareID).Scan(&shares)
	if moved != 3 || int64(inOther) != other.ID || shares != 0 {
		t.Fatalf("moved=%d other=%d shares=%d", moved, inOther, shares)
	}
	for _, d := range []int64{d1, d2} {
		var c int64
		db.QueryRow(`SELECT collection_id FROM days WHERE id = ?`, d).Scan(&c)
		if c != def {
			t.Fatalf("day %d in collection %d, want default %d", d, c, def)
		}
	}
}

func TestDeleteTagRetagsOrphans(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	tags, cols := NewTags(db), NewCollections(db)
	def, _ := tags.DefaultID(ctx, alice)
	col, _ := cols.DefaultID(ctx, alice)
	fun, _ := tags.Create(ctx, alice, GroupInput{Name: "Fun"}, groupNow)
	work, _ := tags.Create(ctx, alice, GroupInput{Name: "Work"}, groupNow)
	onlyFun := newDay(t, db, alice, col, fun.ID)
	funAndWork := newDay(t, db, alice, col, fun.ID, work.ID)
	onlyWork := newDay(t, db, alice, col, work.ID)

	if err := tags.Delete(ctx, alice, fun.ID); err != nil {
		t.Fatal(err)
	}
	if got := dayTags(t, db, onlyFun); len(got) != 1 || got[0] != def {
		t.Errorf("orphaned day tags = %v, want [%d]", got, def)
	}
	if got := dayTags(t, db, funAndWork); len(got) != 1 || got[0] != work.ID {
		t.Errorf("multi-tag day tags = %v, want [%d]", got, work.ID)
	}
	if got := dayTags(t, db, onlyWork); len(got) != 1 || got[0] != work.ID {
		t.Errorf("untouched day tags = %v", got)
	}
	if g, _ := tags.Get(ctx, alice, def); g.DayCount != 1 {
		t.Errorf("default tag day count = %d", g.DayCount)
	}
}
