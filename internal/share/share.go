// Package share contains share management and recipient access resolution.
package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrTargetInvalid     = errors.New("share target is not available")
	ErrRecipientsInvalid = errors.New("one or more recipients are unavailable")
	ErrCannotShareSelf   = errors.New("cannot share with yourself")
	ErrNotFound          = errors.New("share not found")
)

type TargetType string

const (
	TargetDay        TargetType = "day"
	TargetCollection TargetType = "collection"
)

type Recipient struct {
	ID       int64
	Username string
	Email    string
}

type Share struct {
	ID          int64
	OwnerID     int64
	TargetType  TargetType
	TargetID    int64
	ShowComment bool
	Recipients  []Recipient
}

type SharedDay struct {
	Day
	OwnerUsername string
	ShowComment   bool
	Hidden        bool
}

// Day contains only fields that are safe to return to a recipient. In
// particular, collection IDs and tags are not loaded.
type Day struct {
	ID                  int64
	OwnerID             int64
	Title               string
	Date                string
	Time                string
	Repeat              string
	Comment             string
	StyleColor          string
	StyleIcon           string
	StyleBgAttachmentID int64
	ArchivedAt          *time.Time
	ArchiveLive         bool
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, ownerID int64, targetType TargetType, targetID int64,
	identifiers []string, showComment bool, now time.Time) (Share, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Share{}, fmt.Errorf("create share: %w", err)
	}
	defer tx.Rollback()

	if err := checkTarget(ctx, tx, ownerID, targetType, targetID); err != nil {
		return Share{}, err
	}
	recipients, err := resolveRecipients(ctx, tx, ownerID, identifiers)
	if err != nil {
		return Share{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO shares (owner_id, target_type, target_id, show_comment, created_at)
		VALUES (?, ?, ?, ?, ?)`, ownerID, targetType, targetID, showComment, now.Unix())
	if err != nil {
		return Share{}, fmt.Errorf("insert share: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Share{}, fmt.Errorf("read share ID: %w", err)
	}
	if err := insertRecipients(ctx, tx, id, recipients); err != nil {
		return Share{}, err
	}
	if err := tx.Commit(); err != nil {
		return Share{}, fmt.Errorf("commit share: %w", err)
	}
	return Share{ID: id, OwnerID: ownerID, TargetType: targetType, TargetID: targetID,
		ShowComment: showComment, Recipients: recipients}, nil
}

func (s *Store) Update(ctx context.Context, ownerID, shareID int64, identifiers []string, showComment bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update share: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM shares WHERE owner_id = ? AND id = ?`, ownerID, shareID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("find share to update: %w", err)
	}
	recipients, err := resolveRecipients(ctx, tx, ownerID, identifiers)
	if err != nil {
		return err
	}
	args := []any{shareID}
	for _, recipient := range recipients {
		args = append(args, recipient.ID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(recipients)), ",")
	if _, err := tx.ExecContext(ctx, `DELETE FROM share_recipients WHERE share_id = ?
		AND recipient_id NOT IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("replace share recipients: %w", err)
	}
	if err := insertRecipients(ctx, tx, shareID, recipients); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shares SET show_comment = ? WHERE owner_id = ? AND id = ?`,
		showComment, ownerID, shareID); err != nil {
		return fmt.Errorf("update share visibility: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit share update: %w", err)
	}
	return nil
}

func checkTarget(ctx context.Context, tx *sql.Tx, ownerID int64, targetType TargetType, targetID int64) error {
	var query string
	switch targetType {
	case TargetDay:
		query = `SELECT 1 FROM days WHERE owner_id = ? AND id = ?`
	case TargetCollection:
		query = `SELECT 1 FROM collections WHERE owner_id = ? AND id = ?`
	default:
		return ErrTargetInvalid
	}
	var exists int
	err := tx.QueryRowContext(ctx, query, ownerID, targetID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTargetInvalid
	}
	if err != nil {
		return fmt.Errorf("check share target: %w", err)
	}
	return nil
}

type recipientQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveRecipients(ctx context.Context, q recipientQuerier, ownerID int64, identifiers []string) ([]Recipient, error) {
	var recipients []Recipient
	seen := make(map[int64]bool)
	for _, raw := range identifiers {
		identifier := strings.TrimSpace(raw)
		if identifier == "" {
			continue
		}
		var recipient Recipient
		err := q.QueryRowContext(ctx, `SELECT id, username, email FROM users
			WHERE is_active = 1 AND (username = ? COLLATE NOCASE OR email = ? COLLATE NOCASE)`,
			identifier, identifier).Scan(&recipient.ID, &recipient.Username, &recipient.Email)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRecipientsInvalid
		}
		if err != nil {
			return nil, fmt.Errorf("resolve share recipient: %w", err)
		}
		if recipient.ID == ownerID {
			return nil, ErrCannotShareSelf
		}
		if !seen[recipient.ID] {
			recipients = append(recipients, recipient)
			seen[recipient.ID] = true
		}
	}
	if len(recipients) == 0 {
		return nil, ErrRecipientsInvalid
	}
	return recipients, nil
}

func insertRecipients(ctx context.Context, tx *sql.Tx, shareID int64, recipients []Recipient) error {
	for _, recipient := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO share_recipients (share_id, recipient_id, status)
			VALUES (?, ?, 'pending') ON CONFLICT(share_id, recipient_id) DO NOTHING`,
			shareID, recipient.ID); err != nil {
			return fmt.Errorf("insert share recipient: %w", err)
		}
	}
	return nil
}

func (s *Store) List(ctx context.Context, ownerID int64, targetType TargetType, targetID int64) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sh.id, sh.owner_id, sh.target_type, sh.target_id, sh.show_comment,
		u.id, u.username, u.email
		FROM shares sh LEFT JOIN share_recipients sr ON sr.share_id = sh.id
		LEFT JOIN users u ON u.id = sr.recipient_id
		WHERE sh.owner_id = ? AND sh.target_type = ? AND sh.target_id = ?
		ORDER BY sh.id, u.username COLLATE NOCASE`, ownerID, targetType, targetID)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()
	var shares []Share
	indices := make(map[int64]int)
	for rows.Next() {
		var sh Share
		var show int
		var recipientID sql.NullInt64
		var username, email sql.NullString
		if err := rows.Scan(&sh.ID, &sh.OwnerID, &sh.TargetType, &sh.TargetID, &show,
			&recipientID, &username, &email); err != nil {
			return nil, fmt.Errorf("scan share: %w", err)
		}
		sh.ShowComment = show != 0
		i, ok := indices[sh.ID]
		if !ok {
			i = len(shares)
			indices[sh.ID] = i
			shares = append(shares, sh)
		}
		if recipientID.Valid {
			shares[i].Recipients = append(shares[i].Recipients, Recipient{
				ID: recipientID.Int64, Username: username.String, Email: email.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shares: %w", err)
	}
	return shares, nil
}

type SharedTarget struct {
	TargetType TargetType
	TargetID   int64
	Title      string
	Reserved   bool
}

func (s *Store) ListOwnedTargets(ctx context.Context, ownerID int64) ([]SharedTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT sh.target_type, sh.target_id,
		COALESCE(d.title, c.name), COALESCE(c.is_reserved, 0)
		FROM shares sh
		LEFT JOIN days d ON sh.target_type = 'day' AND d.id = sh.target_id AND d.owner_id = sh.owner_id
		LEFT JOIN collections c ON sh.target_type = 'collection' AND c.id = sh.target_id AND c.owner_id = sh.owner_id
		WHERE sh.owner_id = ? AND (d.id IS NOT NULL OR c.id IS NOT NULL)
		AND EXISTS (SELECT 1 FROM share_recipients sr WHERE sr.share_id = sh.id)
		ORDER BY COALESCE(d.title, c.name) COLLATE NOCASE, sh.target_type, sh.target_id`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list owned share targets: %w", err)
	}
	defer rows.Close()
	var targets []SharedTarget
	for rows.Next() {
		var target SharedTarget
		if err := rows.Scan(&target.TargetType, &target.TargetID, &target.Title, &target.Reserved); err != nil {
			return nil, fmt.Errorf("scan owned share target: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned share targets: %w", err)
	}
	return targets, nil
}

func (s *Store) Revoke(ctx context.Context, ownerID, shareID int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE owner_id = ? AND id = ?`, ownerID, shareID)
	if err != nil {
		return fmt.Errorf("revoke share: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoked share result: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Target(ctx context.Context, ownerID, shareID int64) (TargetType, int64, error) {
	var targetType TargetType
	var targetID int64
	err := s.db.QueryRowContext(ctx, `SELECT target_type, target_id FROM shares WHERE owner_id = ? AND id = ?`,
		ownerID, shareID).Scan(&targetType, &targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("get share target: %w", err)
	}
	return targetType, targetID, nil
}

const accessPredicate = `EXISTS (
	SELECT 1 FROM shares sh
	JOIN share_recipients sr ON sr.share_id = sh.id
	WHERE sh.owner_id = d.owner_id AND sr.recipient_id = ? AND sr.status = 'accepted'
	AND ((sh.target_type = 'day' AND sh.target_id = d.id)
		OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL))
)`

func (s *Store) CanView(ctx context.Context, viewerID, dayID int64) (bool, bool, error) {
	var allowed, showComment int
	err := s.db.QueryRowContext(ctx, `SELECT `+accessPredicate+`,
		COALESCE((SELECT MAX(sh.show_comment) FROM shares sh
			JOIN share_recipients sr ON sr.share_id = sh.id
			JOIN days d ON d.id = ?
			WHERE sh.owner_id = d.owner_id AND sr.recipient_id = ? AND sr.status = 'accepted'
			AND ((sh.target_type = 'day' AND sh.target_id = d.id)
				OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL))), 0)
		FROM days d WHERE d.id = ?`, viewerID, dayID, viewerID, dayID).Scan(&allowed, &showComment)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("resolve shared day access: %w", err)
	}
	return allowed != 0, showComment != 0, nil
}

func (s *Store) GetShared(ctx context.Context, viewerID, dayID int64) (SharedDay, error) {
	rows, err := s.queryShared(ctx, viewerID, true, dayID)
	if err != nil {
		return SharedDay{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SharedDay{}, fmt.Errorf("read shared day: %w", err)
		}
		return SharedDay{}, ErrNotFound
	}
	shared, err := scanSharedDay(rows)
	if err != nil {
		return SharedDay{}, fmt.Errorf("scan shared day: %w", err)
	}
	return shared, nil
}

func (s *Store) ListForUser(ctx context.Context, viewerID int64, includeHidden bool) ([]SharedDay, error) {
	rows, err := s.queryShared(ctx, viewerID, includeHidden)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SharedDay
	for rows.Next() {
		shared, err := scanSharedDay(rows)
		if err != nil {
			return nil, fmt.Errorf("scan shared days: %w", err)
		}
		out = append(out, shared)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shared days: %w", err)
	}
	return out, nil
}

type sharedRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func (s *Store) queryShared(ctx context.Context, viewerID int64, includeHidden bool, dayID ...int64) (*sql.Rows, error) {
	where := `d.owner_id <> ? AND ` + accessPredicate
	args := []any{viewerID, viewerID}
	if len(dayID) > 0 {
		where += ` AND d.id = ?`
		args = append(args, dayID[0])
	}
	hidden := `EXISTS (SELECT 1 FROM share_hidden h WHERE h.user_id = ? AND h.day_id = d.id)`
	if !includeHidden {
		where += ` AND NOT ` + hidden
	}
	args = append(args, viewerID)
	query := `SELECT d.id, d.owner_id, d.title, d.date, COALESCE(d.time, ''), d.repeat,
		COALESCE(d.comment, ''), COALESCE(d.style_color, ''), COALESCE(d.style_icon, ''),
		COALESCE(d.style_bg_attachment_id, 0), d.archived_at, d.archive_live, u.username,
		COALESCE(MAX(sh.show_comment), 0), ` + hidden + `
		FROM days d JOIN users u ON u.id = d.owner_id
		JOIN shares sh ON sh.owner_id = d.owner_id
			AND ((sh.target_type = 'day' AND sh.target_id = d.id)
				OR (sh.target_type = 'collection' AND sh.target_id = d.collection_id AND d.archived_at IS NULL))
		JOIN share_recipients sr ON sr.share_id = sh.id AND sr.recipient_id = ? AND sr.status = 'accepted'
		WHERE ` + where + `
		GROUP BY d.id ORDER BY d.id`
	// The access predicate receives the viewer before the hidden expression.
	queryArgs := []any{viewerID, viewerID}
	queryArgs = append(queryArgs, args...)
	return s.db.QueryContext(ctx, query, queryArgs...)
}

func scanSharedDay(rows sharedRows) (SharedDay, error) {
	var out SharedDay
	var archived sql.NullInt64
	var show, hidden int
	if err := rows.Scan(&out.ID, &out.OwnerID, &out.Title, &out.Date, &out.Time, &out.Repeat,
		&out.Comment, &out.StyleColor, &out.StyleIcon, &out.StyleBgAttachmentID, &archived, &out.ArchiveLive,
		&out.OwnerUsername, &show, &hidden); err != nil {
		return SharedDay{}, err
	}
	if archived.Valid {
		t := time.Unix(archived.Int64, 0).UTC()
		out.ArchivedAt = &t
	}
	out.ShowComment, out.Hidden = show != 0, hidden != 0
	if !out.ShowComment {
		out.Comment = ""
	}
	return out, nil
}

func (s *Store) SetHidden(ctx context.Context, viewerID, dayID int64, hidden bool) error {
	allowed, _, err := s.CanView(ctx, viewerID, dayID)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	if hidden {
		_, err = s.db.ExecContext(ctx, `INSERT INTO share_hidden (user_id, day_id) VALUES (?, ?)
			ON CONFLICT(user_id, day_id) DO NOTHING`, viewerID, dayID)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM share_hidden WHERE user_id = ? AND day_id = ?`, viewerID, dayID)
	}
	if err != nil {
		return fmt.Errorf("set shared day visibility: %w", err)
	}
	return nil
}
