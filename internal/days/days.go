package days

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxTitleRunes   = 200
	MaxCommentRunes = 10000
	MaxIconRunes    = 16
	// fallbackQuotaDays applies only if the default_quota_days setting is missing or invalid.
	fallbackQuotaDays = 1000
)

var (
	ErrTitleRequired     = errors.New("title is required")
	ErrTitleTooLong      = errors.New("title is too long")
	ErrDateInvalid       = errors.New("date must be YYYY-MM-DD")
	ErrTimeInvalid       = errors.New("time must be HH:MM")
	ErrRepeatInvalid     = errors.New("unsupported repeat")
	ErrCommentTooLong    = errors.New("comment is too long")
	ErrIconInvalid       = errors.New("icon is invalid")
	ErrCollectionInvalid = errors.New("collection is not available")
	ErrTagInvalid        = errors.New("tag is not available")
	ErrQuotaDays         = errors.New("day quota reached")
)

type Day struct {
	Private      bool
	ID           int64
	OwnerID      int64
	CollectionID int64
	TagIDs       []int64 // ascending
	Title        string
	Date         string // YYYY-MM-DD
	Time         string // HH:MM or empty (all-day)
	Repeat       Repeat
	Calendar     Calendar
	Comment      string
	StyleColor   string // "#rrggbb" or empty
	StyleIcon    string
	// StyleBgAttachmentID is 0 when unset; it is managed by attachments (T13).
	StyleBgAttachmentID int64
	ArchivedAt          *time.Time
	ArchiveLive         bool // archived day keeps counting instead of freezing at ArchivedAt
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (d Day) Schedule() Schedule {
	return Schedule{Date: d.Date, Time: d.Time, Repeat: d.Repeat, Calendar: d.Calendar}
}

// CountAt is the instant the day's counter shows: the archive snapshot while
// archived and paused, otherwise now.
func (d Day) CountAt(now time.Time) time.Time {
	if d.ArchivedAt != nil && !d.ArchiveLive {
		return *d.ArchivedAt
	}
	return now
}

// DayInput is user-supplied data for create/update. A zero CollectionID
// selects the reserved default collection; empty TagIDs select the default tag.
type DayInput struct {
	Private      bool
	Title        string
	Date         string
	Time         string
	Repeat       Repeat
	Calendar     Calendar
	CollectionID int64
	TagIDs       []int64
	TagNames     []string // resolved or created atomically when saving
	Comment      string
	StyleColor   string
	StyleIcon    string
}

// Days is an owner-scoped repository; rows of other owners behave as if they do not exist.
type Days struct {
	db          *sql.DB
	collections *Groups
	tags        *Groups
}

func NewDays(db *sql.DB) *Days {
	return &Days{db: db, collections: NewCollections(db), tags: NewTags(db)}
}

// NormalizeDayInput trims and validates fields that need no database access.
func NormalizeDayInput(in DayInput) (DayInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Date = strings.TrimSpace(in.Date)
	in.Time = strings.TrimSpace(in.Time)
	in.Repeat = Repeat(strings.TrimSpace(string(in.Repeat)))
	in.Comment = strings.TrimSpace(strings.ReplaceAll(in.Comment, "\r\n", "\n"))
	in.StyleColor = strings.ToLower(strings.TrimSpace(in.StyleColor))
	in.StyleIcon = strings.TrimSpace(in.StyleIcon)
	if in.Repeat == "" {
		in.Repeat = RepeatNone
	}
	if in.Calendar == "" {
		in.Calendar = CalendarGregorian
	}
	if in.Calendar != CalendarGregorian && in.Calendar != CalendarLunar {
		return in, ErrCalendarInvalid
	}
	if in.Calendar == CalendarLunar {
		if _, err := SolarToLunar(in.Date); err != nil {
			return in, ErrCalendarInvalid
		}
	}
	switch {
	case in.Title == "":
		return in, ErrTitleRequired
	case utf8.RuneCountInString(in.Title) > MaxTitleRunes:
		return in, ErrTitleTooLong
	case !validDate(in.Date):
		return in, ErrDateInvalid
	case in.Time != "" && !validClock(in.Time):
		return in, ErrTimeInvalid
	}
	switch in.Repeat {
	case RepeatNone, RepeatWeekly, RepeatMonthly, RepeatYearly:
	default:
		return in, ErrRepeatInvalid
	}
	switch {
	case utf8.RuneCountInString(in.Comment) > MaxCommentRunes:
		return in, ErrCommentTooLong
	case in.StyleColor != "" && !colorPattern.MatchString(in.StyleColor):
		return in, ErrColorInvalid
	case !validIcon(in.StyleIcon):
		return in, ErrIconInvalid
	}
	in.TagIDs = uniqueIDs(in.TagIDs)
	in.TagNames = append([]string(nil), in.TagNames...)
	for i, name := range in.TagNames {
		name = strings.TrimSpace(name)
		if err := validateName(name); err != nil && !errors.Is(err, ErrNameReserved) {
			return in, fmt.Errorf("tag name: %w", err)
		}
		in.TagNames[i] = name
	}
	return in, nil
}

func validDate(s string) bool {
	d, err := time.Parse(time.DateOnly, s)
	return err == nil && d.Year() >= 1 && d.Format(time.DateOnly) == s
}

func validClock(s string) bool {
	c, err := time.Parse("15:04", s)
	return err == nil && c.Format("15:04") == s
}

// validIcon accepts a short emoji or symbol sequence without spaces or control characters.
func validIcon(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxIconRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func uniqueIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

const dayColumns = `id, owner_id, collection_id, title, date, COALESCE(time, ''), repeat, COALESCE(comment, ''),
	COALESCE(style_color, ''), COALESCE(style_icon, ''), COALESCE(style_bg_attachment_id, 0), archived_at, archive_live, created_at, updated_at, is_private, calendar`

// Get returns ErrNotFound when the day does not exist or belongs to someone else.
func (s *Days) Get(ctx context.Context, ownerID, id int64) (Day, error) {
	return s.get(ctx, s.db, ownerID, id)
}

type rowsQuerier interface {
	querier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanDay(sc scanner) (Day, error) {
	var d Day
	var archived sql.NullInt64
	var created, updated int64
	if err := sc.Scan(&d.ID, &d.OwnerID, &d.CollectionID, &d.Title, &d.Date, &d.Time, &d.Repeat, &d.Comment,
		&d.StyleColor, &d.StyleIcon, &d.StyleBgAttachmentID, &archived, &d.ArchiveLive, &created, &updated, &d.Private, &d.Calendar); err != nil {
		return Day{}, err
	}
	if archived.Valid {
		t := time.Unix(archived.Int64, 0).UTC()
		d.ArchivedAt = &t
	}
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.UpdatedAt = time.Unix(updated, 0).UTC()
	return d, nil
}

func (s *Days) get(ctx context.Context, q rowsQuerier, ownerID, id int64) (Day, error) {
	d, err := scanDay(q.QueryRowContext(ctx, `SELECT `+dayColumns+` FROM days WHERE owner_id = ? AND id = ? AND is_private = ?`, ownerID, id, IsPrivate(ctx)))
	if errors.Is(err, sql.ErrNoRows) {
		return Day{}, ErrNotFound
	}
	if err != nil {
		return Day{}, fmt.Errorf("get day: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT tag_id FROM day_tags WHERE day_id = ? ORDER BY tag_id`, d.ID)
	if err != nil {
		return Day{}, fmt.Errorf("get day tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tag int64
		if err := rows.Scan(&tag); err != nil {
			return Day{}, fmt.Errorf("get day tags: %w", err)
		}
		d.TagIDs = append(d.TagIDs, tag)
	}
	if err := rows.Err(); err != nil {
		return Day{}, fmt.Errorf("get day tags: %w", err)
	}
	return d, nil
}

// QuotaDays resolves the owner's day quota: the user override, else the site default.
func (s *Days) QuotaDays(ctx context.Context, ownerID int64) (int, error) {
	return quotaDays(ctx, s.db, ownerID)
}

func quotaDays(ctx context.Context, q querier, ownerID int64) (int, error) {
	var override sql.NullInt64
	var site sql.NullString
	err := q.QueryRowContext(ctx, `SELECT u.quota_days, (SELECT value FROM settings WHERE key = 'default_quota_days')
		FROM users u WHERE u.id = ?`, ownerID).Scan(&override, &site)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read day quota: %w", err)
	}
	if override.Valid {
		return int(override.Int64), nil
	}
	var n int
	if _, err := fmt.Sscanf(site.String, "%d", &n); err != nil || n < 0 {
		return fallbackQuotaDays, nil
	}
	return n, nil
}

// resolveGroups applies the defaults and verifies that the collection and tags belong to the owner.
func (s *Days) resolveGroups(ctx context.Context, tx *sql.Tx, ownerID int64, in DayInput, now time.Time) (DayInput, error) {
	if in.CollectionID == 0 {
		id, err := s.collections.defaultID(ctx, tx, ownerID)
		if err != nil {
			return in, err
		}
		in.CollectionID = id
	} else if err := ownedRow(ctx, tx, "collections", ownerID, in.CollectionID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return in, ErrCollectionInvalid
		}
		return in, err
	}
	if len(in.TagNames) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM tags WHERE owner_id = ?`, ownerID)
		if err != nil {
			return in, fmt.Errorf("resolve tag names: %w", err)
		}
		type namedTag struct {
			id   int64
			name string
		}
		var existing []namedTag
		for rows.Next() {
			var tag namedTag
			if err := rows.Scan(&tag.id, &tag.name); err != nil {
				rows.Close()
				return in, fmt.Errorf("resolve tag names: %w", err)
			}
			existing = append(existing, tag)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return in, fmt.Errorf("resolve tag names: %w", err)
		}
		for _, name := range in.TagNames {
			var id int64
			if IsReservedName(name) {
				id, err = s.tags.defaultID(ctx, tx, ownerID)
				if err != nil {
					return in, err
				}
			}
			for _, tag := range existing {
				if strings.EqualFold(name, tag.name) {
					id = tag.id
					break
				}
			}
			if id == 0 {
				res, err := tx.ExecContext(ctx, `INSERT INTO tags (owner_id, name, created_at) VALUES (?, ?, ?)`,
					ownerID, name, now.Unix())
				if err != nil {
					return in, fmt.Errorf("create day tag: %w", err)
				}
				id, err = res.LastInsertId()
				if err != nil {
					return in, fmt.Errorf("create day tag: %w", err)
				}
				existing = append(existing, namedTag{id: id, name: name})
			}
			in.TagIDs = append(in.TagIDs, id)
		}
		in.TagIDs = uniqueIDs(in.TagIDs)
	}
	if len(in.TagIDs) == 0 {
		id, err := s.tags.defaultID(ctx, tx, ownerID)
		if err != nil {
			return in, err
		}
		in.TagIDs = []int64{id}
		return in, nil
	}
	for _, tag := range in.TagIDs {
		if err := ownedRow(ctx, tx, "tags", ownerID, tag); err != nil {
			if errors.Is(err, ErrNotFound) {
				return in, ErrTagInvalid
			}
			return in, err
		}
	}
	return in, nil
}

func ownedRow(ctx context.Context, tx *sql.Tx, table string, ownerID, id int64) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE owner_id = ? AND id = ?`, ownerID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check %s: %w", table, err)
	}
	return nil
}

func setDayTags(ctx context.Context, tx *sql.Tx, dayID int64, tags []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM day_tags WHERE day_id = ?`, dayID); err != nil {
		return fmt.Errorf("set day tags: %w", err)
	}
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO day_tags (day_id, tag_id) VALUES (?, ?)`, dayID, tag); err != nil {
			return fmt.Errorf("set day tags: %w", err)
		}
	}
	return nil
}

// Create validates the input, applies defaults, and enforces quota_days
// (archived days count) in one transaction.
func (s *Days) Create(ctx context.Context, ownerID int64, in DayInput, now time.Time) (Day, error) {
	in, err := NormalizeDayInput(in)
	if err != nil {
		return Day{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Day{}, fmt.Errorf("create day: %w", err)
	}
	defer tx.Rollback()
	quota, err := quotaDays(ctx, tx, ownerID)
	if err != nil {
		return Day{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM days WHERE owner_id = ?`, ownerID).Scan(&count); err != nil {
		return Day{}, fmt.Errorf("count days: %w", err)
	}
	if count >= quota {
		return Day{}, ErrQuotaDays
	}
	if in, err = s.resolveGroups(ctx, tx, ownerID, in, now); err != nil {
		return Day{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO days (owner_id, collection_id, title, date, time, repeat, comment,
		style_color, style_icon, created_at, updated_at, is_private, calendar) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ownerID, in.CollectionID, in.Title, in.Date, nullString(in.Time), string(in.Repeat), nullString(in.Comment),
		nullString(in.StyleColor), nullString(in.StyleIcon), now.Unix(), now.Unix(), in.Private, in.Calendar)
	if err != nil {
		return Day{}, fmt.Errorf("create day: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Day{}, fmt.Errorf("create day: %w", err)
	}
	if err := setDayTags(ctx, tx, id, in.TagIDs); err != nil {
		return Day{}, err
	}
	d, err := s.get(WithPrivate(ctx, in.Private), tx, ownerID, id)
	if err != nil {
		return Day{}, err
	}
	if err := tx.Commit(); err != nil {
		return Day{}, fmt.Errorf("create day: %w", err)
	}
	return d, nil
}

// Update replaces the editable fields; archive state and background are untouched.
func (s *Days) Update(ctx context.Context, ownerID, id int64, in DayInput, now time.Time) (Day, error) {
	in, err := NormalizeDayInput(in)
	if err != nil {
		return Day{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Day{}, fmt.Errorf("update day: %w", err)
	}
	defer tx.Rollback()
	if _, err := s.get(ctx, tx, ownerID, id); err != nil {
		return Day{}, err
	}
	if in, err = s.resolveGroups(ctx, tx, ownerID, in, now); err != nil {
		return Day{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE days SET collection_id = ?, title = ?, date = ?, time = ?, repeat = ?,
		comment = ?, style_color = ?, style_icon = ?, updated_at = ?, is_private = ?, calendar = ? WHERE owner_id = ? AND id = ?`,
		in.CollectionID, in.Title, in.Date, nullString(in.Time), string(in.Repeat), nullString(in.Comment),
		nullString(in.StyleColor), nullString(in.StyleIcon), now.Unix(), in.Private, in.Calendar, ownerID, id); err != nil {
		return Day{}, fmt.Errorf("update day: %w", err)
	}
	if err := setDayTags(ctx, tx, id, in.TagIDs); err != nil {
		return Day{}, err
	}
	d, err := s.get(WithPrivate(ctx, in.Private), tx, ownerID, id)
	if err != nil {
		return Day{}, err
	}
	if err := tx.Commit(); err != nil {
		return Day{}, fmt.Errorf("update day: %w", err)
	}
	return d, nil
}

// SetArchived changes archive state without changing the day's content.
// Repeated archive requests preserve the original archive timestamp and play
// state; a new archive starts paused.
func (s *Days) SetArchived(ctx context.Context, ownerID, id int64, archived bool, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE days
		SET archived_at = CASE WHEN ? THEN COALESCE(archived_at, ?) ELSE NULL END,
			archive_live = CASE WHEN ? AND archived_at IS NOT NULL THEN archive_live ELSE 0 END, updated_at = ?
		WHERE owner_id = ? AND id = ? AND is_private = ?`, archived, now.Unix(), archived, now.Unix(), ownerID, id, IsPrivate(ctx))
	if err != nil {
		return fmt.Errorf("archive day: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("archive day: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetArchiveLive pauses or resumes an archived day's counter. Days that are
// not archived behave as not found.
func (s *Days) SetArchiveLive(ctx context.Context, ownerID, id int64, live bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE days SET archive_live = ?
		WHERE owner_id = ? AND id = ? AND archived_at IS NOT NULL AND is_private = ?`, live, ownerID, id, IsPrivate(ctx))
	if err != nil {
		return fmt.Errorf("set archive play state: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set archive play state: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes the day and shares targeting it. Tags, attachment rows and
// recipient hide markers cascade. Use DeleteWithAttachments when media files
// need to be removed from storage as well.
func (s *Days) Delete(ctx context.Context, ownerID, id int64) error {
	_, err := s.DeleteWithAttachments(ctx, ownerID, id)
	return err
}

// DeleteWithAttachments atomically removes the owned day and returns the
// attachment file IDs that must be removed from disk after the commit.
func (s *Days) DeleteWithAttachments(ctx context.Context, ownerID, id int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("delete day: %w", err)
	}
	defer tx.Rollback()
	if _, err := s.get(ctx, tx, ownerID, id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT uuid FROM attachments WHERE day_id = ? AND owner_id = ? AND sha256 IS NULL`,
		id, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list day attachments for deletion: %w", err)
	}
	var attachmentUUIDs []string
	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan day attachment for deletion: %w", err)
		}
		attachmentUUIDs = append(attachmentUUIDs, uuid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list day attachments for deletion: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close day attachment list: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM days WHERE owner_id = ? AND id = ?`, ownerID, id)
	if err != nil {
		return nil, fmt.Errorf("delete day: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("delete day: %w", err)
	} else if n == 0 {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shares WHERE owner_id = ? AND target_type = 'day' AND target_id = ?`,
		ownerID, id); err != nil {
		return nil, fmt.Errorf("delete day shares: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delete day: %w", err)
	}
	return attachmentUUIDs, nil
}
