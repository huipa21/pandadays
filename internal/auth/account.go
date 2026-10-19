package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
	_ "time/tzdata" // timezone validation must not depend on the host zoneinfo

	"pandadays/internal/i18n"
)

var (
	ErrPasswordUnchanged = errors.New("new password must differ from the current password")
	ErrTimezone          = errors.New("invalid timezone")
	ErrLanguage          = errors.New("invalid language")
)

// ChangePassword verifies the current password, stores the new one, clears
// must_change_password, and deletes every session except keepTokenHash.
// Failed current-password checks share the login backoff, keyed per user.
func (s *Service) ChangePassword(ctx context.Context, ip string, userID int64, keepTokenHash, current, next string, now time.Time) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ? AND is_active = 1`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCredentials
	}
	if err != nil {
		return fmt.Errorf("read password hash: %w", err)
	}
	if err := s.verifyCurrentPassword(ip, userID, hash, current, now); err != nil {
		return err
	}
	if current == next {
		return ErrPasswordUnchanged
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	defer tx.Rollback()
	// Matching the old hash rejects a concurrent change made after verification.
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 0, updated_at = ?
		WHERE id = ? AND password_hash = ? AND is_active = 1`, newHash, now.Unix(), userID, hash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("read password update result: %w", err)
	} else if n == 0 {
		return ErrCredentials
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`,
		userID, keepTokenHash); err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password change: %w", err)
	}
	return nil
}

func passwordLimiterKey(userID int64) string {
	return "\x00password-change:" + strconv.FormatInt(userID, 10)
}

// verifyCurrentPassword re-authenticates a signed-in user for sensitive
// account changes, sharing the login backoff keyed per user.
func (s *Service) verifyCurrentPassword(ip string, userID int64, hash, current string, now time.Time) error {
	key := passwordLimiterKey(userID)
	if current == "" {
		return ErrCredentials
	}
	if !s.limiter.Allow(ip, key, now) {
		return ErrRateLimited
	}
	ok, err := CheckPassword(hash, current)
	if err != nil {
		return fmt.Errorf("verify password hash: %w", err)
	}
	if !ok {
		s.limiter.Failure(ip, key, now)
		return ErrCredentials
	}
	s.limiter.Success(ip, key)
	return nil
}

// ValidateTimezone accepts IANA zone names only (not "" or "Local").
func ValidateTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 64 {
		return nil, ErrTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrTimezone
	}
	return loc, nil
}

// SetTimezoneIfEmpty saves a browser-detected timezone without overriding
// one the user already has. It reports whether the row was updated.
func (s *Service) SetTimezoneIfEmpty(ctx context.Context, userID int64, name string, now time.Time) (bool, error) {
	if _, err := ValidateTimezone(name); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET timezone = ?, updated_at = ?
		WHERE id = ? AND (timezone IS NULL OR timezone = '')`, name, now.Unix(), userID)
	if err != nil {
		return false, fmt.Errorf("set timezone: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read timezone update result: %w", err)
	}
	return n > 0, nil
}

// UpdatePreferences saves the user's language and timezone preferences.
// Empty values allow the site/browser defaults to be used.
func (s *Service) UpdatePreferences(ctx context.Context, userID int64, lang, timezone string, now time.Time) error {
	if err := validatePreferences(lang, timezone); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET lang = NULLIF(?, ''), timezone = NULLIF(?, ''), updated_at = ?
		WHERE id = ?`, lang, timezone, now.Unix(), userID)
	if err != nil {
		return fmt.Errorf("update user preferences: %w", err)
	}
	return nil
}

func validatePreferences(lang, timezone string) error {
	if lang != "" && !i18n.IsSupported(lang) {
		return ErrLanguage
	}
	if timezone != "" {
		if _, err := ValidateTimezone(timezone); err != nil {
			return err
		}
	}
	return nil
}
