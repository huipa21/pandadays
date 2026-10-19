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

// ProfileParams carries profile input. Username is used only when creating a
// user; UpdateProfile never changes it. CurrentPassword and ClientIP are
// required when users change their own email.
type ProfileParams struct {
	Username, DisplayName, Email, Comment string
	Avatar                                []byte
	RemoveAvatar                          bool
	CurrentPassword, ClientIP             string
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
// The username is fixed after creation. Users changing their own email must
// confirm their current password. Preference changes are committed with the
// identity so invalid input cannot partially save.
func (s *Service) UpdateProfile(ctx context.Context, actorID, userID int64, p ProfileParams, lang, timezone string, now time.Time) error {
	p.Email = strings.TrimSpace(p.Email)
	if p.Email == "" {
		return ErrIdentity
	}
	if err := validateProfile(p.DisplayName, p.Comment, p.Avatar); err != nil {
		return err
	}
	if err := validatePreferences(lang, timezone); err != nil {
		return err
	}
	var currentEmail, hash string
	if actorID == userID {
		err := s.db.QueryRowContext(ctx, `SELECT email, password_hash FROM users WHERE id = ? AND is_active = 1`, userID).
			Scan(&currentEmail, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("read profile identity: %w", err)
		}
		if currentEmail != p.Email {
			if err := s.verifyCurrentPassword(p.ClientIP, userID, hash, p.CurrentPassword, now); err != nil {
				return err
			}
		}
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
		AND (username = ? OR email = ?))`, userID, p.Email, p.Email).Scan(&exists); err != nil {
		return fmt.Errorf("check profile identity: %w", err)
	}
	if exists {
		return ErrIdentity
	}
	// For self-service, the email/password checked above must still be current.
	res, err := tx.ExecContext(ctx, `UPDATE users SET display_name = ?, email = ?, comment = ?,
		avatar = CASE WHEN ? THEN NULL WHEN ? THEN ? ELSE avatar END,
		lang = NULLIF(?, ''), timezone = NULLIF(?, ''), updated_at = ?
		WHERE id = ? AND (? OR (email = ? AND password_hash = ?))`,
		strings.TrimSpace(p.DisplayName), p.Email, strings.TrimSpace(p.Comment),
		p.RemoveAvatar, len(p.Avatar) > 0, p.Avatar, lang, timezone, now.Unix(), userID,
		actorID != userID, currentEmail, hash)
	if err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("read profile update result: %w", err)
	} else if n == 0 {
		if actorID == userID {
			return ErrCredentials
		}
		return ErrUserNotFound
	}
	return tx.Commit()
}

// RemoveAvatar clears the user's own avatar.
func (s *Service) RemoveAvatar(ctx context.Context, userID int64, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET avatar = NULL, updated_at = ? WHERE id = ? AND avatar IS NOT NULL`,
		now.Unix(), userID); err != nil {
		return fmt.Errorf("remove avatar: %w", err)
	}
	return nil
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
