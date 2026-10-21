package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound     = errors.New("attachment not found")
	ErrQuota        = errors.New("attachment quota exceeded")
	ErrTooLarge     = errors.New("attachment exceeds upload limit")
	ErrUploadStream = errors.New("invalid attachment stream")
	ErrFileMissing  = errors.New("stored attachment file is missing")
	ErrFilename     = errors.New("invalid attachment filename")
)

type Attachment struct {
	ID        int64
	DayID     int64
	OwnerID   int64
	UUID      string
	SHA256    string // empty for legacy rows still stored per owner
	Kind      string
	Filename  string
	MIME      string
	Size      int64
	CreatedAt time.Time
}

type Usage struct {
	CardUsed  int64
	CardLimit int64
	UserUsed  int64
	UserLimit int64
}

type Store struct {
	db   *sql.DB
	root string
}

func New(db *sql.DB, root string) *Store {
	return &Store{db: db, root: root}
}

func (s *Store) usage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID, dayID int64) (Usage, error) {
	var u Usage
	var cardLimit, userLimit string
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(u.max_card_bytes, mc.value),
		       COALESCE(u.quota_bytes, qb.value),
		       u.used_bytes,
		       COALESCE(SUM(a.size), 0)
		FROM users u
		LEFT JOIN days d ON d.owner_id = u.id AND d.id = ?
		JOIN settings mc ON mc.key = 'default_max_card_bytes'
		JOIN settings qb ON qb.key = 'default_quota_bytes'
		LEFT JOIN attachments a ON a.day_id = d.id AND a.owner_id = u.id AND a.detached = 0
		WHERE u.id = ? AND (? = 0 OR d.id IS NOT NULL)
		GROUP BY u.id`, dayID, ownerID, dayID).
		Scan(&cardLimit, &userLimit, &u.UserUsed, &u.CardUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, ErrNotFound
	}
	if err != nil {
		return Usage{}, fmt.Errorf("read attachment quotas: %w", err)
	}
	u.CardLimit, err = strconv.ParseInt(cardLimit, 10, 64)
	if err != nil {
		return Usage{}, fmt.Errorf("parse max_card_bytes: %w", err)
	}
	u.UserLimit, err = strconv.ParseInt(userLimit, 10, 64)
	if err != nil {
		return Usage{}, fmt.Errorf("parse quota_bytes: %w", err)
	}
	if u.CardLimit < 0 || u.UserLimit < 0 || u.CardUsed < 0 || u.UserUsed < 0 {
		return Usage{}, errors.New("attachment quotas or usage are negative")
	}
	return u, nil
}

func (s *Store) Usage(ctx context.Context, ownerID, dayID int64) (Usage, error) {
	if dayID <= 0 {
		return Usage{}, ErrNotFound
	}
	return s.usage(ctx, s.db, ownerID, dayID)
}

func (s *Store) Remaining(ctx context.Context, ownerID, dayID int64) (int64, error) {
	u, err := s.Usage(ctx, ownerID, dayID)
	if err != nil {
		return 0, err
	}
	return u.remaining(), nil
}

// NewDayRemaining checks the account and per-card allowance before a day exists.
func (s *Store) NewDayRemaining(ctx context.Context, ownerID int64) (int64, error) {
	u, err := s.usage(ctx, s.db, ownerID, 0)
	if err != nil {
		return 0, err
	}
	return u.remaining(), nil
}

func (s *Store) LibraryRemaining(ctx context.Context, ownerID int64) (int64, error) {
	u, err := s.usage(ctx, s.db, ownerID, 0)
	if err != nil {
		return 0, err
	}
	return max(0, u.UserLimit-u.UserUsed), nil
}

func (s *Store) UploadLibrary(ctx context.Context, ownerID int64, filename string, src io.Reader) (Attachment, error) {
	remaining, err := s.LibraryRemaining(ctx, ownerID)
	if err != nil {
		return Attachment{}, err
	}
	if remaining == 0 {
		return Attachment{}, ErrQuota
	}
	st, err := s.stage(src, remaining)
	if err != nil {
		return Attachment{}, err
	}
	defer os.Remove(st.temp)
	return s.insert(ctx, ownerID, 0, safeFilename(filename), st, true)
}

func (u Usage) remaining() int64 {
	card, user := u.CardLimit-u.CardUsed, u.UserLimit-u.UserUsed
	if card < 0 {
		card = 0
	}
	if user < 0 {
		user = 0
	}
	if user < card {
		return user
	}
	return card
}

// Upload stores new content for the owner's day. Content the owner already has
// in their media library is rejected with a *DuplicateError; content another
// user already stored is shared on disk but stays a separate attachment.
func (s *Store) Upload(ctx context.Context, ownerID, dayID int64, filename string, src io.Reader) (Attachment, error) {
	if dayID <= 0 {
		return Attachment{}, ErrNotFound
	}
	u, err := s.usage(ctx, s.db, ownerID, dayID)
	if err != nil {
		return Attachment{}, err
	}
	remaining := min(u.CardLimit-u.CardUsed, u.UserLimit-u.UserUsed)
	if remaining <= 0 {
		return Attachment{}, ErrQuota
	}
	st, err := s.stage(src, remaining)
	if err != nil {
		return Attachment{}, err
	}
	defer os.Remove(st.temp)
	return s.insert(ctx, ownerID, dayID, safeFilename(filename), st, true)
}

type staged struct {
	temp, sum, kind, mime string
	size                  int64
}

// stage streams src into a temporary blob file, hashing and sniffing it.
func (s *Store) stage(src io.Reader, limit int64) (staged, error) {
	temp, err := s.CreateTemp(".upload-*")
	if err != nil {
		return staged{}, fmt.Errorf("create temporary attachment: %w", err)
	}
	st := staged{temp: temp.Name()}
	fail := func(err error) (staged, error) {
		temp.Close()
		os.Remove(st.temp)
		return staged{}, err
	}
	prefix := make([]byte, SniffLen)
	n, readErr := io.ReadFull(src, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fail(fmt.Errorf("%w: read attachment content: %w", ErrUploadStream, readErr))
	}
	prefix = prefix[:n]
	content := io.MultiReader(bytes.NewReader(prefix), src)
	h := sha256.New()
	st.size, err = io.Copy(io.MultiWriter(temp, h), io.LimitReader(content, limit))
	if err != nil {
		return fail(fmt.Errorf("%w: stream attachment: %w", ErrUploadStream, err))
	}
	if st.size == limit {
		var extra [1]byte
		n, err := io.ReadFull(content, extra[:])
		if n > 0 {
			return fail(ErrTooLarge)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fail(fmt.Errorf("%w: probe attachment size: %w", ErrUploadStream, err))
		}
	}
	if err := temp.Close(); err != nil {
		os.Remove(st.temp)
		return staged{}, fmt.Errorf("close temporary attachment: %w", err)
	}
	st.sum = hex.EncodeToString(h.Sum(nil))
	st.mime, st.kind = Sniff(prefix)
	return st, nil
}

// insert records an attachment row for staged content. A non-empty st.temp is
// moved into the blob store; otherwise the blob must already exist.
func (s *Store) insert(ctx context.Context, ownerID, dayID int64, filename string, st staged, rejectDuplicate bool) (Attachment, error) {
	defer s.LockBlobs()()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, fmt.Errorf("begin attachment upload: %w", err)
	}
	defer tx.Rollback()
	if !rejectDuplicate && dayID > 0 {
		existing, err := scanAttachment(tx.QueryRowContext(ctx, `SELECT id, COALESCE(day_id, 0), owner_id, uuid,
			COALESCE(sha256, ''), kind, filename, mime, size, created_at FROM attachments
			WHERE owner_id = ? AND day_id = ? AND detached = 0 AND sha256 = ? ORDER BY id LIMIT 1`,
			ownerID, dayID, st.sum))
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Attachment{}, fmt.Errorf("check existing day reference: %w", err)
		}
	}
	u, err := s.usage(ctx, tx, ownerID, dayID)
	if err != nil {
		return Attachment{}, err
	}
	remaining := min(u.CardLimit-u.CardUsed, u.UserLimit-u.UserUsed)
	if dayID == 0 {
		remaining = u.UserLimit - u.UserUsed
	}
	if remaining <= 0 {
		return Attachment{}, ErrQuota
	}
	if st.size > remaining {
		return Attachment{}, ErrTooLarge
	}
	if rejectDuplicate {
		existing, err := findBySum(ctx, tx, ownerID, st.sum)
		if err != nil {
			return Attachment{}, err
		}
		if existing != 0 {
			return Attachment{}, &DuplicateError{ExistingID: existing}
		}
	}
	uuid, err := newUUID()
	if err != nil {
		return Attachment{}, err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `INSERT INTO attachments
		(day_id, owner_id, uuid, kind, filename, mime, size, created_at, sha256)
		VALUES (NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?)`,
		dayID, ownerID, uuid, st.kind, filename, st.mime, st.size, now.Unix(), st.sum)
	if err != nil {
		return Attachment{}, fmt.Errorf("insert attachment metadata: %w", err)
	}
	attachmentID, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, fmt.Errorf("read attachment ID: %w", err)
	}
	res, err = tx.ExecContext(ctx, `UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?`, st.size, ownerID)
	if err != nil {
		return Attachment{}, fmt.Errorf("update attachment usage: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return Attachment{}, fmt.Errorf("check attachment usage update: %w", err)
		}
		return Attachment{}, errors.New("attachment owner disappeared during upload")
	}
	created := false
	if st.temp != "" {
		if created, err = s.PlaceBlob(st.temp, st.sum); err != nil {
			return Attachment{}, err
		}
	} else if _, err := os.Stat(s.blobPath(st.sum)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Attachment{}, fmt.Errorf("%w: %w", ErrFileMissing, err)
		}
		return Attachment{}, fmt.Errorf("inspect media blob: %w", err)
	}
	if err := tx.Commit(); err != nil {
		err = fmt.Errorf("commit attachment upload: %w", err)
		if created {
			err = errors.Join(err, s.DiscardBlob(st.sum))
		}
		return Attachment{}, err
	}
	return Attachment{ID: attachmentID, DayID: dayID, UUID: uuid, Kind: st.kind, SHA256: st.sum,
		OwnerID: ownerID, Filename: filename, MIME: st.mime, Size: st.size, CreatedAt: now}, nil
}

// SafeFilename strips directories and control characters from a display filename.
func SafeFilename(name string) string { return safeFilename(name) }

// NewUUID returns a random storage name for an attachment file.
func NewUUID() (string, error) { return newUUID() }

func detectedKind(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "file"
	}
	switch {
	case strings.HasPrefix(mediaType, "image/") && mediaType != "image/svg+xml":
		return "image"
	case strings.HasPrefix(mediaType, "audio/"):
		return "audio"
	case strings.HasPrefix(mediaType, "video/"):
		return "video"
	default:
		return "file"
	}
}

func safeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "." || name == "/" || name == "" {
		return "attachment"
	}
	return name
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate attachment ID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (s *Store) ListDay(ctx context.Context, ownerID, dayID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.day_id, a.owner_id, a.uuid, COALESCE(a.sha256, ''), a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a JOIN days d ON d.id = a.day_id
		WHERE a.owner_id = ? AND d.owner_id = ? AND d.id = ? AND a.detached = 0 ORDER BY a.created_at, a.id`,
		ownerID, ownerID, dayID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		var created int64
		if err := rows.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.SHA256, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attachments: %w", err)
	}
	return out, nil
}

// LibraryItem is an owned attachment together with the day it belongs to.
type LibraryItem struct {
	Attachment
	DayTitle string
}

// ListOwner returns each stored file once, using its first owned attachment.
func (s *Store) ListOwner(ctx context.Context, ownerID int64) ([]LibraryItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, COALESCE(a.day_id, 0), a.owner_id, a.uuid, COALESCE(a.sha256, ''), a.kind, a.filename, a.mime, a.size, a.created_at, COALESCE(d.title, '')
		FROM attachments a LEFT JOIN days d ON d.id = a.day_id AND d.owner_id = a.owner_id
		WHERE a.owner_id = ? AND (a.sha256 IS NULL OR NOT EXISTS (
			SELECT 1 FROM attachments original WHERE original.owner_id = a.owner_id
			AND original.sha256 = a.sha256 AND original.id < a.id
		)) ORDER BY a.created_at DESC, a.id DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list media library: %w", err)
	}
	defer rows.Close()
	var out []LibraryItem
	for rows.Next() {
		var item LibraryItem
		var created int64
		if err := rows.Scan(&item.ID, &item.DayID, &item.OwnerID, &item.UUID, &item.SHA256, &item.Kind, &item.Filename,
			&item.MIME, &item.Size, &created, &item.DayTitle); err != nil {
			return nil, fmt.Errorf("scan media library item: %w", err)
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media library: %w", err)
	}
	return out, nil
}

// CopyToDay attaches an owned library item to another owned day as an
// independent attachment row sharing the stored content, so deleting either
// one leaves the other intact; it counts against the same per-day and account
// quotas as an upload.
func (s *Store) CopyToDay(ctx context.Context, ownerID, attachmentID, dayID int64) (Attachment, error) {
	src, err := s.Get(ctx, ownerID, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	file, err := s.Open(src)
	if err != nil {
		return Attachment{}, fmt.Errorf("open selected library file: %w", err)
	}
	if err := file.Close(); err != nil {
		return Attachment{}, fmt.Errorf("close selected library file: %w", err)
	}
	if dayID <= 0 {
		return Attachment{}, ErrNotFound
	}
	if _, err := s.usage(ctx, s.db, ownerID, dayID); err != nil {
		return Attachment{}, err
	}
	// Re-selecting an active file does not add another day reference.
	var existing int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM attachments
		WHERE owner_id = ? AND day_id = ? AND detached = 0 AND (id = ? OR (sha256 IS NOT NULL AND sha256 = ?))
		ORDER BY id LIMIT 1`, ownerID, dayID, src.ID, src.SHA256).Scan(&existing)
	if err == nil {
		return s.Get(ctx, ownerID, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, fmt.Errorf("find selected attachment: %w", err)
	}
	if src.SHA256 != "" {
		return s.insert(ctx, ownerID, dayID, src.Filename,
			staged{sum: src.SHA256, kind: src.Kind, mime: src.MIME, size: src.Size}, false)
	}
	f, err := s.Open(src)
	if err != nil {
		return Attachment{}, fmt.Errorf("open library attachment: %w", err)
	}
	defer f.Close()
	if _, err := s.usage(ctx, s.db, ownerID, dayID); err != nil {
		return Attachment{}, err
	}
	st, err := s.stage(f, src.Size+1)
	if err != nil {
		return Attachment{}, err
	}
	defer os.Remove(st.temp)
	return s.insert(ctx, ownerID, dayID, src.Filename, st, false)
}

// Open reads an attachment after its metadata has been authorized by the caller.
func (s *Store) Open(a Attachment) (*os.File, error) {
	f, err := os.Open(s.Path(a.OwnerID, a))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", ErrFileMissing, err)
	}
	if err != nil {
		return nil, fmt.Errorf("open stored attachment: %w", err)
	}
	return f, nil
}

func (s *Store) ListAccessibleDay(ctx context.Context, viewerID, dayID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.day_id, a.owner_id, a.uuid, COALESCE(a.sha256, ''), a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a JOIN days d ON d.id = a.day_id
		WHERE d.id = ? AND a.detached = 0 AND (
			d.owner_id = ? OR EXISTS (
				SELECT 1 FROM shares sh
				JOIN share_recipients sr ON sr.share_id = sh.id
				WHERE sh.owner_id = d.owner_id AND sr.recipient_id = ? AND sr.status = 'accepted'
				AND ((sh.target_type = 'day' AND sh.target_id = d.id)
					OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL))
			)
		) ORDER BY a.created_at, a.id`, dayID, viewerID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("list accessible attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		var created int64
		if err := rows.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.SHA256, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created); err != nil {
			return nil, fmt.Errorf("scan accessible attachment: %w", err)
		}
		a.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accessible attachments: %w", err)
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, ownerID, id int64) (Attachment, error) {
	var a Attachment
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(day_id, 0), owner_id, uuid, COALESCE(sha256, ''), kind, filename, mime, size, created_at
		FROM attachments WHERE owner_id = ? AND id = ?`, ownerID, id).
		Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.SHA256, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

// Rename changes only display metadata, leaving storage and quota usage intact.
func (s *Store) Rename(ctx context.Context, ownerID, id int64, filename string) error {
	if _, err := s.Get(ctx, ownerID, id); err != nil {
		return err
	}
	filename = strings.TrimSpace(filename)
	if !utf8.ValidString(filename) || utf8.RuneCountInString(filename) > 255 ||
		filename == "" || filename == "." || filename == ".." ||
		strings.ContainsAny(filename, "/\\") || safeFilename(filename) != filename {
		return ErrFilename
	}
	res, err := s.db.ExecContext(ctx, `UPDATE attachments SET filename = ? WHERE owner_id = ? AND id = ?`,
		filename, ownerID, id)
	if err != nil {
		return fmt.Errorf("rename attachment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check attachment rename: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAccessible(ctx context.Context, viewerID, id int64) (Attachment, error) {
	a, err := scanAttachment(s.db.QueryRowContext(ctx, `
		SELECT a.id, COALESCE(a.day_id, 0), a.owner_id, a.uuid, COALESCE(a.sha256, ''), a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a
		LEFT JOIN days d ON d.id = a.day_id
		WHERE a.id = ? AND (
			a.owner_id = ? OR (a.detached = 0 AND EXISTS (
				SELECT 1
				FROM shares sh
				JOIN share_recipients sr ON sr.share_id = sh.id
				WHERE sh.owner_id = a.owner_id AND sr.recipient_id = ? AND sr.status = 'accepted'
				AND (
					(sh.target_type = 'day' AND sh.target_id = d.id)
					OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL)
				)
			))
		)`, id, viewerID, viewerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get accessible attachment: %w", err)
	}
	return a, nil
}

// Detach keeps the file in its owner's library and retains its source day.
func (s *Store) Detach(ctx context.Context, ownerID, dayID, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment removal: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE attachments SET detached = 1
		WHERE owner_id = ? AND day_id = ? AND id = ? AND detached = 0
		AND EXISTS (SELECT 1 FROM days WHERE id = ? AND owner_id = ?)`,
		ownerID, dayID, id, dayID, ownerID)
	if err != nil {
		return fmt.Errorf("remove day attachment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check attachment removal: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE days SET style_bg_attachment_id = NULL
		WHERE id = ? AND owner_id = ? AND style_bg_attachment_id = ?`, dayID, ownerID, id); err != nil {
		return fmt.Errorf("clear removed attachment background: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment removal: %w", err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, ownerID, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment deletion: %w", err)
	}
	defer tx.Rollback()
	a, err := scanAttachment(tx.QueryRowContext(ctx, `SELECT id, COALESCE(day_id, 0), owner_id, uuid, COALESCE(sha256, ''), kind, filename, mime, size, created_at
		FROM attachments WHERE owner_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get attachment for deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE owner_id = ? AND id = ?`, ownerID, id); err != nil {
		return fmt.Errorf("delete attachment metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment deletion: %w", err)
	}
	if a.SHA256 != "" {
		return s.Collect(ctx)
	}
	if err := os.Remove(s.Path(ownerID, a)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove attachment file: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanAttachment(row rowScanner) (Attachment, error) {
	var a Attachment
	var created int64
	err := row.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.SHA256, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created)
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, err
}

// RemoveFiles deletes legacy per-owner files; shared blobs are removed by Collect.
func (s *Store) RemoveFiles(ownerID int64, uuids []string) error {
	var errs []error
	for _, uuid := range uuids {
		if err := os.Remove(s.Path(ownerID, Attachment{UUID: uuid})); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) SetBackground(ctx context.Context, ownerID, dayID, attachmentID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE days
		SET style_bg_attachment_id = ?
		WHERE id = ? AND owner_id = ? AND EXISTS (
			SELECT 1 FROM attachments a WHERE a.id = ? AND a.day_id = days.id
			AND a.owner_id = days.owner_id AND a.kind = 'image' AND a.detached = 0
		)`, attachmentID, dayID, ownerID, attachmentID)
	if err != nil {
		return fmt.Errorf("set attachment background: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check background update: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Path returns the shared content blob, or the legacy per-owner file for rows
// that have no content hash yet.
func (s *Store) Path(ownerID int64, a Attachment) string {
	if validSum(a.SHA256) {
		return s.blobPath(a.SHA256)
	}
	return filepath.Join(s.root, strconv.FormatInt(ownerID, 10), a.UUID)
}

func (s *Store) ClearBackground(ctx context.Context, ownerID, dayID, attachmentID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE days SET style_bg_attachment_id = NULL
		WHERE id = ? AND owner_id = ? AND EXISTS (
			SELECT 1 FROM attachments a WHERE a.id = ? AND a.day_id = days.id
			AND a.owner_id = days.owner_id AND a.kind = 'image'
		)`, dayID, ownerID, attachmentID)
	if err != nil {
		return fmt.Errorf("clear attachment background: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check background removal: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
