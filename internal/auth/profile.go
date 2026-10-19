package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxAvatarBytes = 1 << 20

var ErrProfile = errors.New("invalid profile or avatar")

type ProfileParams struct {
	Username, DisplayName, Email, Comment string
	Avatar                                []byte
	RemoveAvatar                          bool
}

func validateProfile(displayName, comment string, avatar []byte) error {
	if utf8.RuneCountInString(displayName) > 100 || utf8.RuneCountInString(comment) > 10000 {
		return ErrProfile
	}
	if len(avatar) > 0 {
		if len(avatar) > MaxAvatarBytes {
			return ErrProfile
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(avatar))
		if err != nil || (format != "png" && format != "jpeg") ||
			cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
			return ErrProfile
		}
		if _, _, err := image.Decode(bytes.NewReader(avatar)); err != nil {
			return ErrProfile
		}
	}
	return nil
}

// UpdateProfile permits self-service or an active administrator, rechecked in the transaction.
// Preference changes are committed with the identity so invalid input cannot partially save.
func (s *Service) UpdateProfile(ctx context.Context, actorID, userID int64, p ProfileParams, lang, timezone string, now time.Time) error {
	p.Username, p.Email = strings.TrimSpace(p.Username), strings.TrimSpace(p.Email)
	if p.Username == "" || p.Email == "" {
		return ErrIdentity
	}
	if err := validateProfile(p.DisplayName, p.Comment, p.Avatar); err != nil {
		return err
	}
	if err := validatePreferences(lang, timezone); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin profile update: %w", err)
	}
	defer tx.Rollback()
	if actorID != userID {
		if err := CheckAdmin(ctx, tx, actorID); err != nil {
			return err
		}
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id <> ?
		AND (username IN (?, ?) OR email IN (?, ?)))`, userID, p.Username, p.Email, p.Username, p.Email).Scan(&exists); err != nil {
		return fmt.Errorf("check profile identity: %w", err)
	}
	if exists {
		return ErrIdentity
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET username = ?, display_name = ?, email = ?, comment = ?,
		avatar = CASE WHEN ? THEN NULL WHEN ? THEN ? ELSE avatar END,
		lang = NULLIF(?, ''), timezone = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		p.Username, strings.TrimSpace(p.DisplayName), p.Email, strings.TrimSpace(p.Comment),
		p.RemoveAvatar, len(p.Avatar) > 0, p.Avatar, lang, timezone, now.Unix(), userID)
	if err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("read profile update result: %w", err)
	} else if n == 0 {
		return ErrUserNotFound
	}
	return tx.Commit()
}

func (s *Service) Avatar(ctx context.Context, actorID, userID int64) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT avatar FROM users WHERE id = ? AND
		(id = ? OR EXISTS(SELECT 1 FROM users WHERE id = ? AND is_admin = 1 AND is_active = 1 AND must_change_password = 0))`,
		userID, actorID, actorID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(data) == 0) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read avatar: %w", err)
	}
	return data, nil
}
