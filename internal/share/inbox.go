package share

import (
	"context"
	"fmt"
	"time"
)

const InboxPageSize = 20

type Invitation struct {
	ShareID       int64
	OwnerUsername string
	TargetType    TargetType
	TargetID      int64
	Title         string
	Status        string
	CreatedAt     time.Time
}

// A revoked or deleted target must not leave an actionable invitation.
const invitationJoins = ` FROM share_recipients sr
	JOIN shares sh ON sh.id = sr.share_id
	JOIN users u ON u.id = sh.owner_id
	LEFT JOIN days d ON sh.target_type = 'day' AND d.id = sh.target_id AND d.owner_id = sh.owner_id
	LEFT JOIN collections c ON sh.target_type = 'collection' AND c.id = sh.target_id AND c.owner_id = sh.owner_id
	WHERE sr.recipient_id = ? AND (d.id IS NOT NULL OR c.id IS NOT NULL)`

func (s *Store) PendingCount(ctx context.Context, viewerID int64) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+invitationJoins+
		` AND sr.status = 'pending'`, viewerID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending invitations: %w", err)
	}
	return count, nil
}

// Inbox uses a share-ID cursor so new invitations do not shift older pages.
func (s *Store) Inbox(ctx context.Context, viewerID, before int64) ([]Invitation, bool, error) {
	query := `SELECT sh.id, u.username, sh.target_type, sh.target_id,
		COALESCE(d.title, c.name), sr.status, sh.created_at` + invitationJoins
	args := []any{viewerID}
	if before > 0 {
		query += ` AND sh.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY sh.id DESC LIMIT ?`
	args = append(args, InboxPageSize+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()
	var items []Invitation
	for rows.Next() {
		var item Invitation
		var created int64
		if err := rows.Scan(&item.ShareID, &item.OwnerUsername, &item.TargetType,
			&item.TargetID, &item.Title, &item.Status, &created); err != nil {
			return nil, false, fmt.Errorf("scan invitation: %w", err)
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate invitations: %w", err)
	}
	more := len(items) > InboxPageSize
	if more {
		items = items[:InboxPageSize]
	}
	return items, more, nil
}

func (s *Store) Respond(ctx context.Context, viewerID, shareID int64, accept bool) error {
	status := "rejected"
	if accept {
		status = "accepted"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE share_recipients SET status = ?
		WHERE recipient_id = ? AND share_id = ? AND share_id IN
		(SELECT sh.id`+invitationJoins+`)`, status, viewerID, shareID, viewerID)
	if err != nil {
		return fmt.Errorf("respond to invitation: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read invitation response result: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
