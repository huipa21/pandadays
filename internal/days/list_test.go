package days

import (
	"context"
	"reflect"
	"testing"
	"time"

	"pandadays/internal/db/dbtest"
)

func entryTitles(list []Entry) []string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Day.Title
	}
	return out
}

func TestParseWhen(t *testing.T) {
	for in, want := range map[string]When{"": WhenFuture, "future": WhenFuture, "past": WhenPast, "PAST": WhenFuture, "x": WhenFuture} {
		if got := ParseWhen(in); got != want {
			t.Errorf("ParseWhen(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHomeListFiltersAndOrders(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, loc)
	list := []Day{
		{ID: 1, Title: "Far future", Date: "2027-01-01"},
		{ID: 2, Title: "Soon", Date: "2026-10-20"},
		{ID: 3, Title: "Today all-day", Date: "2026-10-18"},
		{ID: 4, Title: "Earlier today", Date: "2026-10-18", Time: "08:00"},
		{ID: 5, Title: "Later today", Date: "2026-10-18", Time: "18:00"},
		{ID: 6, Title: "Yesterday", Date: "2026-10-17"},
		{ID: 7, Title: "Long ago", Date: "2001-01-01"},
		// Original is past but repeating days are always future; next is 2026-11-01.
		{ID: 8, Title: "Anniversary", Date: "2020-11-01", Repeat: RepeatYearly},
		{ID: 9, Title: "B tie", Date: "2026-10-25"},
		{ID: 10, Title: "A tie", Date: "2026-10-25"},
		{ID: 11, Title: "Broken", Date: "not-a-date"},
	}
	future := HomeList(list, WhenFuture, now, loc)
	want := []string{"Today all-day", "Later today", "Soon", "A tie", "B tie", "Anniversary", "Far future"}
	if got := entryTitles(future); !reflect.DeepEqual(got, want) {
		t.Fatalf("future:\n got %v\nwant %v", got, want)
	}
	if got := future[5].Calc.Target.Format(time.DateOnly); got != "2026-11-01" {
		t.Errorf("anniversary target = %s", got)
	}
	past := HomeList(list, WhenPast, now, loc)
	want = []string{"Earlier today", "Yesterday", "Long ago"}
	if got := entryTitles(past); !reflect.DeepEqual(got, want) {
		t.Fatalf("past:\n got %v\nwant %v", got, want)
	}
	for _, e := range past {
		if e.Calc.Classification != Past {
			t.Errorf("%s classified %s", e.Day.Title, e.Calc.Classification)
		}
	}
	if len(HomeList(nil, WhenFuture, now, loc)) != 0 {
		t.Error("empty input should give empty list")
	}
}

func TestHomeListMovesPassedDayToPast(t *testing.T) {
	loc := time.UTC
	list := []Day{{ID: 1, Title: "Deadline", Date: "2026-11-16"}}
	before := time.Date(2026, 11, 15, 12, 0, 0, 0, loc)
	if got := HomeList(list, WhenFuture, before, loc); len(got) != 1 || got[0].Calc.ListLabel.Args[0] != 1 {
		t.Fatalf("before: %+v", got)
	}
	after := time.Date(2026, 11, 20, 12, 0, 0, 0, loc)
	if got := HomeList(list, WhenFuture, after, loc); len(got) != 0 {
		t.Fatalf("passed day still upcoming: %+v", got)
	}
	got := HomeList(list, WhenPast, after, loc)
	if len(got) != 1 || got[0].Calc.ListLabel.Key != "days.count.ago.other" || got[0].Calc.ListLabel.Args[0] != 4 {
		t.Fatalf("after: %+v", got)
	}
}

func TestHomeListFreezesPausedArchive(t *testing.T) {
	loc := time.UTC
	archivedAt := time.Date(2026, 11, 10, 9, 0, 0, 0, loc)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, loc)
	paused := Day{ID: 1, Title: "Paused", Date: "2026-11-16", ArchivedAt: &archivedAt}
	live := paused
	live.ID, live.Title, live.ArchiveLive = 2, "Live", true
	// Archived 6 days before the date: stays a 6-day countdown while paused.
	future := HomeList([]Day{paused, live}, WhenFuture, now, loc)
	if len(future) != 1 || future[0].Day.ID != 1 || future[0].Calc.ListLabel.Args[0] != 6 {
		t.Fatalf("paused: %+v", future)
	}
	past := HomeList([]Day{paused, live}, WhenPast, now, loc)
	if len(past) != 1 || past[0].Day.ID != 2 || past[0].Calc.ListLabel.Args[0] != 4 {
		t.Fatalf("live: %+v", past)
	}
}

func TestListActiveScopedAndExcludesArchived(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice := newOwner(t, db, "alice")
	bob := newOwner(t, db, "bob")
	s := NewDays(db)
	tag, err := s.tags.Create(ctx, alice, GroupInput{Name: "Fun"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	defTag, _ := s.tags.DefaultID(ctx, alice)
	a1, _ := s.Create(ctx, alice, DayInput{Title: "One", Date: "2026-05-01"}, groupNow)
	a2, _ := s.Create(ctx, alice, DayInput{Title: "Two", Date: "2026-06-01", TagIDs: []int64{tag.ID, defTag}}, groupNow)
	archived, _ := s.Create(ctx, alice, DayInput{Title: "Old", Date: "2026-07-01"}, groupNow)
	if _, err := s.Create(ctx, bob, DayInput{Title: "Bob's", Date: "2026-05-01"}, groupNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE days SET archived_at = ? WHERE id = ?`, groupNow.Unix(), archived.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListActive(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Day{a1, a2}) {
		t.Fatalf("ListActive:\n got %+v\nwant %+v", got, []Day{a1, a2})
	}
	empty, err := s.ListActive(ctx, newOwner(t, db, "carol"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty owner: %v %v", empty, err)
	}
}
