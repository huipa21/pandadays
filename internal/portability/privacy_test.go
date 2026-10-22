package portability

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

func TestStagedImportPrivacyBinding(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprintf("private=%t", private), func(t *testing.T) {
			f := newFixture(t)
			owner := f.user(t, "alice")
			token := f.stage(t, owner, []byte("title,date,collection,tags\nSecret,2026-01-01,Secret collection,Secret tag\n"))
			ctx := days.WithPrivate(context.Background(), private)
			otherCtx := days.WithPrivate(context.Background(), !private)
			preview, err := f.svc.Preview(ctx, owner, token, testNow)
			if err != nil || preview.New != 1 {
				t.Fatalf("initial preview=%+v error=%v", preview, err)
			}
			// Binding survives replacement of the service, not just one request.
			svc := New(f.db, filepath.Join(f.root, "media"), filepath.Join(f.root, "tmp"))
			if got, err := svc.Preview(otherCtx, owner, token, testNow); !errors.Is(err, ErrStagedNotFound) ||
				got.New != 0 || len(got.NewCollections) != 0 || len(got.NewTags) != 0 {
				t.Fatalf("cross-context preview exposed staged data: %+v error=%v", got, err)
			}
			if result, err := svc.Import(otherCtx, owner, token, testNow); !errors.Is(err, ErrStagedNotFound) || result.Imported != 0 {
				t.Fatalf("cross-context confirmation accepted: %+v error=%v", result, err)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, owner).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected confirmation created days: %d error=%v", count, err)
			}
			if _, err := svc.Preview(ctx, owner, token, testNow); err != nil {
				t.Fatalf("rejected request consumed original token: %v", err)
			}
			result, err := svc.Import(ctx, owner, token, testNow)
			if err != nil || result.Imported != 1 {
				t.Fatalf("original confirmation=%+v error=%v", result, err)
			}
			var gotPrivate bool
			if err := f.db.QueryRow(`SELECT is_private FROM days WHERE owner_id = ?`, owner).Scan(&gotPrivate); err != nil || gotPrivate != private {
				t.Fatalf("legacy import privacy=%t want=%t error=%v", gotPrivate, private, err)
			}
			entries, err := os.ReadDir(filepath.Join(f.root, "tmp"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("confirmed upload not removed: %v error=%v", entries, err)
			}
		})
	}
}

func TestStagedPrivacyBindingCleanup(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "alice")
	ctx := days.WithPrivate(context.Background(), true)
	for _, discard := range []bool{false, true} {
		token := f.stage(t, owner, []byte("title,date\nSecret,2026-01-01\n"))
		if _, err := f.svc.Preview(ctx, owner, token, testNow); err != nil {
			t.Fatal(err)
		}
		if discard {
			f.svc.Discard(owner, token)
		} else {
			expired := testNow.Add(StagedTTL + time.Minute)
			if _, err := f.svc.Preview(ctx, owner, token, expired); !errors.Is(err, ErrStagedNotFound) {
				t.Fatalf("expired bound token accepted: %v", err)
			}
			f.svc.sweepStaged(expired)
		}
		entries, err := os.ReadDir(filepath.Join(f.root, "tmp"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("bound upload not cleaned up: %v error=%v", entries, err)
		}
	}
}

func TestExportPrivacyAndRoundTrip(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "alice")
	other := f.user(t, "other")
	f.seed(t, owner)
	f.seed(t, other)
	if _, err := f.db.Exec(`UPDATE days SET is_private = 1 WHERE title = 'Paris'`); err != nil {
		t.Fatal(err)
	}
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprintf("private=%t", private), func(t *testing.T) {
			ctx := days.WithPrivate(context.Background(), private)
			var buf bytes.Buffer
			if err := f.svc.Export(ctx, owner, testNow, &buf); err != nil {
				t.Fatal(err)
			}
			_, recs := readCSV(t, buf.Bytes())
			wantDays, wantFiles := 3, 1
			if private {
				wantDays, wantFiles = 1, 2
			}
			if len(recs)-1 != wantDays {
				t.Fatalf("exported %d days, want %d", len(recs)-1, wantDays)
			}
			var referenced []string
			for _, rec := range recs[1:] {
				if rec[13] != fmt.Sprint(private) || (rec[0] == "Paris") != private {
					t.Fatalf("wrong privacy mode in row: %v", rec)
				}
				referenced = append(referenced, splitList(rec[11])...)
				if private && (rec[5] != `a\|b\\c|Fun` || rec[10] == "" || !contains(splitList(rec[11]), rec[10])) {
					t.Fatalf("private tags/background missing: %v", rec)
				}
			}
			zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if len(zr.File)-1 != wantFiles || len(referenced) != wantFiles {
				t.Fatalf("wrong attachment count: zip=%d CSV=%d", len(zr.File)-1, len(referenced))
			}
			for _, entry := range zr.File {
				if entry.Name != csvName && !contains(referenced, entry.Name) {
					t.Fatalf("export leaked unreferenced attachment %q", entry.Name)
				}
			}
			recipient := f.user(t, fmt.Sprintf("recipient%t", private))
			preview, result := f.importData(t, recipient, buf.Bytes())
			if preview.New != wantDays || result.Imported != wantDays || result.Attachments != wantFiles {
				t.Fatalf("round trip preview=%+v result=%+v", preview, result)
			}
			var count, privateCount int
			if err := f.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_private), 0) FROM days WHERE owner_id = ?`,
				recipient).Scan(&count, &privateCount); err != nil {
				t.Fatal(err)
			}
			wantPrivate := 0
			if private {
				wantPrivate = wantDays
			}
			if count != wantDays || privateCount != wantPrivate {
				t.Fatalf("round trip lost privacy: total=%d private=%d", count, privateCount)
			}
		})
	}
}

func TestImportPrivacyColumnAndContext(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, column := range []string{"legacy", "", "false", "true", "yes", "1", "invalid"} {
			t.Run(fmt.Sprintf("private=%t/column=%s", private, column), func(t *testing.T) {
				f := newFixture(t)
				owner := f.user(t, "alice")
				data := "title,date\nImported,2026-01-01\n"
				if column != "legacy" {
					data = "title,date,is_private\nImported,2026-01-01," + column + "\n"
				}
				token := f.stage(t, owner, []byte(data))
				ctx := days.WithPrivate(context.Background(), private)
				preview, err := f.svc.Preview(ctx, owner, token, testNow)
				if err != nil {
					t.Fatal(err)
				}
				result, err := f.svc.Import(ctx, owner, token, testNow)
				if err != nil {
					t.Fatal(err)
				}
				if column == "invalid" {
					if preview.New != 0 || len(preview.Errors) != 1 ||
						!errors.Is(preview.Errors[0].Err, ErrPrivateInvalid) || result.Errors != 1 || result.Imported != 0 {
						t.Fatalf("invalid privacy accepted: preview=%+v result=%+v", preview, result)
					}
					return
				}
				if preview.New != 1 || len(preview.Errors) != 0 || result.Imported != 1 {
					t.Fatalf("preview=%+v result=%+v", preview, result)
				}
				var got bool
				if err := f.db.QueryRow(`SELECT is_private FROM days WHERE owner_id = ?`, owner).Scan(&got); err != nil {
					t.Fatal(err)
				}
				want := private || column == "true" || column == "yes" || column == "1"
				if got != want {
					t.Fatalf("imported privacy=%t, want %t", got, want)
				}
			})
		}
	}
}

func TestImportPrivacyDuplicateIdentity(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "alice")
	data := []byte("title,date,is_private\nSame,2026-01-01,false\nSame,2026-01-01,true\n")
	preview, result := f.importData(t, owner, data)
	if preview.New != 2 || result.Imported != 2 {
		t.Fatalf("privacy modes incorrectly deduplicated: preview=%+v result=%+v", preview, result)
	}
	preview, result = f.importData(t, owner, data)
	if preview.New != 0 || preview.Skipped != 2 || result.Skipped != 2 {
		t.Fatalf("duplicates not skipped: preview=%+v result=%+v", preview, result)
	}
	recipient := f.user(t, "bobby")
	token := f.stage(t, recipient, data)
	ctx := days.WithPrivate(context.Background(), true)
	preview, err := f.svc.Preview(ctx, recipient, token, testNow)
	if err != nil {
		t.Fatal(err)
	}
	result, err = f.svc.Import(ctx, recipient, token, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if preview.New != 1 || preview.Skipped != 1 || result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("private context must deduplicate after forcing privacy: preview=%+v result=%+v", preview, result)
	}
}

func TestImportPrivacyQuotasRemainTotal(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprintf("private=%t", private), func(t *testing.T) {
			f := newFixture(t)
			owner := f.user(t, "alice")
			f.seed(t, owner)
			if _, err := f.db.Exec(`UPDATE days SET is_private = 1 WHERE owner_id = ? AND title = 'Paris'`, owner); err != nil {
				t.Fatal(err)
			}
			var used int64
			if err := f.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, owner).Scan(&used); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE users SET quota_days = 4, quota_bytes = ? WHERE id = ?`, used+2, owner); err != nil {
				t.Fatal(err)
			}
			ctx := days.WithPrivate(context.Background(), private)
			data := makeZip(t, entry{name: csvName, body: "title,date,attachments\nNew,2026-01-01,media/a.txt\n"},
				entry{name: "media/a.txt", body: "123"})
			token := f.stage(t, owner, data)
			preview, err := f.svc.Preview(ctx, owner, token, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if preview.DaysUsed != 4 || preview.BytesUsed != used || !preview.DaysExceeded || !preview.BytesExceeded {
				t.Fatalf("quotas did not count both privacy modes: %+v", preview)
			}
			if _, err := f.svc.Import(ctx, owner, token, testNow); !errors.Is(err, ErrQuota) {
				t.Fatalf("import error=%v, want quota error", err)
			}
			limit, err := f.svc.UploadLimit(ctx, owner)
			if err != nil || limit != f.svc.Limits.MaxCSVBytes+2 {
				t.Fatalf("upload limit=%d error=%v", limit, err)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, owner).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 4 {
				t.Fatal("rejected import changed days")
			}
		})
	}
}

func TestExportDoesNotReadExcludedPrivateMedia(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "alice")
	f.seed(t, owner)
	if _, err := f.db.Exec(`UPDATE days SET is_private = 1 WHERE owner_id = ? AND title = 'Paris'`, owner); err != nil {
		t.Fatal(err)
	}
	// An excluded file's size mismatch must not make a normal export fail.
	if _, err := f.db.Exec(`UPDATE attachments SET size = size + 1
		WHERE owner_id = ? AND day_id IN (SELECT id FROM days WHERE owner_id = ? AND is_private = 1)`, owner, owner); err != nil {
		t.Fatal(err)
	}
	data := f.export(t, owner)
	raw, _ := readCSV(t, data)
	if strings.Contains(raw, "Paris") || strings.Contains(raw, "photo 1.png") {
		t.Fatal("normal export leaked private day or media")
	}
}
