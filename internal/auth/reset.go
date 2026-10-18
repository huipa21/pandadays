package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUserNotFound = errors.New("user not found")
	ErrNotAdmin     = errors.New("user is not an admin")
)

// ResetAdminPassword sets a random password on the named admin, requires a
// change at next login, and deletes all of the admin's sessions (SPEC §4.4).
// The new password is returned once for printing and only its hash is stored.
func ResetAdminPassword(ctx context.Context, db *sql.DB, username string, now time.Time) (*User, string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, "", ErrUserNotFound
	}
	password, err := RandomPassword()
	if err != nil {
		return nil, "", fmt.Errorf("generate password: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("reset admin password: %w", err)
	}
	defer tx.Rollback()
	var u User
	err = tx.QueryRowContext(ctx, `SELECT id, username, email, is_admin, is_active
		FROM users WHERE username = ?`, username).Scan(&u.ID, &u.Username, &u.Email, &u.IsAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrUserNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("read user: %w", err)
	}
	if !u.IsAdmin {
		return nil, "", ErrNotAdmin
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 1, updated_at = ?
		WHERE id = ?`, hash, now.Unix(), u.ID); err != nil {
		return nil, "", fmt.Errorf("update password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); err != nil {
		return nil, "", fmt.Errorf("delete sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit password reset: %w", err)
	}
	u.MustChangePassword = true
	return &u, password, nil
}
