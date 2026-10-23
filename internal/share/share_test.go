package share

import (
	"context"
	"errors"
	"testing"
	"time"

	"pandadays/internal/db/dbtest"
)

func shareFixture(t *testing.T) (*Store, int64, int64, int64, int64, int64, int64) {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	ids := make([]int64, 3)
	for i, username := range []string{"Alice", "Bob", "Carol"} {
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, email, password_hash) VALUES (?, ?, 'unused')`,
			username, username+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		ids[i], err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.ExecContext(ctx, `INSERT INTO collections (owner_id, name) VALUES (?, 'Trips')`, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	collectionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	insertDay := func(title string) int64 {
		t.Helper()
		res, err := db.ExecContext(ctx, `INSERT INTO days (owner_id, collection_id, title, date, comment)
			VALUES (?, ?, ?, '2027-01-01', 'private note')`, ids[0], collectionID, title)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return New(db), ids[0], ids[1], ids[2], collectionID, insertDay("One"), insertDay("Two")
}

func TestListOwnedTargets(t *testing.T) {
	store, owner, bob, _, collectionID, dayID, unsharedID := shareFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	var shares []Share
	for _, target := range []struct {
		kind TargetType
		id   int64
	}{{TargetDay, dayID}, {TargetDay, dayID}, {TargetCollection, collectionID}} {
		sh, err := store.Create(ctx, owner, target.kind, target.id, []string{"Bob", "Carol"}, false, now)
		if err != nil {
			t.Fatal(err)
		}
		shares = append(shares, sh)
	}
	// Outgoing shares include pending/rejected invitations and archived targets.
	if err := store.Respond(ctx, bob, shares[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE days SET archived_at = ? WHERE owner_id = ? AND id = ?`,
		now.Unix(), owner, dayID); err != nil {
		t.Fatal(err)
	}
	targets, err := store.ListOwnedTargets(ctx, owner)
	if err != nil || len(targets) != 2 {
		t.Fatalf("deduplicated targets: %+v, %v", targets, err)
	}
	if targets[0].TargetType != TargetDay || targets[0].TargetID != dayID || targets[0].Title != "One" ||
		targets[1].TargetType != TargetCollection || targets[1].TargetID != collectionID || targets[1].Title != "Trips" {
		t.Fatalf("target metadata: %+v", targets)
	}
	for _, id := range []int64{bob, 999999} {
		if targets, err := store.ListOwnedTargets(ctx, id); err != nil || len(targets) != 0 {
			t.Fatalf("owner isolation for %d: %+v, %v", id, targets, err)
		}
	}
	for _, sh := range shares[:2] {
		if err := store.Revoke(ctx, owner, sh.ID); err != nil {
			t.Fatal(err)
		}
	}
	if targets, err := store.ListOwnedTargets(ctx, owner); err != nil || len(targets) != 1 || targets[0].TargetID == unsharedID {
		t.Fatalf("revoked targets: %+v, %v", targets, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE collections SET is_reserved = 1 WHERE owner_id = ? AND id = ?`,
		owner, collectionID); err != nil {
		t.Fatal(err)
	}
	if targets, err := store.ListOwnedTargets(ctx, owner); err != nil || len(targets) != 1 || !targets[0].Reserved {
		t.Fatalf("reserved collection: %+v, %v", targets, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM days WHERE owner_id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM collections WHERE owner_id = ? AND id = ?`,
		owner, collectionID); err != nil {
		t.Fatal(err)
	}
	if targets, err := store.ListOwnedTargets(ctx, owner); err != nil || len(targets) != 0 {
		t.Fatalf("deleted targets: %+v, %v", targets, err)
	}
}

func TestSharedLunarCalendar(t *testing.T) {
	store, owner, recipient, _, _, dayID, _ := shareFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`UPDATE days SET calendar = 'lunar', date = '2024-02-10', repeat = 'yearly' WHERE owner_id = ? AND id = ?`, owner, dayID); err != nil {
		t.Fatal(err)
	}
	shared, err := store.Create(ctx, owner, TargetDay, dayID, []string{"Bob"}, false, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Respond(ctx, recipient, shared.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetShared(ctx, recipient, dayID)
	if err != nil || got.Calendar != "lunar" || got.Repeat != "yearly" {
		t.Fatalf("shared lunar day: %+v %v", got, err)
	}
}

func TestSharesRecipientsAccessAndCommentVisibility(t *testing.T) {
	store, owner, bob, carol, collectionID, dayID, secondDayID := shareFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	direct, err := store.Create(ctx, owner, TargetDay, dayID,
		[]string{"bOb", "BOB@example.com", "carol@example.com"}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Recipients) != 2 {
		t.Fatalf("deduplicated recipients = %d, want 2", len(direct.Recipients))
	}
	for _, userID := range []int64{bob, carol} {
		if err := store.Respond(ctx, userID, direct.ID, true); err != nil {
			t.Fatal(err)
		}
		allowed, showComment, err := store.CanView(ctx, userID, dayID)
		if err != nil || !allowed || !showComment {
			t.Errorf("CanView(%d) = %v, %v, %v", userID, allowed, showComment, err)
		}
	}
	if allowed, _, err := store.CanView(ctx, bob, secondDayID); err != nil || allowed {
		t.Fatalf("unshared day access = %v, %v", allowed, err)
	}

	collection, err := store.Create(ctx, owner, TargetCollection, collectionID,
		[]string{"bob@example.com"}, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Respond(ctx, bob, collection.ID, true); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListForUser(ctx, bob, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("shared list = %d days, err %v", len(list), err)
	}
	for _, d := range list {
		if d.ID == dayID && (!d.ShowComment || d.OwnerUsername != "Alice") {
			t.Errorf("day access metadata: %+v", d)
		}
		if d.ID == dayID && (d.Comment != "private note") {
			t.Errorf("shared day data missing authorized comment: %+v", d)
		}
	}

	if err := store.SetHidden(ctx, bob, secondDayID, true); err != nil {
		t.Fatal(err)
	}
	home, err := store.ListForUser(ctx, bob, false)
	if err != nil || len(home) != 1 || home[0].ID != dayID {
		t.Fatalf("home list after hide = %+v, %v", home, err)
	}
	all, err := store.ListForUser(ctx, bob, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("shared page after hide = %+v, %v", all, err)
	}
	for _, d := range all {
		if d.ID == secondDayID && !d.Hidden {
			t.Errorf("hidden state not retained on shared page: %+v", d)
		}
	}
	if err := store.SetHidden(ctx, bob, secondDayID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, owner, direct.ID); err != nil {
		t.Fatal(err)
	}
	allowed, showComment, err := store.CanView(ctx, bob, dayID)
	if err != nil || !allowed || showComment {
		t.Errorf("collection share should remain without comment: %v, %v, %v", allowed, showComment, err)
	}
	if err := store.Revoke(ctx, owner, collection.ID); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := store.CanView(ctx, bob, dayID); err != nil || allowed {
		t.Errorf("revoked share still grants access: %v, %v", allowed, err)
	}
}

func TestCollectionShareIsLiveAndExcludesArchivedDays(t *testing.T) {
	store, owner, bob, _, collectionID, first, second := shareFixture(t)
	ctx := context.Background()
	sh, err := store.Create(ctx, owner, TargetCollection, collectionID, []string{"bob"}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Respond(ctx, bob, sh.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE days SET archived_at = 1 WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListForUser(ctx, bob, true)
	if err != nil || len(list) != 1 || list[0].ID != second {
		t.Fatalf("live collection list = %+v, %v", list, err)
	}
	if _, err := store.GetShared(ctx, bob, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived collection day returned %v, want ErrNotFound", err)
	}
}

func TestShareRejectsUnknownAndSelfRecipientsGenerically(t *testing.T) {
	store, owner, _, _, _, dayID, _ := shareFixture(t)
	ctx := context.Background()
	_, err := store.Create(ctx, owner, TargetDay, dayID, []string{"unknown@example.com"}, false, time.Now())
	if !errors.Is(err, ErrRecipientsInvalid) {
		t.Fatalf("unknown recipient error = %v", err)
	}
	_, err = store.Create(ctx, owner, TargetDay, dayID, []string{"ALICE"}, false, time.Now())
	if !errors.Is(err, ErrCannotShareSelf) {
		t.Fatalf("self recipient error = %v", err)
	}
}

func TestShareUpdateReplacesRecipientsAndCommentVisibility(t *testing.T) {
	store, owner, bob, carol, _, dayID, _ := shareFixture(t)
	ctx := context.Background()
	sh, err := store.Create(ctx, owner, TargetDay, dayID, []string{"bob"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, owner, sh.ID, []string{"CAROL@example.com"}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Respond(ctx, carol, sh.ID, true); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := store.CanView(ctx, bob, dayID); err != nil || allowed {
		t.Errorf("removed recipient still has access: %v, %v", allowed, err)
	}
	if allowed, showComment, err := store.CanView(ctx, carol, dayID); err != nil || !allowed || showComment {
		t.Errorf("replacement recipient access: %v, %v, %v", allowed, showComment, err)
	}
	shared, err := store.GetShared(ctx, carol, dayID)
	if err != nil || shared.Comment != "" {
		t.Errorf("hidden comment leaked from shared-day result: %+v, %v", shared, err)
	}
}
