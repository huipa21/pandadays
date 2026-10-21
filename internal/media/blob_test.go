package media

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pandadays/internal/db/dbtest"
)

type blobFixture struct {
	db    *sql.DB
	root  string
	store *Store
}

func newBlobFixture(t *testing.T) *blobFixture {
	t.Helper()
	conn := dbtest.Open(t)
	root := filepath.Join(t.TempDir(), "media")
	return &blobFixture{db: conn, root: root, store: New(conn, root)}
}

func (f *blobFixture) user(t *testing.T, name string) int64 {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO users (username, email, password_hash) VALUES (?, ?, 'x')`, name, name+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *blobFixture) day(t *testing.T, ownerID int64) int64 {
	t.Helper()
	var collectionID int64
	err := f.db.QueryRow(`SELECT id FROM collections WHERE owner_id = ? AND is_reserved = 1`, ownerID).Scan(&collectionID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insertErr := f.db.Exec(`INSERT INTO collections (owner_id, name, is_reserved) VALUES (?, 'Default', 1)`, ownerID)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		collectionID, _ = res.LastInsertId()
	} else if err != nil {
		t.Fatal(err)
	}
	res, err := f.db.Exec(`INSERT INTO days (owner_id, collection_id, title, date) VALUES (?, ?, 'Day', '2026-10-20')`,
		ownerID, collectionID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *blobFixture) upload(t *testing.T, ownerID, dayID int64, name, content string) Attachment {
	t.Helper()
	a, err := f.store.Upload(context.Background(), ownerID, dayID, name, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *blobFixture) used(t *testing.T, ownerID int64) int64 {
	t.Helper()
	var used int64
	if err := f.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, ownerID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	return used
}

func (f *blobFixture) files(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(f.root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readAll(t *testing.T, s *Store, a Attachment) string {
	t.Helper()
	file, err := s.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var b strings.Builder
	if _, err := file.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestUploadRejectsContentAlreadyInOwnersLibrary(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	alice := f.user(t, "alice")
	first, second := f.day(t, alice), f.day(t, alice)
	original := f.upload(t, alice, first, "photo.jpg", "same bytes")

	_, err := f.store.Upload(ctx, alice, second, "renamed.jpg", strings.NewReader("same bytes"))
	var dup *DuplicateError
	if !errors.Is(err, ErrDuplicate) || !errors.As(err, &dup) || dup.ExistingID != original.ID {
		t.Fatalf("duplicate upload: %v", err)
	}
	// Removed-from-day items stay in the library and still count as duplicates.
	if err := f.store.Detach(ctx, alice, first, original.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Upload(ctx, alice, second, "again.jpg", strings.NewReader("same bytes")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate of removed library item: %v", err)
	}
	if got := f.used(t, alice); got != original.Size {
		t.Fatalf("used_bytes after rejected duplicates = %d, want %d", got, original.Size)
	}
	if files := f.files(t); len(files) != 1 {
		t.Fatalf("stored files = %v, want only the original blob", files)
	}
	if id, err := f.store.FindBySum(ctx, alice, original.SHA256); err != nil || id != original.ID {
		t.Fatalf("FindBySum = %d, %v", id, err)
	}
	if _, err := f.store.Upload(ctx, alice, second, "other.jpg", strings.NewReader("different bytes")); err != nil {
		t.Fatalf("different content rejected: %v", err)
	}
}

func TestLibraryUploadsAndSelectionsReuseStoredFile(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	owner := f.user(t, "owner")
	other := f.user(t, "other")
	a, err := f.store.UploadLibrary(ctx, owner, "photo.png", strings.NewReader("library image"))
	if err != nil || a.DayID != 0 {
		t.Fatalf("standalone upload: %+v %v", a, err)
	}
	if got, err := f.store.GetAccessible(ctx, owner, a.ID); err != nil || got.ID != a.ID {
		t.Fatalf("owner cannot read library upload: %+v %v", got, err)
	}
	if _, err := f.store.GetAccessible(ctx, other, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign library upload accessible: %v", err)
	}
	first, second := f.day(t, owner), f.day(t, owner)
	for _, day := range []int64{first, first, second, second} {
		if _, err := f.store.CopyToDay(ctx, owner, a.ID, day); err != nil {
			t.Fatal(err)
		}
		items, err := f.store.ListDay(ctx, owner, day)
		if err != nil || len(items) != 1 {
			t.Fatalf("repeated selection duplicated day entry: %+v %v", items, err)
		}
	}
	items, err := f.store.ListOwner(ctx, owner)
	if err != nil || len(items) != 1 || items[0].ID != a.ID {
		t.Fatalf("library has duplicate entries: %+v %v", items, err)
	}
	if files := f.files(t); len(files) != 1 {
		t.Fatalf("selection duplicated stored files: %v", files)
	}
	if _, err := f.store.CopyToDay(ctx, other, a.ID, f.day(t, other)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign selection succeeded: %v", err)
	}
	if _, err := f.store.UploadLibrary(ctx, owner, "duplicate.png", strings.NewReader("library image")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate standalone upload: %v", err)
	}
	if err := f.store.Delete(ctx, owner, a.ID); err != nil {
		t.Fatal(err)
	}
	items, err = f.store.ListOwner(ctx, owner)
	if err != nil || len(items) != 1 || readAll(t, f.store, items[0].Attachment) != "library image" {
		t.Fatalf("deleting library source lost day references: %+v %v", items, err)
	}
}

func TestStandaloneLibraryUploadUsesAccountQuotaNotCardLimit(t *testing.T) {
	f := newBlobFixture(t)
	owner := f.user(t, "owner")
	if _, err := f.db.Exec(`UPDATE users SET quota_bytes = 10, max_card_bytes = 2 WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := f.store.UploadLibrary(ctx, owner, "file.txt", strings.NewReader("12345"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CopyToDay(ctx, owner, a.ID, f.day(t, owner)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("day limit ignored: %v", err)
	}
	if _, err := f.store.UploadLibrary(ctx, owner, "large.txt", strings.NewReader("123456")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("account limit ignored: %v", err)
	}
	if err := f.store.Delete(ctx, owner, a.ID); err != nil {
		t.Fatal(err)
	}
	if f.used(t, owner) != 0 || len(f.files(t)) != 0 {
		t.Fatal("standalone deletion failed to release quota or stored file")
	}
}

func TestUsersShareStoredContentButKeepOwnMetadata(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	alice, bob := f.user(t, "alice"), f.user(t, "bob")
	a := f.upload(t, alice, f.day(t, alice), "alice.png", "shared content")
	b := f.upload(t, bob, f.day(t, bob), "bob-copy.png", "shared content")

	if a.SHA256 == "" || a.SHA256 != b.SHA256 || a.UUID == b.UUID || a.ID == b.ID {
		t.Fatalf("attachments: %+v / %+v", a, b)
	}
	if f.store.Path(alice, a) != f.store.Path(bob, b) {
		t.Fatal("identical content is stored twice")
	}
	if files := f.files(t); len(files) != 1 || !strings.HasPrefix(files[0], "blobs/"+a.SHA256[:2]+"/") {
		t.Fatalf("stored files = %v", files)
	}
	if got, _ := f.store.Get(ctx, bob, b.ID); got.Filename != "bob-copy.png" || got.OwnerID != bob {
		t.Fatalf("bob metadata = %+v", got)
	}
	if _, err := f.store.Get(ctx, bob, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob can read alice's attachment row: %v", err)
	}
	// Each user is charged for their own attachment.
	if f.used(t, alice) != a.Size || f.used(t, bob) != b.Size {
		t.Fatalf("used_bytes alice=%d bob=%d", f.used(t, alice), f.used(t, bob))
	}

	if err := f.store.Delete(ctx, alice, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, f.store, b); got != "shared content" {
		t.Fatalf("bob's content after alice deleted hers: %q", got)
	}
	if err := f.store.Delete(ctx, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	if files := f.files(t); len(files) != 0 {
		t.Fatalf("blob kept after last reference: %v", files)
	}
	var queued int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM blob_gc`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("blob_gc rows = %d, %v", queued, err)
	}
}

func TestCopyToDaySharesContentAndSurvivesSourceDeletion(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	alice := f.user(t, "alice")
	src := f.upload(t, alice, f.day(t, alice), "voice.webm", "recording")
	copied, err := f.store.CopyToDay(ctx, alice, src.ID, f.day(t, alice))
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == src.ID || copied.SHA256 != src.SHA256 || copied.Filename != src.Filename || copied.Kind != src.Kind {
		t.Fatalf("copy = %+v, source = %+v", copied, src)
	}
	if f.used(t, alice) != 2*src.Size {
		t.Fatalf("copy not charged to quota: used_bytes = %d", f.used(t, alice))
	}
	if files := f.files(t); len(files) != 1 {
		t.Fatalf("copy duplicated stored bytes: %v", files)
	}
	if err := f.store.Delete(ctx, alice, src.ID); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, f.store, copied); got != "recording" {
		t.Fatalf("copy content = %q", got)
	}
}

func TestCascadeDeletesReleaseBlobs(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	alice, bob := f.user(t, "alice"), f.user(t, "bob")
	aliceDay := f.day(t, alice)
	f.upload(t, alice, aliceDay, "only-alice.txt", "alice only")
	f.upload(t, alice, aliceDay, "shared.txt", "both have this")
	b := f.upload(t, bob, f.day(t, bob), "shared.txt", "both have this")

	if _, err := f.db.Exec(`DELETE FROM days WHERE id = ?`, aliceDay); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if files := f.files(t); len(files) != 1 || f.store.Path(bob, b) != filepath.Join(f.root, filepath.FromSlash(files[0])) {
		t.Fatalf("after day deletion files = %v", files)
	}
	if _, err := f.db.Exec(`DELETE FROM users WHERE id = ?`, bob); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if files := f.files(t); len(files) != 0 {
		t.Fatalf("after user deletion files = %v", files)
	}
}

func TestMigrateLegacyMovesFilesIntoSharedBlobs(t *testing.T) {
	f := newBlobFixture(t)
	ctx := context.Background()
	alice, bob := f.user(t, "alice"), f.user(t, "bob")
	legacy := func(owner int64, uuid, content string) Attachment {
		t.Helper()
		dir := filepath.Join(f.root, strconv.FormatInt(owner, 10))
		if content != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, uuid), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		res, err := f.db.Exec(`INSERT INTO attachments (day_id, owner_id, uuid, kind, filename, mime, size)
			VALUES (?, ?, ?, 'file', 'legacy.txt', 'text/plain', ?)`, f.day(t, owner), owner, uuid, len(content))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return Attachment{ID: id, OwnerID: owner}
	}
	a := legacy(alice, "11111111-1111-4111-8111-111111111111", "legacy bytes")
	b := legacy(bob, "22222222-2222-4222-8222-222222222222", "legacy bytes")
	missing := legacy(bob, "33333333-3333-4333-8333-333333333333", "")

	moved, err := f.store.MigrateLegacy(ctx)
	if err != nil || moved != 2 {
		t.Fatalf("MigrateLegacy = %d, %v", moved, err)
	}
	gotA, _ := f.store.Get(ctx, alice, a.ID)
	gotB, _ := f.store.Get(ctx, bob, b.ID)
	if gotA.SHA256 == "" || gotA.SHA256 != gotB.SHA256 {
		t.Fatalf("hashes alice=%q bob=%q", gotA.SHA256, gotB.SHA256)
	}
	if readAll(t, f.store, gotA) != "legacy bytes" || readAll(t, f.store, gotB) != "legacy bytes" {
		t.Fatal("migrated content differs")
	}
	if files := f.files(t); len(files) != 1 {
		t.Fatalf("files after migration = %v", files)
	}
	if _, err := os.Stat(filepath.Join(f.root, strconv.FormatInt(alice, 10))); !os.IsNotExist(err) {
		t.Fatalf("empty legacy directory kept: %v", err)
	}
	gotMissing, _ := f.store.Get(ctx, bob, missing.ID)
	if gotMissing.SHA256 != "" {
		t.Fatalf("missing legacy file got a hash: %q", gotMissing.SHA256)
	}
	if _, err := f.store.Open(gotMissing); !errors.Is(err, ErrFileMissing) {
		t.Fatalf("missing legacy file open: %v", err)
	}
	if moved, err := f.store.MigrateLegacy(ctx); err != nil || moved != 0 {
		t.Fatalf("rerun MigrateLegacy = %d, %v", moved, err)
	}
}
