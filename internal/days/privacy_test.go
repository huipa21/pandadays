package days

import (
	"context"
	"errors"
	"testing"

	"pandadays/internal/db/dbtest"
)

func TestPrivateDaysNamespace(t *testing.T) {
	ctx := context.Background()
	private := WithPrivate(ctx, true)
	db := dbtest.Open(t)
	owner := newOwner(t, db, "alice")
	other := newOwner(t, db, "bob")
	s := NewDays(db)
	publicDay, err := s.Create(ctx, owner, validInput(), groupNow)
	if err != nil {
		t.Fatal(err)
	}
	in := validInput()
	in.Private = true
	secret, err := s.Create(ctx, owner, in, groupNow)
	if err != nil || !secret.Private {
		t.Fatalf("create private: %+v %v", secret, err)
	}
	for _, tc := range []struct {
		ctx   context.Context
		owner int64
		id    int64
	}{
		{ctx, owner, secret.ID}, {private, owner, publicDay.ID}, {private, other, secret.ID},
	} {
		if _, err := s.Get(tc.ctx, tc.owner, tc.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get inaccessible day: %v", err)
		}
		if _, err := s.Update(tc.ctx, tc.owner, tc.id, in, groupNow); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update inaccessible day: %v", err)
		}
		if err := s.SetArchived(tc.ctx, tc.owner, tc.id, true, groupNow); !errors.Is(err, ErrNotFound) {
			t.Fatalf("archive inaccessible day: %v", err)
		}
		if err := s.Delete(tc.ctx, tc.owner, tc.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete inaccessible day: %v", err)
		}
	}
	for _, scope := range []context.Context{ctx, private} {
		want := publicDay.ID
		if IsPrivate(scope) {
			want = secret.ID
		}
		included := WithPrivateIncluded(ctx)
		for _, load := range []func() ([]Day, error){
			func() ([]Day, error) { return s.ListActive(included, owner) },
			func() ([]Day, error) { return s.ListCollection(included, owner, publicDay.CollectionID, false) },
			func() ([]Day, error) { return s.ListTag(included, owner, publicDay.TagIDs[0], false) },
		} {
			if list, err := load(); err != nil || len(list) != 2 {
				t.Fatalf("mixed list: %+v %v", list, err)
			}
		}
		if list, err := s.ListActive(included, other); err != nil || len(list) != 0 {
			t.Fatalf("mixed list crossed owner boundary: %+v %v", list, err)
		}
		for _, store := range []*Groups{s.collections, s.tags} {
			if groups, err := store.List(included, owner); err != nil || len(groups) != 1 || groups[0].DayCount != 2 {
				t.Fatalf("mixed group counts: %+v %v", groups, err)
			}
		}
		for _, load := range []func() ([]Day, error){
			func() ([]Day, error) { return s.ListActive(scope, owner) },
			func() ([]Day, error) { return s.ListCollection(scope, owner, publicDay.CollectionID, false) },
			func() ([]Day, error) { return s.ListTag(scope, owner, publicDay.TagIDs[0], false) },
		} {
			list, err := load()
			if err != nil || len(list) != 1 || list[0].ID != want {
				t.Fatalf("scoped list: %+v %v", list, err)
			}
		}
		for _, store := range []*Groups{s.collections, s.tags} {
			groups, err := store.List(scope, owner)
			if err != nil || len(groups) != 1 || groups[0].DayCount != 1 {
				t.Fatalf("scoped counts: %+v %v", groups, err)
			}
		}
	}
	if err := s.SetArchived(private, owner, secret.ID, true, groupNow); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListArchived(ctx, owner); err != nil || len(list) != 0 {
		t.Fatalf("private archive leaked: %+v %v", list, err)
	}
	if list, err := s.ListArchived(private, owner); err != nil || len(list) != 1 {
		t.Fatalf("vault archive missing: %+v %v", list, err)
	}
	if _, err := db.Exec(`UPDATE users SET quota_days = 2 WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, owner, validInput(), groupNow); !errors.Is(err, ErrQuotaDays) {
		t.Fatalf("private archived day did not count toward quota: %v", err)
	}
	in.Private = false
	if d, err := s.Update(private, owner, secret.ID, in, groupNow); err != nil || d.Private {
		t.Fatalf("make public: %+v %v", d, err)
	}
	if _, err := s.Get(ctx, owner, secret.ID); err != nil {
		t.Fatal(err)
	}
}
