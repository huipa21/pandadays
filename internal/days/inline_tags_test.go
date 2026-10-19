package days

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"pandadays/internal/db/dbtest"
)

func TestInlineTagsResolveOwnedNamesAndPersist(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice, bob := newOwner(t, db, "alice"), newOwner(t, db, "bob")
	tags := NewTags(db)
	foreign, err := tags.Create(ctx, bob, GroupInput{Name: "工作"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := tags.Create(ctx, alice, GroupInput{Name: "Ärger"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	store := NewDays(db)
	in := DayInput{Title: "Trip", Date: "2026-01-02", TagNames: []string{" äRGER ", "工作", "工作", "Family trip"}}
	d, err := store.Create(ctx, alice, in, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := tags.List(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 4 || len(d.TagIDs) != 3 {
		t.Fatalf("groups=%+v day=%+v", groups, d)
	}
	for _, id := range d.TagIDs {
		if id == foreign.ID {
			t.Fatal("resolved another owner's tag")
		}
	}
	if got, err := tags.Get(ctx, alice, existing.ID); err != nil || got.DayCount != 1 {
		t.Fatalf("existing tag not reused: %+v %v", got, err)
	}
	in.TagNames = []string{"Updated"}
	updated, err := store.Update(ctx, alice, d.ID, in, groupNow)
	if err != nil || len(updated.TagIDs) != 1 {
		t.Fatalf("update=%+v %v", updated, err)
	}
	saved, err := store.Get(ctx, alice, d.ID)
	if err != nil || !reflect.DeepEqual(saved.TagIDs, updated.TagIDs) {
		t.Fatalf("saved tags=%v %v", saved.TagIDs, err)
	}
	in.TagNames = []string{"默认"}
	updated, err = store.Update(ctx, alice, d.ID, in, groupNow)
	def, _ := tags.DefaultID(ctx, alice)
	if err != nil || !reflect.DeepEqual(updated.TagIDs, []int64{def}) {
		t.Fatalf("localized default=%+v %v", updated, err)
	}
}

func TestInlineTagsSaveIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	alice, bob := newOwner(t, db, "alice"), newOwner(t, db, "bob")
	store, tags := NewDays(db), NewTags(db)
	foreign, _ := tags.DefaultID(ctx, bob)
	d, err := store.Create(ctx, alice, DayInput{Title: "Original", Date: "2026-01-02"}, groupNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		in   DayInput
		want error
	}{
		{"invalid day", DayInput{Date: "2026-01-02", TagNames: []string{"Not saved"}}, ErrTitleRequired},
		{"foreign tag", DayInput{Title: "Changed", Date: "2026-01-02", TagIDs: []int64{foreign}, TagNames: []string{"Not saved"}}, ErrTagInvalid},
		{"invalid name", DayInput{Title: "Changed", Date: "2026-01-02", TagNames: []string{"Not saved", strings.Repeat("日", 101)}}, ErrNameTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Create(ctx, alice, tc.in, groupNow); !errors.Is(err, tc.want) {
				t.Fatalf("create: %v", err)
			}
			if _, err := store.Update(ctx, alice, d.ID, tc.in, groupNow); !errors.Is(err, tc.want) {
				t.Fatalf("update: %v", err)
			}
			groups, err := tags.List(ctx, alice)
			if err != nil || len(groups) != 1 {
				t.Fatalf("orphan tags: %+v %v", groups, err)
			}
		})
	}
	if _, err := store.Update(ctx, bob, d.ID, DayInput{Title: "Stolen", Date: "2026-01-02", TagNames: []string{"Not saved"}}, groupNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner update: %v", err)
	}
	saved, err := store.Get(ctx, alice, d.ID)
	if err != nil || saved.Title != "Original" || !reflect.DeepEqual(saved.TagIDs, d.TagIDs) {
		t.Fatalf("failed update changed day: %+v %v", saved, err)
	}
	if _, err := db.Exec(`UPDATE users SET quota_days = 1 WHERE id = ?`, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, alice, DayInput{Title: "Over quota", Date: "2026-01-02", TagNames: []string{"Not saved"}}, groupNow); !errors.Is(err, ErrQuotaDays) {
		t.Fatalf("quota: %v", err)
	}
	groups, err := tags.List(ctx, alice)
	if err != nil || len(groups) != 1 {
		t.Fatalf("quota left tags: %+v %v", groups, err)
	}
}
