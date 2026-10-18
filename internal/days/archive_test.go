package days

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"pandadays/internal/db/dbtest"
)

func TestSetArchivedPreservesContentAndOwnership(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	owner := newOwner(t, db, "alice")
	other := newOwner(t, db, "bob")
	s := NewDays(db)
	original, err := s.Create(ctx, owner, DayInput{Title: "Birthday", Date: "2000-05-01",
		Time: "09:30", Repeat: RepeatYearly, Comment: "Cake", StyleColor: "#123456", StyleIcon: "*"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := groupNow.Add(time.Hour)
	for _, tc := range []struct {
		archived bool
		when     time.Time
	}{
		{true, archivedAt},
		{true, archivedAt.Add(time.Hour)},
		{false, archivedAt.Add(2 * time.Hour)},
		{false, archivedAt.Add(3 * time.Hour)},
		{true, archivedAt.Add(4 * time.Hour)},
	} {
		if err := s.SetArchived(ctx, owner, original.ID, tc.archived, tc.when); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, owner, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := original
		want.UpdatedAt = tc.when.Truncate(time.Second)
		if tc.archived {
			when := archivedAt
			if tc.when.Equal(archivedAt.Add(4 * time.Hour)) {
				when = tc.when
			}
			when = when.Truncate(time.Second)
			want.ArchivedAt = &when
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("archived=%v:\n got %+v\nwant %+v", tc.archived, got, want)
		}
		for _, denied := range []struct{ owner, id int64 }{{other, original.ID}, {owner, original.ID + 1000}} {
			if err := s.SetArchived(ctx, denied.owner, denied.id, tc.archived, tc.when); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unauthorized/missing day: %v", err)
			}
		}
		unchanged, err := s.Get(ctx, owner, original.ID)
		if err != nil || !reflect.DeepEqual(unchanged, got) {
			t.Fatalf("unauthorized mutation: %+v, %v", unchanged, err)
		}
	}
}

func TestArchiveAndGroupLists(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	owner := newOwner(t, db, "alice")
	other := newOwner(t, db, "bob")
	s := NewDays(db)
	col, err := s.collections.Create(ctx, owner, GroupInput{Name: "Trips"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.tags.Create(ctx, owner, GroupInput{Name: "Fun"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	defTag, err := s.tags.DefaultID(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	create := func(owner int64, title string, grouped, archived bool) Day {
		t.Helper()
		in := DayInput{Title: title, Date: "2026-05-01"}
		if grouped {
			in.CollectionID, in.TagIDs = col.ID, []int64{defTag, tag.ID}
		}
		d, err := s.Create(ctx, owner, in, groupNow)
		if err != nil {
			t.Fatal(err)
		}
		if archived {
			if err := s.SetArchived(ctx, owner, d.ID, true, groupNow); err != nil {
				t.Fatal(err)
			}
			d, err = s.Get(ctx, owner, d.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	active := create(owner, "Active", true, false)
	archived := create(owner, "Archived", true, true)
	ungrouped := create(owner, "Elsewhere", false, false)
	archivedElsewhere := create(owner, "Archived elsewhere", false, true)
	create(other, "Other active", false, false)
	create(other, "Other archived", false, true)
	for _, tc := range []struct {
		name string
		list func() ([]Day, error)
		want []Day
	}{
		{"active", func() ([]Day, error) { return s.ListActive(ctx, owner) }, []Day{active, ungrouped}},
		{"archive", func() ([]Day, error) { return s.ListArchived(ctx, owner) }, []Day{archived, archivedElsewhere}},
		{"collection", func() ([]Day, error) { return s.ListCollection(ctx, owner, col.ID, false) }, []Day{active, archived}},
		{"collection hidden", func() ([]Day, error) { return s.ListCollection(ctx, owner, col.ID, true) }, []Day{active}},
		{"tag", func() ([]Day, error) { return s.ListTag(ctx, owner, tag.ID, false) }, []Day{active, archived}},
		{"tag hidden", func() ([]Day, error) { return s.ListTag(ctx, owner, tag.ID, true) }, []Day{active}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.list()
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	for _, hide := range []bool{false, true} {
		for _, list := range []func() ([]Day, error){
			func() ([]Day, error) { return s.ListCollection(ctx, other, col.ID, hide) },
			func() ([]Day, error) { return s.ListTag(ctx, other, tag.ID, hide) },
			func() ([]Day, error) { return s.ListCollection(ctx, owner, col.ID+1000, hide) },
			func() ([]Day, error) { return s.ListTag(ctx, owner, tag.ID+1000, hide) },
		} {
			if _, err := list(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign/missing group: %v", err)
			}
		}
	}
}
