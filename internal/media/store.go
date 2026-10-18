package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("attachment not found")
	ErrQuota        = errors.New("attachment quota exceeded")
	ErrTooLarge     = errors.New("attachment exceeds upload limit")
	ErrUploadStream = errors.New("invalid attachment stream")
)

type Attachment struct {
	ID        int64
	DayID     int64
	OwnerID   int64
	UUID      string
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
		JOIN days d ON d.owner_id = u.id
		JOIN settings mc ON mc.key = 'default_max_card_bytes'
		JOIN settings qb ON qb.key = 'default_quota_bytes'
		LEFT JOIN attachments a ON a.day_id = d.id AND a.owner_id = u.id
		WHERE u.id = ? AND d.id = ?
		GROUP BY u.id`, ownerID, dayID).
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
	return s.usage(ctx, s.db, ownerID, dayID)
}

func (s *Store) Remaining(ctx context.Context, ownerID, dayID int64) (int64, error) {
	u, err := s.Usage(ctx, ownerID, dayID)
	if err != nil {
		return 0, err
	}
	card, user := u.CardLimit-u.CardUsed, u.UserLimit-u.UserUsed
	if card < 0 {
		card = 0
	}
	if user < 0 {
		user = 0
	}
	if user < card {
		return user, nil
	}
	return card, nil
}

func (s *Store) Upload(ctx context.Context, ownerID, dayID int64, filename string, src io.Reader) (Attachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, fmt.Errorf("begin attachment upload: %w", err)
	}
	defer tx.Rollback()
	u, err := s.usage(ctx, tx, ownerID, dayID)
	if err != nil {
		return Attachment{}, err
	}
	remaining := min(u.CardLimit-u.CardUsed, u.UserLimit-u.UserUsed)
	if remaining <= 0 {
		return Attachment{}, ErrQuota
	}

	dir := filepath.Join(s.root, strconv.FormatInt(ownerID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, fmt.Errorf("create user media directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return Attachment{}, fmt.Errorf("create temporary attachment: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	prefix := make([]byte, SniffLen)
	n, readErr := io.ReadFull(src, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		temp.Close()
		return Attachment{}, fmt.Errorf("%w: read attachment content: %w", ErrUploadStream, readErr)
	}
	prefix = prefix[:n]
	content := io.MultiReader(bytes.NewReader(prefix), src)
	size, err := io.Copy(temp, io.LimitReader(content, remaining))
	if err != nil {
		temp.Close()
		return Attachment{}, fmt.Errorf("%w: stream attachment: %w", ErrUploadStream, err)
	}
	if size == remaining {
		var extra [1]byte
		n, err := io.ReadFull(content, extra[:])
		if n > 0 {
			temp.Close()
			return Attachment{}, ErrTooLarge
		}
		if err != nil && !errors.Is(err, io.EOF) {
			temp.Close()
			return Attachment{}, fmt.Errorf("%w: probe attachment size: %w", ErrUploadStream, err)
		}
	}
	if err := temp.Close(); err != nil {
		return Attachment{}, fmt.Errorf("close temporary attachment: %w", err)
	}

	contentType, kind := Sniff(prefix)
	uuid, err := newUUID()
	if err != nil {
		return Attachment{}, err
	}
	finalName := filepath.Join(dir, uuid)
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `INSERT INTO attachments
		(day_id, owner_id, uuid, kind, filename, mime, size, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		dayID, ownerID, uuid, kind, safeFilename(filename), contentType, size, now.Unix())
	if err != nil {
		return Attachment{}, fmt.Errorf("insert attachment metadata: %w", err)
	}
	attachmentID, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, fmt.Errorf("read attachment ID: %w", err)
	}
	res, err = tx.ExecContext(ctx, `UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?`, size, ownerID)
	if err != nil {
		return Attachment{}, fmt.Errorf("update attachment usage: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return Attachment{}, fmt.Errorf("check attachment usage update: %w", err)
		}
		return Attachment{}, errors.New("attachment owner disappeared during upload")
	}
	if err := os.Rename(tempName, finalName); err != nil {
		return Attachment{}, fmt.Errorf("store attachment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if removeErr := os.Remove(finalName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return Attachment{}, errors.Join(fmt.Errorf("commit attachment upload: %w", err),
				fmt.Errorf("remove uncommitted attachment: %w", removeErr))
		}
		return Attachment{}, fmt.Errorf("commit attachment upload: %w", err)
	}
	return Attachment{ID: attachmentID, DayID: dayID, UUID: uuid, Kind: kind,
		OwnerID: ownerID, Filename: safeFilename(filename), MIME: contentType, Size: size, CreatedAt: now}, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.day_id, a.owner_id, a.uuid, a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a JOIN days d ON d.id = a.day_id
		WHERE a.owner_id = ? AND d.owner_id = ? AND d.id = ? ORDER BY a.created_at, a.id`,
		ownerID, ownerID, dayID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		var created int64
		if err := rows.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created); err != nil {
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

func (s *Store) ListAccessibleDay(ctx context.Context, viewerID, dayID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.day_id, a.owner_id, a.uuid, a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a JOIN days d ON d.id = a.day_id
		WHERE d.id = ? AND (
			d.owner_id = ? OR EXISTS (
				SELECT 1 FROM shares sh
				JOIN share_recipients sr ON sr.share_id = sh.id
				WHERE sh.owner_id = d.owner_id AND sr.recipient_id = ?
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
		if err := rows.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT id, day_id, owner_id, uuid, kind, filename, mime, size, created_at
		FROM attachments WHERE owner_id = ? AND id = ?`, ownerID, id).
		Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

func (s *Store) GetAccessible(ctx context.Context, viewerID, id int64) (Attachment, error) {
	a, err := scanAttachment(s.db.QueryRowContext(ctx, `
		SELECT a.id, a.day_id, a.owner_id, a.uuid, a.kind, a.filename, a.mime, a.size, a.created_at
		FROM attachments a
		JOIN days d ON d.id = a.day_id
		WHERE a.id = ? AND (
			a.owner_id = ? OR EXISTS (
				SELECT 1
				FROM shares sh
				JOIN share_recipients sr ON sr.share_id = sh.id
				WHERE sh.owner_id = a.owner_id AND sr.recipient_id = ?
				AND (
					(sh.target_type = 'day' AND sh.target_id = d.id)
					OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL)
				)
			)
		)`, id, viewerID, viewerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get accessible attachment: %w", err)
	}
	return a, nil
}

func (s *Store) Delete(ctx context.Context, ownerID, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment deletion: %w", err)
	}
	defer tx.Rollback()
	a, err := scanAttachment(tx.QueryRowContext(ctx, `SELECT id, day_id, owner_id, uuid, kind, filename, mime, size, created_at
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
	err := row.Scan(&a.ID, &a.DayID, &a.OwnerID, &a.UUID, &a.Kind, &a.Filename, &a.MIME, &a.Size, &created)
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, err
}

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
			AND a.owner_id = days.owner_id AND a.kind = 'image'
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

func (s *Store) Path(ownerID int64, a Attachment) string {
	return filepath.Join(s.root, strconv.FormatInt(ownerID, 10), a.UUID)
}
