package media

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pandadays/internal/days"
)

func TestPrivateAttachmentsAndLibrary(t *testing.T) {
	ctx := context.Background()
	private := days.WithPrivate(ctx, true)
	f := newBlobFixture(t)
	owner := f.user(t, "alice")
	other := f.user(t, "bob")
	day := f.day(t, owner)
	a := f.upload(t, owner, day, "secret.txt", "private content")
	if _, err := f.db.Exec(`UPDATE days SET is_private = 1 WHERE owner_id = ? AND id = ?`, owner, day); err != nil {
		t.Fatal(err)
	}
	standalone, err := f.store.UploadLibrary(private, owner, "vault.txt", strings.NewReader("private standalone"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, standalone.ID} {
		if _, err := f.store.Get(ctx, owner, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private metadata leaked: %v", err)
		}
		if _, err := f.store.GetAccessible(ctx, owner, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private file accessible outside Vault: %v", err)
		}
		if _, err := f.store.GetAccessible(private, other, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private file accessible to another owner: %v", err)
		}
		if err := f.store.Rename(ctx, owner, id, "leaked.txt"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private rename outside Vault: %v", err)
		}
		if err := f.store.Delete(ctx, owner, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private delete outside Vault: %v", err)
		}
		if _, err := f.store.GetAccessible(private, owner, id); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := f.store.ListOwner(ctx, owner); err != nil || len(list) != 0 {
		t.Fatalf("private library leaked: %+v %v", list, err)
	}
	if list, err := f.store.ListOwner(private, owner); err != nil || len(list) != 2 {
		t.Fatalf("private library: %+v %v", list, err)
	}
	if list, err := f.store.ListDay(ctx, owner, day); err != nil || len(list) != 0 {
		t.Fatalf("private attachments leaked: %+v %v", list, err)
	}
	if err := f.store.Detach(ctx, owner, day, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private detach outside Vault: %v", err)
	}
	if _, err := f.store.Upload(ctx, owner, day, "bad.txt", strings.NewReader("should fail")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private upload outside Vault: %v", err)
	}
	// Deduplication must select the visible reference, not hide it behind an earlier private one.
	normalDay := f.day(t, owner)
	copied, err := f.store.CopyToDay(private, owner, a.ID, day)
	if err != nil || copied.ID != a.ID {
		t.Fatalf("private reuse: %+v %v", copied, err)
	}
	if _, err := f.store.CopyToDay(ctx, owner, a.ID, normalDay); !errors.Is(err, ErrNotFound) {
		t.Fatalf("copied private media from normal mode: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO attachments(day_id, owner_id, uuid, sha256, kind, filename, mime, size)
		VALUES (?, ?, 'public-copy', ?, 'file', 'public.txt', 'text/plain', ?)`, normalDay, owner, a.SHA256, a.Size); err != nil {
		t.Fatal(err)
	}
	list, err := f.store.ListOwner(ctx, owner)
	if err != nil || len(list) != 1 || list[0].DayID != normalDay {
		t.Fatalf("visible duplicate reference: %+v %v", list, err)
	}
}
