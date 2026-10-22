package share

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrivateDayNeverShared(t *testing.T) {
	s, owner, bob, _, collection, day, _ := shareFixture(t)
	ctx := context.Background()
	for _, target := range []struct {
		kind TargetType
		id   int64
	}{{TargetDay, day}, {TargetCollection, collection}} {
		sh, err := s.Create(ctx, owner, target.kind, target.id, []string{"Bob"}, true, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Respond(ctx, bob, sh.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE days SET is_private = 1 WHERE owner_id = ? AND id = ?`, owner, day); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := s.CanView(ctx, bob, day); err != nil || allowed {
		t.Fatalf("private access: %v %v", allowed, err)
	}
	if _, err := s.GetShared(ctx, bob, day); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get private: %v", err)
	}
	list, err := s.ListForUser(ctx, bob, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if d.ID == day {
			t.Fatal("collection share leaked private day")
		}
	}
	if _, err := s.Create(ctx, owner, TargetDay, day, []string{"Bob"}, true, time.Now()); !errors.Is(err, ErrTargetInvalid) {
		t.Fatalf("shared a private day: %v", err)
	}
	shares, err := s.List(ctx, owner, TargetDay, day)
	if err != nil || len(shares) != 0 {
		t.Fatalf("direct shares not revoked: %+v %v", shares, err)
	}
	if _, err := s.db.Exec(`UPDATE days SET is_private = 0 WHERE owner_id = ? AND id = ?`, owner, day); err != nil {
		t.Fatal(err)
	}
	shares, err = s.List(ctx, owner, TargetDay, day)
	if err != nil || len(shares) != 0 {
		t.Fatal("making public restored old direct shares")
	}
}
