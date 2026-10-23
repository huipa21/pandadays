package portability

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"pandadays/internal/days"
	"pandadays/internal/media"
)

// Service exports and imports one user's data. Every query is scoped by owner.
type Service struct {
	db     *sql.DB
	media  *media.Store
	tmpDir string
	Limits Limits
}

// Limits bound what an import may contain; the per-user and per-card storage
// quotas are enforced on top of them.
type Limits struct {
	MaxEntries    int
	MaxCSVBytes   int64
	MaxMediaBytes int64
}

var DefaultLimits = Limits{MaxEntries: 10000, MaxCSVBytes: 32 << 20, MaxMediaBytes: 16 << 30}

// New returns a service storing media in the content store under mediaRoot
// and staged uploads under tmpDir.
func New(db *sql.DB, mediaRoot, tmpDir string) *Service {
	return &Service{db: db, media: media.New(db, mediaRoot), tmpDir: tmpDir, Limits: DefaultLimits}
}

// UploadLimit is the largest import upload accepted for the owner: the
// remaining storage allowance plus room for days.csv.
func (s *Service) UploadLimit(ctx context.Context, ownerID int64) (int64, error) {
	var used, quota int64
	err := s.db.QueryRowContext(ctx, `SELECT u.used_bytes,
		COALESCE(u.quota_bytes, (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_quota_bytes'), 0)
		FROM users u WHERE u.id = ?`, ownerID).Scan(&used, &quota)
	if err != nil {
		return 0, fmt.Errorf("read import allowance: %w", err)
	}
	return min(max(quota-used, 0), s.Limits.MaxMediaBytes) + s.Limits.MaxCSVBytes, nil
}

// ExportFilename is the download name pandadays-export-<username>-<date>.zip.
func ExportFilename(username string, date time.Time) string {
	return "pandadays-export-" + archiveFilename(username, 64) + "-" + date.Format(time.DateOnly) + ".zip"
}

// archiveFilename makes a display name safe as a single zip path element on
// common platforms while keeping it readable.
func archiveFilename(name string, maxRunes int) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`/\:*?"<>|`, r):
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if utf8.RuneCountInString(name) > maxRunes {
		name = string([]rune(name)[:maxRunes])
	}
	if name == "" {
		return "file"
	}
	return name
}

type exportDay struct {
	id          int64
	record      []string
	bgID        int64
	tags        []string
	attachments []string
}

type exportFile struct {
	path    string
	src     string
	size    int64
	created time.Time
}

// Export writes the owner's days matching the context's privacy mode (not
// shared-with-me) and their attachments as a zip to w. Media files are checked
// before anything is written.
func (s *Service) Export(ctx context.Context, ownerID int64, now time.Time, w io.Writer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin export: %w", err)
	}
	defer tx.Rollback()

	list, byID, err := exportDays(ctx, tx, ownerID)
	if err != nil {
		return err
	}
	if err := exportTags(ctx, tx, ownerID, byID); err != nil {
		return err
	}
	files, err := s.exportAttachments(ctx, tx, ownerID, byID)
	if err != nil {
		return err
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("end export: %w", err)
	}
	for _, f := range files {
		info, err := os.Stat(f.src)
		if err != nil {
			return fmt.Errorf("export attachment %s: %w", f.path, err)
		}
		if info.Size() != f.size {
			return fmt.Errorf("export attachment %s: size %d does not match recorded %d", f.path, info.Size(), f.size)
		}
	}

	zw := zip.NewWriter(w)
	cw, err := zw.CreateHeader(&zip.FileHeader{Name: csvName, Method: zip.Deflate, Modified: now.UTC()})
	if err != nil {
		return fmt.Errorf("export days.csv: %w", err)
	}
	if _, err := io.WriteString(cw, utf8BOM); err != nil {
		return fmt.Errorf("export days.csv: %w", err)
	}
	csvw := csv.NewWriter(cw)
	csvw.UseCRLF = true
	if err := csvw.Write(Columns); err != nil {
		return fmt.Errorf("export days.csv: %w", err)
	}
	for _, d := range list {
		rec := d.record
		rec[5] = joinList(d.tags)
		rec[11] = joinList(d.attachments)
		for i := range rec {
			rec[i] = escapeCell(rec[i])
		}
		if err := csvw.Write(rec); err != nil {
			return fmt.Errorf("export days.csv: %w", err)
		}
	}
	csvw.Flush()
	if err := csvw.Error(); err != nil {
		return fmt.Errorf("export days.csv: %w", err)
	}
	for _, f := range files {
		if err := copyToZip(zw, f); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finish export: %w", err)
	}
	return nil
}

func exportDays(ctx context.Context, tx *sql.Tx, ownerID int64) ([]*exportDay, map[int64]*exportDay, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id, d.title, d.date, COALESCE(d.time, ''), d.repeat, c.name,
		COALESCE(d.comment, ''), d.archived_at, d.archive_live, COALESCE(d.style_color, ''), COALESCE(d.style_icon, ''),
		COALESCE(d.style_bg_attachment_id, 0), d.is_private, d.calendar
		FROM days d JOIN collections c ON c.id = d.collection_id AND c.owner_id = d.owner_id
		WHERE d.owner_id = ? AND d.is_private = ? ORDER BY d.date, d.id`, ownerID, days.IsPrivate(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("export days: %w", err)
	}
	defer rows.Close()
	var list []*exportDay
	byID := map[int64]*exportDay{}
	for rows.Next() {
		var (
			d                                                              exportDay
			title, date, clock, repeat, coll, comment, color, ic, calendar string
			archived                                                       sql.NullInt64
			live                                                           bool
			private                                                        bool
		)
		if err := rows.Scan(&d.id, &title, &date, &clock, &repeat, &coll, &comment, &archived, &live,
			&color, &ic, &d.bgID, &private, &calendar); err != nil {
			return nil, nil, fmt.Errorf("export days: %w", err)
		}
		archivedAt, archiveLive := "", ""
		if archived.Valid {
			archivedAt = time.Unix(archived.Int64, 0).UTC().Format(time.RFC3339)
			archiveLive = strconv.FormatBool(live)
		}
		d.record = []string{title, date, clock, repeat, coll, "", comment, archivedAt, color, ic, "", "", archiveLive,
			strconv.FormatBool(private), calendar}
		list = append(list, &d)
		byID[d.id] = &d
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("export days: %w", err)
	}
	return list, byID, nil
}

func exportTags(ctx context.Context, tx *sql.Tx, ownerID int64, byID map[int64]*exportDay) error {
	rows, err := tx.QueryContext(ctx, `SELECT dt.day_id, t.name FROM day_tags dt
		JOIN tags t ON t.id = dt.tag_id JOIN days d ON d.id = dt.day_id
		WHERE d.owner_id = ? AND t.owner_id = ? AND d.is_private = ?
		ORDER BY t.is_reserved DESC, t.name, t.id`, ownerID, ownerID, days.IsPrivate(ctx))
	if err != nil {
		return fmt.Errorf("export tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dayID int64
		var name string
		if err := rows.Scan(&dayID, &name); err != nil {
			return fmt.Errorf("export tags: %w", err)
		}
		if d := byID[dayID]; d != nil {
			d.tags = append(d.tags, name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export tags: %w", err)
	}
	return nil
}

func (s *Service) exportAttachments(ctx context.Context, tx *sql.Tx, ownerID int64, byID map[int64]*exportDay) ([]exportFile, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.day_id, a.uuid, COALESCE(a.sha256, ''), a.filename, a.size, a.created_at
		FROM attachments a JOIN days d ON d.id = a.day_id
		WHERE a.owner_id = ? AND d.owner_id = ? AND d.is_private = ? AND a.detached = 0
		ORDER BY a.created_at, a.id`, ownerID, ownerID, days.IsPrivate(ctx))
	if err != nil {
		return nil, fmt.Errorf("export attachments: %w", err)
	}
	defer rows.Close()
	var files []exportFile
	for rows.Next() {
		var id, dayID, size, created int64
		var uuid, sum, filename string
		if err := rows.Scan(&id, &dayID, &uuid, &sum, &filename, &size, &created); err != nil {
			return nil, fmt.Errorf("export attachments: %w", err)
		}
		d := byID[dayID]
		if d == nil {
			continue
		}
		p := mediaPrefix + archiveFilename(uuid, 64) + "_" + archiveFilename(filename, 120)
		d.attachments = append(d.attachments, p)
		if d.bgID == id {
			d.record[10] = p
		}
		files = append(files, exportFile{path: p, src: s.media.Path(ownerID, media.Attachment{UUID: uuid, SHA256: sum}), size: size,
			created: time.Unix(created, 0).UTC()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export attachments: %w", err)
	}
	return files, nil
}

func copyToZip(zw *zip.Writer, f exportFile) error {
	src, err := os.Open(f.src)
	if err != nil {
		return fmt.Errorf("export attachment %s: %w", f.path, err)
	}
	defer src.Close()
	// Media is usually compressed already, so it is stored as-is.
	dst, err := zw.CreateHeader(&zip.FileHeader{Name: f.path, Method: zip.Store, Modified: f.created})
	if err != nil {
		return fmt.Errorf("export attachment %s: %w", f.path, err)
	}
	n, err := io.Copy(dst, io.LimitReader(src, f.size))
	if err != nil {
		return fmt.Errorf("export attachment %s: %w", f.path, err)
	}
	if n != f.size {
		return fmt.Errorf("export attachment %s: file is shorter than recorded size", f.path)
	}
	return nil
}
