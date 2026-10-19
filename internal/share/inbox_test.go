package share

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInvitationDecisionsGateEveryAccessPath(t *testing.T) {
	s, owner, bob, carol, collectionID, dayID, _ := shareFixture(t)
	ctx := context.Background()
	direct, err := s.Create(ctx, owner, TargetDay, dayID, []string{"Bob"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check := func(allowed, comment bool, count int) {
		t.Helper()
		got, show, err := s.CanView(ctx, bob, dayID)
		if err != nil || got != allowed || show != comment {
			t.Fatalf("CanView = %v, %v, %v; want %v, %v", got, show, err, allowed, comment)
		}
		list, err := s.ListForUser(ctx, bob, true)
		if err != nil || (len(list) != 0) != allowed {
			t.Fatalf("list = %+v, %v", list, err)
		}
		_, err = s.GetShared(ctx, bob, dayID)
		if allowed && err != nil || !allowed && !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetShared = %v", err)
		}
		n, err := s.PendingCount(ctx, bob)
		if err != nil || n != count {
			t.Fatalf("pending = %d, %v; want %d", n, err, count)
		}
	}
	check(false, false, 1)
	if err := s.Respond(ctx, carol, direct.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-recipient responded: %v", err)
	}
	for _, accept := range []bool{false, true, true, false, true} {
		if err := s.Respond(ctx, bob, direct.ID, accept); err != nil {
			t.Fatal(err)
		}
		check(accept, accept, 0)
	}
	if err := s.Update(ctx, owner, direct.ID, []string{"Bob", "Carol"}, true); err != nil {
		t.Fatal(err)
	}
	check(true, true, 0)
	if n, err := s.PendingCount(ctx, carol); err != nil || n != 1 {
		t.Fatalf("new recipient pending = %d, %v", n, err)
	}
	collection, err := s.Create(ctx, owner, TargetCollection, collectionID, []string{"Bob"}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, bob, collection.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, bob, direct.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetShared(ctx, bob, dayID)
	if err != nil || got.ShowComment || got.Comment != "" {
		t.Fatalf("rejected direct share leaked comment via collection: %+v, %v", got, err)
	}
	if err := s.Revoke(ctx, owner, direct.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, bob, direct.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked invitation accepted: %v", err)
	}
}

func TestInboxCursorAndRecipientIsolation(t *testing.T) {
	s, owner, bob, carol, _, dayID, _ := shareFixture(t)
	ctx := context.Background()
	for i := 0; i < InboxPageSize+3; i++ {
		if _, err := s.Create(ctx, owner, TargetDay, dayID, []string{"Bob"}, false, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	first, more, err := s.Inbox(ctx, bob, 0)
	if err != nil || !more || len(first) != InboxPageSize {
		t.Fatalf("first page = %d, %v, %v", len(first), more, err)
	}
	if err := s.Respond(ctx, bob, first[0].ShareID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, owner, TargetDay, dayID, []string{"Bob"}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	next, more, err := s.Inbox(ctx, bob, first[len(first)-1].ShareID)
	if err != nil || more || len(next) != 3 {
		t.Fatalf("next page = %d, %v, %v", len(next), more, err)
	}
	all := append(first, next...)
	for i, item := range all {
		if item.Title != "One" || item.OwnerUsername != "Alice" || i > 0 && item.ShareID >= all[i-1].ShareID {
			t.Fatalf("incorrect queue ordering/metadata: %+v", item)
		}
	}
	items, more, err := s.Inbox(ctx, carol, 0)
	if err != nil || more || len(items) != 0 {
		t.Fatalf("other recipient inbox = %+v, %v, %v", items, more, err)
	}
	items, _, err = s.Inbox(ctx, bob, 0)
	if err != nil || items[1].Status != "rejected" {
		t.Fatalf("rejected invitation lost: %+v, %v", items, err)
	}
}
