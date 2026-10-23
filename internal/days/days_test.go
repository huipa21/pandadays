package days

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"pandadays/internal/db/dbtest"
)

func validInput() DayInput {
	return DayInput{Title: "Trip", Date: "2026-05-01"}
}

func TestDaysCreateAppliesDefaults(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	s := NewDays(db)
	defCol, _ := s.collections.DefaultID(ctx, alice)
	defTag, _ := s.tags.DefaultID(ctx, alice)

	d, err := s.Create(ctx, alice, DayInput{Title: "  Trip  ", Date: "2026-05-01", Comment: " \r\nnote\r\nmore  "}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	want := Day{ID: d.ID, OwnerID: alice, CollectionID: defCol, TagIDs: []int64{defTag}, Title: "Trip",
		Date: "2026-05-01", Repeat: RepeatNone, Calendar: CalendarGregorian, Comment: "note\nmore", CreatedAt: groupNow.Truncate(1e9), UpdatedAt: groupNow.Truncate(1e9)}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("created day:\n got %+v\nwant %+v", d, want)
	}
	got, err := s.Get(ctx, alice, d.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("get: %+v %v", got, err)
	}
	var timeNull, commentNull, colorNull, iconNull bool
	if err := db.QueryRow(`SELECT time IS NULL, comment IS NULL, style_color IS NULL, style_icon IS NULL FROM days WHERE id = ?`,
		d.ID).Scan(&timeNull, &commentNull, &colorNull, &iconNull); err != nil {
		t.Fatal(err)
	}
	if !timeNull || commentNull || !colorNull || !iconNull {
		t.Fatalf("NULL storage: time=%v comment=%v color=%v icon=%v", timeNull, commentNull, colorNull, iconNull)
	}
}

func TestDaysCreateWithExplicitGroupsAndStyle(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	s := NewDays(db)
	col, _ := s.collections.Create(ctx, alice, GroupInput{Name: "Trips"}, groupNow)
	t1, _ := s.tags.Create(ctx, alice, GroupInput{Name: "fun"}, groupNow)
	t2, _ := s.tags.Create(ctx, alice, GroupInput{Name: "family"}, groupNow)

	d, err := s.Create(ctx, alice, DayInput{Title: "Trip", Date: "2024-02-29", Time: "08:30", Repeat: RepeatYearly,
		CollectionID: col.ID, TagIDs: []int64{t2.ID, t1.ID, t2.ID}, StyleColor: "#AABBCC", StyleIcon: "🎂"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	if d.CollectionID != col.ID || !reflect.DeepEqual(d.TagIDs, []int64{t1.ID, t2.ID}) || d.Time != "08:30" ||
		d.Repeat != RepeatYearly || d.StyleColor != "#aabbcc" || d.StyleIcon != "🎂" {
		t.Fatalf("created: %+v", d)
	}
}

func TestDaysValidation(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	s := NewDays(db)
	for _, tc := range []struct {
		name string
		mod  func(*DayInput)
		want error
	}{
		{"empty title", func(in *DayInput) { in.Title = "   " }, ErrTitleRequired},
		{"long title", func(in *DayInput) { in.Title = strings.Repeat("字", 201) }, ErrTitleTooLong},
		{"missing date", func(in *DayInput) { in.Date = "" }, ErrDateInvalid},
		{"bad date", func(in *DayInput) { in.Date = "2026-02-30" }, ErrDateInvalid},
		{"short date", func(in *DayInput) { in.Date = "2026-5-1" }, ErrDateInvalid},
		{"year zero", func(in *DayInput) { in.Date = "0000-01-01" }, ErrDateInvalid},
		{"bad time", func(in *DayInput) { in.Time = "24:00" }, ErrTimeInvalid},
		{"seconds", func(in *DayInput) { in.Time = "10:00:00" }, ErrTimeInvalid},
		{"repeat", func(in *DayInput) { in.Repeat = "daily" }, ErrRepeatInvalid},
		{"calendar", func(in *DayInput) { in.Calendar = "unknown" }, ErrCalendarInvalid},
		{"lunar range", func(in *DayInput) { in.Calendar = CalendarLunar; in.Date = "1800-01-01" }, ErrCalendarInvalid},
		{"comment", func(in *DayInput) { in.Comment = strings.Repeat("a", MaxCommentRunes+1) }, ErrCommentTooLong},
		{"color", func(in *DayInput) { in.StyleColor = "red" }, ErrColorInvalid},
		{"icon space", func(in *DayInput) { in.StyleIcon = "a b" }, ErrIconInvalid},
		{"icon long", func(in *DayInput) { in.StyleIcon = strings.Repeat("x", MaxIconRunes+1) }, ErrIconInvalid},
		{"collection", func(in *DayInput) { in.CollectionID = 9999 }, ErrCollectionInvalid},
		{"tag", func(in *DayInput) { in.TagIDs = []int64{9999} }, ErrTagInvalid},
	} {
		in := validInput()
		tc.mod(&in)
		if _, err := s.Create(ctx, alice, in, groupNow); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	in := validInput()
	in.Title = strings.Repeat("字", 200)
	in.Comment = strings.Repeat("字", MaxCommentRunes)
	in.StyleIcon = "👨‍👩‍👧‍👦"
	if _, err := s.Create(ctx, alice, in, groupNow); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM days`).Scan(&n)
	if n != 1 {
		t.Fatalf("invalid creates stored rows: %d", n)
	}
}

func TestDaysQuota(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	bob := newOwner(t, db, "bob")
	s := NewDays(db)

	if q, err := s.QuotaDays(ctx, alice); err != nil || q != 1000 {
		t.Fatalf("site default quota: %d %v", q, err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = '2' WHERE key = 'default_quota_days'`); err != nil {
		t.Fatal(err)
	}
	first, err := s.Create(ctx, alice, validInput(), groupNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, alice, validInput(), groupNow); err != nil {
		t.Fatal(err)
	}
	// Archived days still count toward the quota.
	if _, err := db.Exec(`UPDATE days SET archived_at = 1 WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, alice, validInput(), groupNow); !errors.Is(err, ErrQuotaDays) {
		t.Fatalf("over quota: %v", err)
	}
	if _, err := s.Create(ctx, bob, validInput(), groupNow); err != nil {
		t.Fatalf("quota is per user: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET quota_days = 3 WHERE id = ?`, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, alice, validInput(), groupNow); err != nil {
		t.Fatalf("user override: %v", err)
	}
	if _, err := s.Create(ctx, alice, validInput(), groupNow); !errors.Is(err, ErrQuotaDays) {
		t.Fatalf("over user override: %v", err)
	}
	if _, err := s.Update(ctx, alice, first.ID, validInput(), groupNow); err != nil {
		t.Fatalf("update at quota: %v", err)
	}
	if err := s.Delete(ctx, alice, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, alice, validInput(), groupNow); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func TestDaysUpdate(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	s := NewDays(db)
	col, _ := s.collections.Create(ctx, alice, GroupInput{Name: "Trips"}, groupNow)
	tag, _ := s.tags.Create(ctx, alice, GroupInput{Name: "fun"}, groupNow)
	defTag, _ := s.tags.DefaultID(ctx, alice)
	d, err := s.Create(ctx, alice, DayInput{Title: "Trip", Date: "2026-05-01", Time: "10:00", TagIDs: []int64{tag.ID},
		Comment: "c", StyleColor: "#112233", StyleIcon: "✈️"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	later := groupNow.Add(3600e9)
	u, err := s.Update(ctx, alice, d.ID, DayInput{Title: "Holiday", Date: "2026-06-01", Repeat: RepeatMonthly, CollectionID: col.ID}, later)
	if err != nil {
		t.Fatal(err)
	}
	if u.Title != "Holiday" || u.Date != "2026-06-01" || u.Time != "" || u.Repeat != RepeatMonthly || u.CollectionID != col.ID ||
		!reflect.DeepEqual(u.TagIDs, []int64{defTag}) || u.Comment != "" || u.StyleColor != "" || u.StyleIcon != "" ||
		!u.CreatedAt.Equal(d.CreatedAt) || u.UpdatedAt.Unix() != later.Unix() {
		t.Fatalf("updated: %+v", u)
	}
	if _, err := s.Update(ctx, alice, d.ID, DayInput{Title: "", Date: "2026-06-01"}, later); !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("invalid update: %v", err)
	}
	if got, _ := s.Get(ctx, alice, d.ID); got.Title != "Holiday" {
		t.Fatalf("invalid update changed row: %+v", got)
	}
}

func TestDaysDeleteRemovesSharesAndTags(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	s := NewDays(db)
	d, _ := s.Create(ctx, alice, validInput(), groupNow)
	keep, _ := s.Create(ctx, alice, validInput(), groupNow)
	for _, id := range []int64{d.ID, keep.ID} {
		if _, err := db.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'day', ?)`, alice, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, alice, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted day: %v", err)
	}
	var shares, tags int
	db.QueryRow(`SELECT COUNT(*) FROM shares`).Scan(&shares)
	db.QueryRow(`SELECT COUNT(*) FROM day_tags WHERE day_id = ?`, d.ID).Scan(&tags)
	if shares != 1 || tags != 0 {
		t.Fatalf("after delete: shares=%d tags=%d", shares, tags)
	}
	if err := s.Delete(ctx, alice, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDaysOwnership(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	bob := newOwner(t, db, "bob")
	s := NewDays(db)
	d, _ := s.Create(ctx, alice, validInput(), groupNow)

	if _, err := s.Get(ctx, bob, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob get: %v", err)
	}
	if _, err := s.Update(ctx, bob, d.ID, validInput(), groupNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob update: %v", err)
	}
	if err := s.Delete(ctx, bob, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob delete: %v", err)
	}
	aliceCol, _ := s.collections.Create(ctx, alice, GroupInput{Name: "Private"}, groupNow)
	aliceTag, _ := s.tags.DefaultID(ctx, alice)
	in := validInput()
	in.CollectionID = aliceCol.ID
	if _, err := s.Create(ctx, bob, in, groupNow); !errors.Is(err, ErrCollectionInvalid) {
		t.Errorf("bob uses alice's collection: %v", err)
	}
	in = validInput()
	in.TagIDs = []int64{aliceTag}
	if _, err := s.Create(ctx, bob, in, groupNow); !errors.Is(err, ErrTagInvalid) {
		t.Errorf("bob uses alice's tag: %v", err)
	}
	bobDay, _ := s.Create(ctx, bob, validInput(), groupNow)
	in = validInput()
	in.CollectionID = aliceCol.ID
	if _, err := s.Update(ctx, bob, bobDay.ID, in, groupNow); !errors.Is(err, ErrCollectionInvalid) {
		t.Errorf("bob moves day into alice's collection: %v", err)
	}
	if got, err := s.Get(ctx, alice, d.ID); err != nil || got.Title != "Trip" {
		t.Fatalf("alice's day changed: %+v %v", got, err)
	}
}
