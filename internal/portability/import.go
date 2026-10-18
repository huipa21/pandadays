package portability

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pandadays/internal/days"
	"pandadays/internal/media"
)

// Row-level problems; such rows are reported in the preview and not imported.
var (
	ErrFieldCount        = errors.New("row has the wrong number of fields")
	ErrArchivedInvalid   = errors.New("archived must be empty, true/false or an RFC 3339 time")
	ErrCollectionName    = errors.New("collection name is invalid")
	ErrTagName           = errors.New("tag name is invalid")
	ErrAttachmentMissing = errors.New("attachment is not in the archive")
	ErrCardLimit         = errors.New("attachments exceed the per-day limit")
)

// ErrQuota means the import would exceed the day or storage quota.
var ErrQuota = errors.New("import exceeds quota")

// RowError reports a rejected days.csv row; Line is 1-based.
type RowError struct {
	Line int
	Err  error
}

// Preview summarizes what confirming an import would do.
type Preview struct {
	New, Skipped   int
	Errors         []RowError
	NewCollections []string
	NewTags        []string
	Attachments    int
	Bytes          int64
	// BareCSV imports ignore the attachments and style_bg columns.
	BareCSV bool

	DaysUsed, DaysQuota   int64
	BytesUsed, BytesQuota int64
	DaysExceeded          bool
	BytesExceeded         bool
}

// CanImport reports whether confirming would import anything.
func (p Preview) CanImport() bool { return p.New > 0 && !p.DaysExceeded && !p.BytesExceeded }

// Result summarizes a completed import.
type Result struct {
	Imported, Skipped, Errors, Attachments int
}

type row struct {
	line        int
	in          days.DayInput
	collection  string
	tags        []string
	archivedAt  *time.Time
	attachments []string
	styleBg     string
}

func parseRows(data []byte, bare bool, now time.Time) ([]row, []RowError, error) {
	data = bytes.TrimPrefix(data, []byte(utf8BOM))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, nil, ErrInvalidCSV
	}
	col := map[string]int{}
	for i, name := range header {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, dup := col[name]; !dup {
			col[name] = i
		}
	}
	if _, ok := col["title"]; !ok {
		return nil, nil, ErrInvalidCSV
	}
	if _, ok := col["date"]; !ok {
		return nil, nil, ErrInvalidCSV
	}
	var rows []row
	var rowErrs []RowError
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidCSV, err)
		}
		line, _ := r.FieldPos(0)
		if len(rec) != len(header) {
			rowErrs = append(rowErrs, RowError{Line: line, Err: ErrFieldCount})
			continue
		}
		cell := func(name string) string {
			if i, ok := col[name]; ok {
				return unescapeCell(rec[i])
			}
			return ""
		}
		in, err := days.NormalizeDayInput(days.DayInput{
			Title: cell("title"), Date: cell("date"), Time: cell("time"), Repeat: days.Repeat(cell("repeat")),
			Comment: cell("comment"), StyleColor: cell("style_color"), StyleIcon: cell("style_icon"),
		})
		if err != nil {
			rowErrs = append(rowErrs, RowError{Line: line, Err: err})
			continue
		}
		archivedAt, err := parseArchived(cell("archived"), now)
		if err != nil {
			rowErrs = append(rowErrs, RowError{Line: line, Err: err})
			continue
		}
		rw := row{line: line, in: in, collection: strings.TrimSpace(cell("collection")),
			tags: splitList(cell("tags")), archivedAt: archivedAt}
		if !bare {
			rw.attachments = unique(splitList(cell("attachments")))
			rw.styleBg = strings.TrimSpace(cell("style_bg"))
			if rw.styleBg != "" && !contains(rw.attachments, rw.styleBg) {
				rowErrs = append(rowErrs, RowError{Line: line, Err: ErrAttachmentMissing})
				continue
			}
		}
		rows = append(rows, rw)
	}
	return rows, rowErrs, nil
}

func parseArchived(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "false", "no", "0":
		return nil, nil
	case "true", "yes", "1":
		t := time.Unix(now.Unix(), 0).UTC()
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, ErrArchivedInvalid
	}
	t = time.Unix(t.Unix(), 0).UTC()
	return &t, nil
}

func unique(items []string) []string {
	var out []string
	for _, item := range items {
		if !contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func contains(items []string, s string) bool {
	for _, item := range items {
		if item == s {
			return true
		}
	}
	return false
}

// groupIndex resolves collection or tag names with Unicode case folding, the
// same rule the days package uses for uniqueness.
type groupIndex struct {
	defaultID int64
	names     []string
	ids       []int64
	pending   []string // names to create, in first-seen order
}

func loadGroups(ctx context.Context, q queryer, table string, ownerID int64) (*groupIndex, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, is_reserved FROM `+table+` WHERE owner_id = ? ORDER BY id`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", table, err)
	}
	defer rows.Close()
	g := &groupIndex{}
	for rows.Next() {
		var id int64
		var name string
		var reserved bool
		if err := rows.Scan(&id, &name, &reserved); err != nil {
			return nil, fmt.Errorf("load %s: %w", table, err)
		}
		if reserved {
			g.defaultID = id
			continue
		}
		g.names = append(g.names, name)
		g.ids = append(g.ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load %s: %w", table, err)
	}
	if g.defaultID == 0 {
		return nil, fmt.Errorf("owner %d has no reserved default in %s", ownerID, table)
	}
	return g, nil
}

// groupRef is either an existing group ID or the canonical name of a group to create.
type groupRef struct {
	id   int64
	name string
}

func (r groupRef) key() string {
	if r.id != 0 {
		return "id:" + strconv.FormatInt(r.id, 10)
	}
	return "new:" + strings.ToLower(r.name)
}

// resolve maps a name to a group; empty and reserved names select the default.
func (g *groupIndex) resolve(name string) (groupRef, error) {
	if name == "" || days.IsReservedName(name) {
		return groupRef{id: g.defaultID}, nil
	}
	for i, existing := range g.names {
		if strings.EqualFold(existing, name) {
			return groupRef{id: g.ids[i]}, nil
		}
	}
	for _, p := range g.pending {
		if strings.EqualFold(p, name) {
			return groupRef{name: p}, nil
		}
	}
	if err := days.ValidateGroupName(name); err != nil {
		return groupRef{}, err
	}
	return groupRef{name: name}, nil
}

func (g *groupIndex) accept(ref groupRef) {
	if ref.id != 0 {
		return
	}
	for _, p := range g.pending {
		if strings.EqualFold(p, ref.name) {
			return
		}
	}
	g.pending = append(g.pending, ref.name)
}

type plannedDay struct {
	row         *row
	collection  groupRef
	tags        []groupRef
	attachments []*zip.File
}

type plan struct {
	preview     Preview
	days        []plannedDay
	collections *groupIndex
	tags        *groupIndex
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// makePlan matches parsed rows against the owner's current data: duplicates by
// (title, date, collection) are skipped, and quotas are checked.
func makePlan(ctx context.Context, q queryer, ownerID int64, src *source, rows []row, rowErrs []RowError) (*plan, error) {
	p := &plan{preview: Preview{Skipped: 0, Errors: append([]RowError(nil), rowErrs...), BareCSV: src.bare}}
	var err error
	if p.collections, err = loadGroups(ctx, q, "collections", ownerID); err != nil {
		return nil, err
	}
	if p.tags, err = loadGroups(ctx, q, "tags", ownerID); err != nil {
		return nil, err
	}
	var maxCard int64
	if err := q.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM days WHERE owner_id = u.id),
			COALESCE(u.quota_days, (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_quota_days'), 1000),
			u.used_bytes,
			COALESCE(u.quota_bytes, (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_quota_bytes'), 0),
			COALESCE(u.max_card_bytes, (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_max_card_bytes'), 0)
		FROM users u WHERE u.id = ?`, ownerID).Scan(&p.preview.DaysUsed, &p.preview.DaysQuota,
		&p.preview.BytesUsed, &p.preview.BytesQuota, &maxCard); err != nil {
		return nil, fmt.Errorf("read import quotas: %w", err)
	}

	existing := map[string]bool{}
	dayRows, err := q.QueryContext(ctx, `SELECT title, date, collection_id FROM days WHERE owner_id = ?`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("load existing days: %w", err)
	}
	for dayRows.Next() {
		var title, date string
		var coll int64
		if err := dayRows.Scan(&title, &date, &coll); err != nil {
			dayRows.Close()
			return nil, fmt.Errorf("load existing days: %w", err)
		}
		existing[dayKey(title, date, groupRef{id: coll})] = true
	}
	if err := dayRows.Close(); err != nil {
		return nil, fmt.Errorf("load existing days: %w", err)
	}
	if err := dayRows.Err(); err != nil {
		return nil, fmt.Errorf("load existing days: %w", err)
	}

	for i := range rows {
		rw := &rows[i]
		fail := func(err error) { p.preview.Errors = append(p.preview.Errors, RowError{Line: rw.line, Err: err}) }
		coll, err := p.collections.resolve(rw.collection)
		if err != nil {
			fail(fmt.Errorf("%w: %w", ErrCollectionName, err))
			continue
		}
		key := dayKey(rw.in.Title, rw.in.Date, coll)
		if existing[key] {
			p.preview.Skipped++
			continue
		}
		var tags []groupRef
		var tagErr error
		seen := map[string]bool{}
		for _, name := range rw.tags {
			ref, err := p.tags.resolve(name)
			if err != nil {
				tagErr = fmt.Errorf("%w: %w", ErrTagName, err)
				break
			}
			if !seen[ref.key()] {
				seen[ref.key()] = true
				tags = append(tags, ref)
			}
		}
		if tagErr != nil {
			fail(tagErr)
			continue
		}
		if len(tags) == 0 {
			tags = []groupRef{{id: p.tags.defaultID}}
		}
		var files []*zip.File
		var size uint64
		for _, path := range rw.attachments {
			f := src.files[path]
			if f == nil {
				files = nil
				break
			}
			files = append(files, f)
			size += f.UncompressedSize64
		}
		if len(files) != len(rw.attachments) {
			fail(ErrAttachmentMissing)
			continue
		}
		if size > uint64(max(maxCard, 0)) {
			fail(ErrCardLimit)
			continue
		}
		existing[key] = true
		p.collections.accept(coll)
		for _, t := range tags {
			p.tags.accept(t)
		}
		p.days = append(p.days, plannedDay{row: rw, collection: coll, tags: tags, attachments: files})
		p.preview.Attachments += len(files)
		p.preview.Bytes += int64(size)
	}
	p.preview.New = len(p.days)
	p.preview.NewCollections = p.collections.pending
	p.preview.NewTags = p.tags.pending
	p.preview.DaysExceeded = p.preview.New > 0 && p.preview.DaysUsed+int64(p.preview.New) > p.preview.DaysQuota
	p.preview.BytesExceeded = p.preview.Bytes > 0 && p.preview.BytesUsed+p.preview.Bytes > p.preview.BytesQuota
	return p, nil
}

func dayKey(title, date string, coll groupRef) string {
	return title + "\x00" + date + "\x00" + coll.key()
}

func (s *Service) load(ownerID int64, token string, now time.Time) (*source, []row, []RowError, error) {
	path, err := s.staged(ownerID, token, now)
	if err != nil {
		return nil, nil, nil, err
	}
	src, err := openSource(path, s.Limits)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, rowErrs, err := parseRows(src.csv, src.bare, now)
	if err != nil {
		src.Close()
		return nil, nil, nil, err
	}
	return src, rows, rowErrs, nil
}

// Preview analyses a staged upload without changing anything. Archive-level
// problems (unsafe paths, size, malformed CSV) are returned as errors.
func (s *Service) Preview(ctx context.Context, ownerID int64, token string, now time.Time) (Preview, error) {
	src, rows, rowErrs, err := s.load(ownerID, token, now)
	if err != nil {
		return Preview{}, err
	}
	defer src.Close()
	p, err := makePlan(ctx, s.db, ownerID, src, rows, rowErrs)
	if err != nil {
		return Preview{}, err
	}
	return p.preview, nil
}

// extracted is an attachment copied out of the archive into the owner's media
// directory under a temporary name.
type extracted struct {
	temp, final string
	kind, mime  string
	size        int64
}

// extractor hands out one extracted copy per attachment occurrence.
type extractor struct {
	dir  string
	pool map[*zip.File][]*extracted
	all  []*extracted
}

func (x *extractor) extract(f *zip.File) (*extracted, error) {
	temp, err := os.CreateTemp(x.dir, ".import-*")
	if err != nil {
		return nil, fmt.Errorf("create import file: %w", err)
	}
	e := &extracted{temp: temp.Name(), size: int64(f.UncompressedSize64)}
	x.all = append(x.all, e)
	rc, err := f.Open()
	if err != nil {
		temp.Close()
		return nil, ErrInvalidArchive
	}
	defer rc.Close()
	var prefix bytes.Buffer
	n, err := io.Copy(temp, io.TeeReader(io.LimitReader(rc, e.size+1), &limitedBuffer{&prefix, 512}))
	if closeErr := temp.Close(); err == nil && closeErr != nil {
		return nil, fmt.Errorf("write import file: %w", closeErr)
	}
	if err != nil || n != e.size {
		return nil, ErrInvalidArchive
	}
	e.mime, e.kind = media.Sniff(prefix.Bytes())
	return e, nil
}

func (x *extractor) prepare(files []*zip.File) error {
	for _, f := range files {
		e, err := x.extract(f)
		if err != nil {
			return err
		}
		x.pool[f] = append(x.pool[f], e)
	}
	return nil
}

func (x *extractor) take(f *zip.File) (*extracted, error) {
	if list := x.pool[f]; len(list) > 0 {
		x.pool[f] = list[1:]
		return list[0], nil
	}
	return x.extract(f)
}

// cleanup removes temporary files and, unless committed, the final files.
func (x *extractor) cleanup(committed bool) {
	for _, e := range x.all {
		os.Remove(e.temp)
		if !committed && e.final != "" {
			os.Remove(e.final)
		}
	}
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// Import applies a staged upload in merge mode and removes it. Media is
// extracted to temporary files first; the database changes run in one
// transaction and files are moved into place only if it commits.
func (s *Service) Import(ctx context.Context, ownerID int64, token string, now time.Time) (Result, error) {
	src, rows, rowErrs, err := s.load(ownerID, token, now)
	if err != nil {
		return Result{}, err
	}
	defer src.Close()

	dir := filepath.Join(s.mediaRoot, strconv.FormatInt(ownerID, 10))
	x := &extractor{dir: dir, pool: map[*zip.File][]*extracted{}}
	committed := false
	defer func() { x.cleanup(committed) }()

	// Extract outside the write transaction so other writers are not blocked;
	// the plan is recomputed inside it.
	first, err := makePlan(ctx, s.db, ownerID, src, rows, rowErrs)
	if err != nil {
		return Result{}, err
	}
	if !first.preview.CanImport() {
		if first.preview.DaysExceeded || first.preview.BytesExceeded {
			return Result{}, ErrQuota
		}
		src.Close()
		s.Discard(ownerID, token)
		return Result{Skipped: first.preview.Skipped, Errors: len(first.preview.Errors)}, nil
	}
	if first.preview.Attachments > 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Result{}, fmt.Errorf("create user media directory: %w", err)
		}
		for _, d := range first.days {
			if err := x.prepare(d.attachments); err != nil {
				return Result{}, err
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin import: %w", err)
	}
	defer tx.Rollback()
	p, err := makePlan(ctx, tx, ownerID, src, rows, rowErrs)
	if err != nil {
		return Result{}, err
	}
	if p.preview.DaysExceeded || p.preview.BytesExceeded {
		return Result{}, ErrQuota
	}
	res := Result{Imported: p.preview.New, Skipped: p.preview.Skipped, Errors: len(p.preview.Errors),
		Attachments: p.preview.Attachments}
	collIDs, err := createGroups(ctx, tx, "collections", ownerID, p.collections.pending, now)
	if err != nil {
		return Result{}, err
	}
	tagIDs, err := createGroups(ctx, tx, "tags", ownerID, p.tags.pending, now)
	if err != nil {
		return Result{}, err
	}
	var total int64
	for _, d := range p.days {
		n, err := s.insertDay(ctx, tx, x, ownerID, d, collIDs, tagIDs, now)
		if err != nil {
			return Result{}, err
		}
		total += n
	}
	if total > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?`, total, ownerID); err != nil {
			return Result{}, fmt.Errorf("update storage usage: %w", err)
		}
	}
	for _, e := range x.all {
		if e.final == "" {
			continue
		}
		if err := os.Rename(e.temp, e.final); err != nil {
			return Result{}, fmt.Errorf("store imported attachment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit import: %w", err)
	}
	committed = true
	// Windows cannot remove a file that is still open.
	src.Close()
	s.Discard(ownerID, token)
	return res, nil
}

func createGroups(ctx context.Context, tx *sql.Tx, table string, ownerID int64, names []string, now time.Time) (map[string]int64, error) {
	ids := map[string]int64{}
	for _, name := range names {
		res, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (owner_id, name, created_at) VALUES (?, ?, ?)`,
			ownerID, name, now.Unix())
		if err != nil {
			return nil, fmt.Errorf("create imported %s: %w", table, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("create imported %s: %w", table, err)
		}
		ids[groupRef{name: name}.key()] = id
	}
	return ids, nil
}

func refID(ref groupRef, created map[string]int64) int64 {
	if ref.id != 0 {
		return ref.id
	}
	return created[ref.key()]
}

var uuidPrefix = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}_`)

// attachmentFilename recovers the display name from media/<uuid>_<filename>.
func attachmentFilename(path string) string {
	name := strings.TrimPrefix(path, mediaPrefix)
	if stripped := uuidPrefix.ReplaceAllString(name, ""); stripped != "" {
		name = stripped
	}
	return media.SafeFilename(name)
}

// insertDay inserts one planned day with its tags and attachments and returns
// the attachment bytes added.
func (s *Service) insertDay(ctx context.Context, tx *sql.Tx, x *extractor, ownerID int64, d plannedDay,
	collIDs, tagIDs map[string]int64, now time.Time) (int64, error) {
	in := d.row.in
	var archived any
	if d.row.archivedAt != nil {
		archived = d.row.archivedAt.Unix()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO days (owner_id, collection_id, title, date, time, repeat, comment,
		style_color, style_icon, archived_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ownerID, refID(d.collection, collIDs), in.Title, in.Date, nullString(in.Time), string(in.Repeat),
		nullString(in.Comment), nullString(in.StyleColor), nullString(in.StyleIcon), archived, now.Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("import day: %w", err)
	}
	dayID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("import day: %w", err)
	}
	for _, t := range d.tags {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO day_tags (day_id, tag_id) VALUES (?, ?)`,
			dayID, refID(t, tagIDs)); err != nil {
			return 0, fmt.Errorf("import day tags: %w", err)
		}
	}
	var total int64
	for i, f := range d.attachments {
		e, err := x.take(f)
		if err != nil {
			return 0, err
		}
		uuid, err := media.NewUUID()
		if err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO attachments (day_id, owner_id, uuid, kind, filename, mime, size, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, dayID, ownerID, uuid, e.kind, attachmentFilename(d.row.attachments[i]),
			e.mime, e.size, now.Unix())
		if err != nil {
			return 0, fmt.Errorf("import attachment: %w", err)
		}
		e.final = filepath.Join(x.dir, uuid)
		total += e.size
		if d.row.attachments[i] == d.row.styleBg && e.kind == "image" {
			attachmentID, err := res.LastInsertId()
			if err != nil {
				return 0, fmt.Errorf("import attachment: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE days SET style_bg_attachment_id = ? WHERE id = ? AND owner_id = ?`,
				attachmentID, dayID, ownerID); err != nil {
				return 0, fmt.Errorf("import background: %w", err)
			}
		}
	}
	return total, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
