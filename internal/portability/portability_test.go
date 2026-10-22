package portability

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/db/dbtest"
	"pandadays/internal/media"
)

// testNow is close to the wall clock because staged uploads expire by file mtime.
var testNow = time.Now().UTC().Truncate(time.Second)

type fixture struct {
	db    *sql.DB
	svc   *Service
	media *media.Store
	root  string
	days  *days.Days
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := dbtest.Open(t)
	root := t.TempDir()
	return &fixture{
		db: db, root: root,
		svc:   New(db, filepath.Join(root, "media"), filepath.Join(root, "tmp")),
		media: media.New(db, filepath.Join(root, "media")),
		days:  days.NewDays(db),
	}
}

func (f *fixture) user(t *testing.T, name string) int64 {
	t.Helper()
	if len(name) < 4 {
		name += "1"
	}
	svc, err := auth.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	u, err := svc.CreateUser(context.Background(), auth.CreateUserParams{
		Username: name, Email: name + "@example.com", Password: "password123"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (f *fixture) export(t *testing.T, owner int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := f.svc.Export(context.Background(), owner, testNow, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExportDoesNotReattachRemovedLibraryMedia(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "alice")
	ctx := context.Background()
	d, err := f.days.Create(ctx, owner, days.DayInput{Title: "Trip", Date: "2026-10-18"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.media.Upload(ctx, owner, d.ID, "removed.txt", strings.NewReader("retained"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.media.Detach(ctx, owner, d.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	data := f.export(t, owner)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range zr.File {
		if strings.HasPrefix(entry.Name, mediaPrefix) {
			t.Fatalf("removed attachment exported as current day media: %s", entry.Name)
		}
	}
	recipient := f.user(t, "bobby")
	_, result := f.importData(t, recipient, data)
	if result.Imported != 1 {
		t.Fatalf("export lost source day: %+v", result)
	}
	items, err := f.media.ListOwner(ctx, recipient)
	if err != nil || len(items) != 0 {
		t.Fatalf("import reattached removed media: %+v %v", items, err)
	}
}

func (f *fixture) stage(t *testing.T, owner int64, data []byte) string {
	t.Helper()
	token, err := f.svc.Stage(owner, bytes.NewReader(data), 1<<30, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *fixture) importData(t *testing.T, owner int64, data []byte) (Preview, Result) {
	t.Helper()
	token := f.stage(t, owner, data)
	p, err := f.svc.Preview(context.Background(), owner, token, testNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Import(context.Background(), owner, token, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return p, res
}

// seed creates a varied data set for owner: custom and default groups,
// archived/timed/repeating days, formula-like text, and attachments.
func (f *fixture) seed(t *testing.T, owner int64) {
	t.Helper()
	ctx := context.Background()
	colls, tags := days.NewCollections(f.db), days.NewTags(f.db)
	trips, err := colls.Create(ctx, owner, days.GroupInput{Name: "Trips"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	fun, err := tags.Create(ctx, owner, days.GroupInput{Name: "Fun"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	odd, err := tags.Create(ctx, owner, days.GroupInput{Name: `a|b\c`}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []days.DayInput{
		{Title: "Paris", Date: "2027-05-01", Time: "08:30", CollectionID: trips.ID, TagIDs: []int64{fun.ID, odd.ID},
			Comment: "Line one\nLine \"two\", with comma", StyleColor: "#112233", StyleIcon: "✈️"},
		{Title: "=HYPERLINK(\"http://evil\")", Date: "2020-01-02", Repeat: days.RepeatYearly, Comment: "@SUM(A1)"},
		{Title: "'=already quoted", Date: "2021-03-04", Comment: "-1+2"},
		{Title: "Archived thing", Date: "2019-07-08", CollectionID: trips.ID},
	}
	var created []days.Day
	for _, in := range inputs {
		d, err := f.days.Create(ctx, owner, in, testNow)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, d)
	}
	if err := f.days.SetArchived(ctx, owner, created[3].ID, true, testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.days.SetArchiveLive(ctx, owner, created[3].ID, true); err != nil {
		t.Fatal(err)
	}
	img, err := f.media.Upload(ctx, owner, created[0].ID, "photo 1.png", bytes.NewReader(fixturePNG(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.media.Upload(ctx, owner, created[0].ID, "notes.txt", strings.NewReader("hello notes")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.media.Upload(ctx, owner, created[3].ID, "archived.txt", strings.NewReader("archived file")); err != nil {
		t.Fatal(err)
	}
	if err := f.media.SetBackground(ctx, owner, created[0].ID, img.ID); err != nil {
		t.Fatal(err)
	}
}

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// snapshot renders the owner's days, groups and media independent of IDs.
func (f *fixture) snapshot(t *testing.T, owner int64) ([]string, int64) {
	t.Helper()
	rows, err := f.db.Query(`SELECT d.id, d.title, d.date, COALESCE(d.time, ''), d.repeat,
		CASE WHEN c.is_reserved = 1 THEN '<default>' ELSE c.name END, COALESCE(d.comment, ''),
		COALESCE(d.archived_at, 0), d.archive_live, COALESCE(d.style_color, ''), COALESCE(d.style_icon, ''),
		COALESCE((SELECT filename FROM attachments WHERE id = d.style_bg_attachment_id), '')
		FROM days d JOIN collections c ON c.id = d.collection_id WHERE d.owner_id = ?`, owner)
	if err != nil {
		t.Fatal(err)
	}
	type dayRow struct {
		id   int64
		line string
	}
	var list []dayRow
	for rows.Next() {
		var id, archived int64
		var live bool
		var title, date, clock, repeat, coll, comment, color, icon, bg string
		if err := rows.Scan(&id, &title, &date, &clock, &repeat, &coll, &comment, &archived, &live, &color, &icon, &bg); err != nil {
			t.Fatal(err)
		}
		list = append(list, dayRow{id, fmt.Sprintf("%q %s %s %s %s %q %d live=%t %s %s bg=%q",
			title, date, clock, repeat, coll, comment, archived, live, color, icon, bg)})
	}
	rows.Close()
	var out []string
	for _, d := range list {
		var tagNames []string
		tr, err := f.db.Query(`SELECT CASE WHEN t.is_reserved = 1 THEN '<default>' ELSE t.name END
			FROM day_tags dt JOIN tags t ON t.id = dt.tag_id WHERE dt.day_id = ?`, d.id)
		if err != nil {
			t.Fatal(err)
		}
		for tr.Next() {
			var n string
			tr.Scan(&n)
			tagNames = append(tagNames, n)
		}
		tr.Close()
		sort.Strings(tagNames)
		var files []string
		ar, err := f.db.Query(`SELECT uuid, COALESCE(sha256, ''), kind, filename, mime, size FROM attachments WHERE day_id = ? AND owner_id = ?`, d.id, owner)
		if err != nil {
			t.Fatal(err)
		}
		for ar.Next() {
			var a media.Attachment
			ar.Scan(&a.UUID, &a.SHA256, &a.Kind, &a.Filename, &a.MIME, &a.Size)
			content, err := os.ReadFile(f.media.Path(owner, a))
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, fmt.Sprintf("%s/%s/%s/%d/%x", a.Filename, a.Kind, a.MIME, a.Size, sha256.Sum256(content)))
		}
		ar.Close()
		sort.Strings(files)
		out = append(out, d.line+" tags="+strings.Join(tagNames, ",")+" files="+strings.Join(files, ","))
	}
	sort.Strings(out)
	var used int64
	if err := f.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, owner).Scan(&used); err != nil {
		t.Fatal(err)
	}
	return out, used
}

func TestExportImportRoundTrip(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "alice"), f.user(t, "bob")
	f.seed(t, a)
	// Bob's own data and Alice's shared view must not leak into the export.
	if _, err := f.days.Create(context.Background(), b, days.DayInput{Title: "Bob only", Date: "2026-01-01"}, testNow); err != nil {
		t.Fatal(err)
	}
	data := f.export(t, a)

	p, res := f.importData(t, b, data)
	if p.New != 4 || p.Skipped != 0 || len(p.Errors) != 0 || p.Attachments != 3 || p.BareCSV {
		t.Fatalf("preview = %+v", p)
	}
	if strings.Join(p.NewCollections, ",") != "Trips" || strings.Join(p.NewTags, ",") != `a|b\c,Fun` {
		t.Fatalf("new groups = %v / %v", p.NewCollections, p.NewTags)
	}
	if res.Imported != 4 || res.Attachments != 3 {
		t.Fatalf("result = %+v", res)
	}

	want, wantUsed := f.snapshot(t, a)
	got, gotUsed := f.snapshot(t, b)
	got = without(got, "Bob only")
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Fatalf("round trip mismatch\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	if wantUsed != gotUsed || wantUsed == 0 {
		t.Fatalf("used_bytes: alice %d bob %d", wantUsed, gotUsed)
	}
	// Bob's imported attachments share Alice's identical content on disk.
	if n := blobFiles(t, f.root); n != 3 {
		t.Fatalf("media blob files = %d, want 3 shared by both users (no leftover temp files)", n)
	}
	if _, err := os.Stat(filepath.Join(f.root, "media", fmt.Sprint(b))); !os.IsNotExist(err) {
		t.Fatalf("imported media written to a per-user directory: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.root, "tmp")); len(entries) != 0 {
		t.Fatalf("staged upload not removed: %d files", len(entries))
	}
	// Re-exporting the imported copy yields the same CSV apart from storage names.
	if normalizeCSV(t, f.export(t, b), "Bob only") != normalizeCSV(t, data, "") {
		t.Fatal("re-export of imported data differs")
	}
}

// blobFiles counts every regular file under media/blobs, including temporary ones.
func blobFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(root, "media", "blobs"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func without(lines []string, title string) []string {
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, fmt.Sprintf("%q ", title)) {
			out = append(out, l)
		}
	}
	return out
}

func readCSV(t *testing.T, data []byte) (string, [][]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "days.csv" {
			rc, _ := f.Open()
			raw, _ := io.ReadAll(rc)
			rc.Close()
			recs, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte(utf8BOM)))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			return string(raw), recs
		}
	}
	t.Fatal("no days.csv")
	return "", nil
}

func normalizeCSV(t *testing.T, data []byte, skipTitle string) string {
	_, recs := readCSV(t, data)
	var lines []string
	for _, r := range recs {
		if r[0] == skipTitle {
			continue
		}
		r[10] = uuidPrefix.ReplaceAllString(strings.TrimPrefix(r[10], "media/"), "")
		var att []string
		for _, p := range splitList(r[11]) {
			att = append(att, uuidPrefix.ReplaceAllString(strings.TrimPrefix(p, "media/"), ""))
		}
		r[11] = strings.Join(att, "|")
		lines = append(lines, strings.Join(r, "\x1f"))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestExportFormatAndCSVInjection(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "alice")
	f.seed(t, a)
	var archivedID int64
	if err := f.db.QueryRow(`SELECT id FROM days WHERE owner_id = ? AND title = 'Archived thing'`, a).Scan(&archivedID); err != nil {
		t.Fatal(err)
	}
	// Characters that are unsafe in zip paths are replaced in the exported name.
	if _, err := f.media.Upload(context.Background(), a, archivedID, "b*c|d?.txt", strings.NewReader("weird name")); err != nil {
		t.Fatal(err)
	}
	data := f.export(t, a)
	raw, recs := readCSV(t, data)
	if !strings.HasPrefix(raw, utf8BOM) || !strings.Contains(raw, "\r\n") {
		t.Fatal("days.csv must start with a UTF-8 BOM and use CRLF")
	}
	if strings.Join(recs[0], ",") != strings.Join(Columns, ",") {
		t.Fatalf("header = %v", recs[0])
	}
	byTitle := map[string][]string{}
	for _, r := range recs[1:] {
		byTitle[r[0]] = r
		for _, cell := range r {
			if cell != "" && isFormulaTrigger(cell[0]) {
				t.Errorf("unescaped formula cell %q", cell)
			}
		}
	}
	if r := byTitle[`'=HYPERLINK("http://evil")`]; r == nil || r[6] != "'@SUM(A1)" || r[3] != "yearly" || r[4] != "Default" {
		t.Fatalf("formula row = %v", r)
	}
	if r := byTitle["''=already quoted"]; r == nil || r[6] != "'-1+2" {
		t.Fatalf("pre-quoted row = %v (all titles: %v)", r, keys(byTitle))
	}
	paris := byTitle["Paris"]
	if paris == nil || paris[2] != "08:30" || paris[4] != "Trips" || paris[5] != `a\|b\\c|Fun` ||
		!strings.HasSuffix(paris[10], "_photo 1.png") || len(splitList(paris[11])) != 2 {
		t.Fatalf("paris row = %v", paris)
	}
	if r := byTitle["Archived thing"]; r == nil || r[7] != testNow.Add(-time.Hour).Format(time.RFC3339) || r[12] != "true" ||
		!strings.HasSuffix(r[11], "_b_c_d_.txt") {
		t.Fatalf("archived row = %v", r)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, zf := range zr.File {
		if zf.Name != "days.csv" && !strings.HasPrefix(zf.Name, "media/") {
			t.Errorf("unexpected entry %q", zf.Name)
		}
	}
	if name := ExportFilename("ali/ce", testNow); name != "pandadays-export-ali_ce-"+testNow.Format(time.DateOnly)+".zip" {
		t.Fatalf("filename = %q", name)
	}
}

func keys(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCellEscaping(t *testing.T) {
	for _, s := range []string{"", "plain", "=1+1", "+x", "-x", "@x", "\tx", "\rx", "'", "'=x", "''+x", "'plain", "x=1"} {
		esc := escapeCell(s)
		if esc != "" && isFormulaTrigger(esc[0]) {
			t.Errorf("escapeCell(%q) = %q still starts with a trigger", s, esc)
		}
		if got := unescapeCell(esc); got != s {
			t.Errorf("unescape(escape(%q)) = %q", s, got)
		}
	}
	for _, items := range [][]string{{"a", "b"}, {`a|b`, `c\d`, `e\`}, {"x"}} {
		if got := splitList(joinList(items)); strings.Join(got, "\x00") != strings.Join(items, "\x00") {
			t.Errorf("split(join(%q)) = %q", items, got)
		}
	}
}

func TestImportSkipsDuplicates(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "alice"), f.user(t, "bob")
	f.seed(t, a)
	data := f.export(t, a)
	if _, res := f.importData(t, b, data); res.Imported != 4 {
		t.Fatalf("first import = %+v", res)
	}
	before, used := f.snapshot(t, b)
	p, res := f.importData(t, b, data)
	if p.New != 0 || p.Skipped != 4 || p.CanImport() || res.Imported != 0 || res.Skipped != 4 {
		t.Fatalf("second import preview=%+v result=%+v", p, res)
	}
	after, usedAfter := f.snapshot(t, b)
	if strings.Join(before, "\n") != strings.Join(after, "\n") || used != usedAfter {
		t.Fatal("duplicate import changed data")
	}

	// Within one file the collection matches case-insensitively, the title exactly.
	csvData := "title,date,collection\nSame,2026-01-01,Trips\nSame,2026-01-01,trips\nSame,2026-01-01,\nsame,2026-01-01,Trips\n"
	p, res = f.importData(t, b, []byte(csvData))
	if p.New != 3 || p.Skipped != 1 || len(p.NewCollections) != 0 || res.Imported != 3 {
		t.Fatalf("in-file duplicates preview=%+v result=%+v", p, res)
	}
}

func TestImportBareCSVAndRowErrors(t *testing.T) {
	f := newFixture(t)
	b := f.user(t, "bob")
	csvData := utf8BOM + "title,date,time,repeat,collection,tags,comment,archived,style_color,style_icon,style_bg,attachments\r\n" +
		"Good,2026-02-03,,,New Coll,x|y|Default,'=1+1,true,,,,media/missing.png\r\n" +
		"Bad date,2026-02-30,,,,,,,,,,\r\n" +
		"Bad archived,2026-02-03,,,,,,someday,,,,\r\n" +
		"Short,2026-02-03\r\n" +
		"Long coll,2026-02-03,,,," + strings.Repeat("c", 101) + ",,,,,,\r\n" +
		"Default coll,2026-02-04,,weekly,默认,,,,,,,\r\n"
	p, res := f.importData(t, b, []byte(csvData))
	if !p.BareCSV || p.New != 2 || len(p.Errors) != 4 || res.Imported != 2 || res.Errors != 4 {
		t.Fatalf("preview=%+v result=%+v", p, res)
	}
	wantErrs := map[int]error{3: days.ErrDateInvalid, 4: ErrArchivedInvalid, 5: ErrFieldCount, 6: ErrTagName}
	for _, e := range p.Errors {
		if want := wantErrs[e.Line]; want == nil || !errors.Is(e.Err, want) {
			t.Errorf("line %d: %v", e.Line, e.Err)
		}
	}
	var comment string
	var archived sql.NullInt64
	var coll string
	if err := f.db.QueryRow(`SELECT d.comment, d.archived_at, c.name FROM days d JOIN collections c ON c.id = d.collection_id
		WHERE d.owner_id = ? AND d.title = 'Good'`, b).Scan(&comment, &archived, &coll); err != nil {
		t.Fatal(err)
	}
	if comment != "=1+1" || archived.Int64 != testNow.Unix() || coll != "New Coll" {
		t.Fatalf("good row = %q %v %q", comment, archived, coll)
	}
	var tagCount, defaultColl int
	f.db.QueryRow(`SELECT COUNT(*) FROM day_tags dt JOIN days d ON d.id = dt.day_id WHERE d.title = 'Good'`).Scan(&tagCount)
	f.db.QueryRow(`SELECT c.is_reserved FROM days d JOIN collections c ON c.id = d.collection_id WHERE d.title = 'Default coll'`).Scan(&defaultColl)
	if tagCount != 3 || defaultColl != 1 {
		t.Fatalf("tags=%d default collection=%d", tagCount, defaultColl)
	}
}

func TestImportArchiveLive(t *testing.T) {
	f := newFixture(t)
	b := f.user(t, "bob")
	csvData := "title,date,archived,archive_live\n" +
		"Live,2026-02-03,true,true\n" +
		"Paused,2026-02-03,true,\n" +
		"Active,2026-02-03,,true\n" +
		"Bad,2026-02-03,true,maybe\n"
	p, res := f.importData(t, b, []byte(csvData))
	if res.Imported != 3 || len(p.Errors) != 1 || p.Errors[0].Line != 5 || !errors.Is(p.Errors[0].Err, ErrArchiveLive) {
		t.Fatalf("preview=%+v result=%+v", p, res)
	}
	for title, want := range map[string]bool{"Live": true, "Paused": false, "Active": false} {
		var live bool
		if err := f.db.QueryRow(`SELECT archive_live FROM days WHERE owner_id = ? AND title = ?`, b, title).Scan(&live); err != nil || live != want {
			t.Errorf("%s: archive_live=%v err=%v, want %v", title, live, err, want)
		}
	}
}

func TestImportQuotasAndCardLimit(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "alice"), f.user(t, "bob")
	f.seed(t, a)
	data := f.export(t, a)

	if _, err := f.db.Exec(`UPDATE users SET quota_days = 3 WHERE id = ?`, b); err != nil {
		t.Fatal(err)
	}
	token := f.stage(t, b, data)
	p, err := f.svc.Preview(context.Background(), b, token, testNow)
	if err != nil || !p.DaysExceeded || p.CanImport() {
		t.Fatalf("day quota preview=%+v err=%v", p, err)
	}
	if _, err := f.svc.Import(context.Background(), b, token, testNow); !errors.Is(err, ErrQuota) {
		t.Fatalf("day quota import err = %v", err)
	}

	if _, err := f.db.Exec(`UPDATE users SET quota_days = NULL, quota_bytes = 10 WHERE id = ?`, b); err != nil {
		t.Fatal(err)
	}
	p, err = f.svc.Preview(context.Background(), b, token, testNow)
	if err != nil || !p.BytesExceeded || p.CanImport() {
		t.Fatalf("byte quota preview=%+v err=%v", p, err)
	}
	if _, err := f.svc.Import(context.Background(), b, token, testNow); !errors.Is(err, ErrQuota) {
		t.Fatalf("byte quota import err = %v", err)
	}

	// Paris carries 119 bytes of attachments; a 50-byte card limit rejects only that row.
	if _, err := f.db.Exec(`UPDATE users SET quota_bytes = NULL, max_card_bytes = 50 WHERE id = ?`, b); err != nil {
		t.Fatal(err)
	}
	p, res := f.importData(t, b, data)
	if p.New != 3 || len(p.Errors) != 1 || !errors.Is(p.Errors[0].Err, ErrCardLimit) || res.Imported != 3 {
		t.Fatalf("card limit preview=%+v result=%+v", p, res)
	}
	var count int
	f.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, b).Scan(&count)
	if count != 3 {
		t.Fatalf("days = %d", count)
	}
}

type entry struct {
	name    string
	body    string
	symlink bool
}

func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.symlink {
			h.SetMode(os.ModeSymlink | 0o777)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, e.body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportRejectsMaliciousArchives(t *testing.T) {
	f := newFixture(t)
	b := f.user(t, "bob")
	f.svc.Limits = Limits{MaxEntries: 5, MaxCSVBytes: 1 << 10, MaxMediaBytes: 1 << 10}
	csvRow := entry{name: "days.csv", body: "title,date,attachments\nX,2026-01-01,media/a.txt\n"}
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"parent traversal", makeZip(t, csvRow, entry{name: "../evil.txt", body: "x"}), ErrUnsafeArchive},
		{"nested traversal", makeZip(t, csvRow, entry{name: "media/../../evil.txt", body: "x"}), ErrUnsafeArchive},
		{"absolute path", makeZip(t, csvRow, entry{name: "/etc/evil", body: "x"}), ErrUnsafeArchive},
		{"drive letter", makeZip(t, csvRow, entry{name: "C:/evil", body: "x"}), ErrUnsafeArchive},
		{"backslash", makeZip(t, csvRow, entry{name: `media\..\..\evil`, body: "x"}), ErrUnsafeArchive},
		{"symlink", makeZip(t, csvRow, entry{name: "media/a.txt", body: "/etc/passwd", symlink: true}), ErrUnsafeArchive},
		{"duplicate entry", makeZip(t, csvRow, entry{name: "media/a.txt", body: "1"}, entry{name: "media/a.txt", body: "2"}), ErrUnsafeArchive},
		{"too many entries", makeZip(t, csvRow, entry{name: "media/1"}, entry{name: "media/2"}, entry{name: "media/3"},
			entry{name: "media/4"}, entry{name: "media/5"}), ErrTooManyEntries},
		{"media too large", makeZip(t, csvRow, entry{name: "media/a.txt", body: strings.Repeat("x", 2<<10)}), ErrArchiveTooLarge},
		{"csv too large", makeZip(t, entry{name: "days.csv", body: "title,date\n" + strings.Repeat("x", 2<<10)}), ErrArchiveTooLarge},
		{"missing csv", makeZip(t, entry{name: "media/a.txt", body: "x"}), ErrMissingCSV},
		{"corrupt zip", append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 64)...), ErrInvalidArchive},
		{"bad header", makeZip(t, entry{name: "days.csv", body: "name,when\nX,2026-01-01\n"}), ErrInvalidCSV},
		{"bare quote", []byte("title,date\n\"unterminated,2026-01-01\n"), ErrInvalidCSV},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := f.stage(t, b, tc.data)
			if _, err := f.svc.Preview(context.Background(), b, token, testNow); !errors.Is(err, tc.want) {
				t.Fatalf("preview err = %v, want %v", err, tc.want)
			}
			if _, err := f.svc.Import(context.Background(), b, token, testNow); !errors.Is(err, tc.want) {
				t.Fatalf("import err = %v, want %v", err, tc.want)
			}
		})
	}
	var count int
	f.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, b).Scan(&count)
	if count != 0 {
		t.Fatalf("malicious archives imported %d days", count)
	}
	for _, p := range []string{filepath.Join(f.root, "evil.txt"), filepath.Join(f.root, "media", "evil.txt")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file written outside media: %s", p)
		}
	}

	// A safe archive with the same limits still imports its media.
	p, res := f.importData(t, b, makeZip(t, csvRow, entry{name: "media/", body: ""}, entry{name: "media/a.txt", body: "ok"}))
	if p.New != 1 || res.Imported != 1 || res.Attachments != 1 {
		t.Fatalf("safe archive preview=%+v result=%+v", p, res)
	}
	var filename string
	f.db.QueryRow(`SELECT filename FROM attachments WHERE owner_id = ?`, b).Scan(&filename)
	if filename != "a.txt" {
		t.Fatalf("filename = %q", filename)
	}
}

func TestStageLimitsAndOwnership(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "alice"), f.user(t, "bob")
	if _, err := f.svc.Stage(a, strings.NewReader("123456"), 5, testNow); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("oversized upload err = %v", err)
	}
	token, err := f.svc.Stage(a, strings.NewReader("title,date\nA,2026-01-01\n"), 1<<10, testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{token, "../" + token, "", "zz"} {
		if _, err := f.svc.Preview(context.Background(), b, tok, testNow); !errors.Is(err, ErrStagedNotFound) {
			t.Fatalf("bob using %q: err = %v", tok, err)
		}
		if _, err := f.svc.Import(context.Background(), b, tok, testNow); !errors.Is(err, ErrStagedNotFound) {
			t.Fatalf("bob importing %q: err = %v", tok, err)
		}
	}
	if _, err := f.svc.Preview(context.Background(), a, token, time.Now().Add(StagedTTL+time.Minute)); !errors.Is(err, ErrStagedNotFound) {
		t.Fatalf("expired upload err = %v", err)
	}
	if _, err := f.svc.Import(context.Background(), a, token, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Import(context.Background(), a, token, testNow); !errors.Is(err, ErrStagedNotFound) {
		t.Fatalf("reused token err = %v", err)
	}

	if _, err := f.db.Exec(`UPDATE users SET quota_bytes = 100, used_bytes = 40 WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if limit, err := f.svc.UploadLimit(context.Background(), a); err != nil || limit != 60+DefaultLimits.MaxCSVBytes {
		t.Fatalf("upload limit = %d, %v", limit, err)
	}
}
