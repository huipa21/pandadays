package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pandadays/internal/i18n"
)

var (
	ErrAdminProtected = errors.New("cannot remove yourself or the last active admin")
	ErrQuotaInvalid   = errors.New("quotas must be nonnegative integers")
	ErrSettingInvalid = errors.New("invalid site setting")
)

type QuotaOverrides struct {
	Days      sql.NullInt64
	Bytes     sql.NullInt64
	CardBytes sql.NullInt64
}

func (q QuotaOverrides) Validate() error {
	for _, n := range []sql.NullInt64{q.Days, q.Bytes, q.CardBytes} {
		if n.Valid && n.Int64 < 0 {
			return ErrQuotaInvalid
		}
	}
	return nil
}

type AdminUser struct {
	User
	QuotaOverrides
	UsedDays, UsedBytes                               int64
	EffectiveDays, EffectiveBytes, EffectiveCardBytes int64
}

// CheckAdmin rechecks the actor inside a write transaction, not just at routing time.
func CheckAdmin(ctx context.Context, tx *sql.Tx, actorID int64) error {
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM users WHERE id = ? AND is_admin = 1 AND is_active = 1
		AND must_change_password = 0)`, actorID).Scan(&allowed); err != nil {
		return fmt.Errorf("check admin: %w", err)
	}
	if !allowed {
		return ErrNotAdmin
	}
	return nil
}

func (s *Service) AdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.username, u.email, u.is_admin, u.is_active,
		u.must_change_password, u.quota_days, u.quota_bytes, u.max_card_bytes, u.used_bytes,
		(SELECT COUNT(*) FROM days d WHERE d.owner_id = u.id),
		COALESCE(u.quota_days, (SELECT value FROM settings WHERE key = 'default_quota_days')),
		COALESCE(u.quota_bytes, (SELECT value FROM settings WHERE key = 'default_quota_bytes')),
		COALESCE(u.max_card_bytes, (SELECT value FROM settings WHERE key = 'default_max_card_bytes')),
		u.display_name, u.comment, u.created_at, COALESCE(length(u.avatar), 0) > 0,
		COALESCE(u.lang, ''), COALESCE(u.timezone, '')
		FROM users u ORDER BY u.username COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list admin users: %w", err)
	}
	defer rows.Close()
	var users []AdminUser
	for rows.Next() {
		var u AdminUser
		var created int64
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.IsAdmin, &u.IsActive, &u.MustChangePassword,
			&u.Days, &u.Bytes, &u.CardBytes, &u.UsedBytes, &u.UsedDays,
			&u.EffectiveDays, &u.EffectiveBytes, &u.EffectiveCardBytes,
			&u.DisplayName, &u.Comment, &created, &u.HasAvatar, &u.Lang, &u.Timezone); err != nil {
			return nil, fmt.Errorf("scan admin user: %w", err)
		}
		u.CreatedAt = time.Unix(created, 0).UTC()
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Service) AdminCreateUser(ctx context.Context, actorID int64, p CreateUserParams, now time.Time) (*User, string, error) {
	p.Username, p.Email = strings.TrimSpace(p.Username), strings.TrimSpace(p.Email)
	if p.Username == "" || p.Email == "" {
		return nil, "", ErrIdentity
	}
	password, err := RandomPassword()
	if err != nil {
		return nil, "", fmt.Errorf("generate password: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}
	p.MustChangePassword = true
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return nil, "", err
	}
	u, err := insertUser(ctx, tx, p, hash, now)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit admin user creation: %w", err)
	}
	return u, password, nil
}

// AdminResetPassword returns the target's username and the new one-time password.
func (s *Service) AdminResetPassword(ctx context.Context, actorID, userID int64, now time.Time) (string, string, error) {
	password, err := RandomPassword()
	if err != nil {
		return "", "", fmt.Errorf("generate password: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return "", "", err
	}
	var username string
	err = tx.QueryRowContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 1,
		updated_at = ? WHERE id = ? RETURNING username`, hash, now.Unix(), userID).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUserNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("reset user password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return "", "", fmt.Errorf("delete reset user sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit user password reset: %w", err)
	}
	return username, password, nil
}

func protectAdmin(ctx context.Context, tx *sql.Tx, actorID, userID int64) error {
	var admin, active bool
	err := tx.QueryRowContext(ctx, `SELECT is_admin, is_active FROM users WHERE id = ?`, userID).Scan(&admin, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read protected user: %w", err)
	}
	if actorID == userID {
		return ErrAdminProtected
	}
	if admin && active {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1`).Scan(&count); err != nil {
			return fmt.Errorf("count active admins: %w", err)
		}
		if count <= 1 {
			return ErrAdminProtected
		}
	}
	return nil
}

func (s *Service) AdminSetActive(ctx context.Context, actorID, userID int64, active bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	if !active {
		if err := protectAdmin(ctx, tx, actorID, userID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET is_active = ?, updated_at = ? WHERE id = ?`, active, now.Unix(), userID)
	if err != nil {
		return fmt.Errorf("set user active: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrUserNotFound
	}
	if !active {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("delete inactive user sessions: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Service) AdminSetQuotas(ctx context.Context, actorID, userID int64, q QuotaOverrides, now time.Time) error {
	if err := q.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET quota_days = ?, quota_bytes = ?, max_card_bytes = ?,
		updated_at = ? WHERE id = ?`, q.Days, q.Bytes, q.CardBytes, now.Unix(), userID)
	if err != nil {
		return fmt.Errorf("set user quotas: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrUserNotFound
	}
	return tx.Commit()
}

// AdminDeleteUser stages only this user's media directory on the same volume.
// A failed DB transaction restores it; committed deletion removes the staged files.
func (s *Service) AdminDeleteUser(ctx context.Context, actorID, userID int64, mediaRoot string) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	if err := protectAdmin(ctx, tx, actorID, userID); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM days WHERE owner_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, userID); err != nil {
			return fmt.Errorf("delete user data: %w", err)
		}
	}
	dir := filepath.Join(mediaRoot, strconv.FormatInt(userID, 10))
	var staged string
	if _, statErr := os.Lstat(dir); statErr == nil {
		staged, err = os.MkdirTemp(mediaRoot, ".delete-user-*")
		if err != nil {
			return fmt.Errorf("stage user media: %w", err)
		}
		defer func() {
			if staged != "" {
				if cleanupErr := os.RemoveAll(staged); cleanupErr != nil {
					err = errors.Join(err, fmt.Errorf("remove staged user media: %w", cleanupErr))
				}
			}
		}()
		if err := os.Rename(dir, filepath.Join(staged, "media")); err != nil {
			return fmt.Errorf("stage user media: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect user media: %w", statErr)
	}
	if err := tx.Commit(); err != nil {
		// SQLite can leave a transaction open after a failed COMMIT. Keep this
		// connection reserved until rollback, before restoring the media.
		_, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		if staged != "" {
			if restoreErr := os.Rename(filepath.Join(staged, "media"), dir); restoreErr != nil {
				// Keep the staged data for recovery if restoration fails.
				staged = ""
				return errors.Join(fmt.Errorf("commit user deletion: %w", err), rollbackErr,
					fmt.Errorf("restore user media: %w", restoreErr))
			}
		}
		return errors.Join(fmt.Errorf("commit user deletion: %w", err), rollbackErr)
	}
	return nil
}

type SiteSettings struct {
	Language        string
	SessionDays     int64
	QuotaDays       int64
	QuotaBytes      int64
	MaxCardBytes    int64
	BackupRetention int64
}

func (s *Service) SiteSettings(ctx context.Context) (SiteSettings, error) {
	var settings SiteSettings
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM settings WHERE key = 'site_lang'), ''),
		(SELECT value FROM settings WHERE key = 'session_days'),
		(SELECT value FROM settings WHERE key = 'default_quota_days'),
		(SELECT value FROM settings WHERE key = 'default_quota_bytes'),
		(SELECT value FROM settings WHERE key = 'default_max_card_bytes'),
		(SELECT value FROM settings WHERE key = 'backup_retention')`).Scan(&settings.Language,
		&settings.SessionDays, &settings.QuotaDays, &settings.QuotaBytes, &settings.MaxCardBytes, &settings.BackupRetention)
	if err != nil {
		return SiteSettings{}, fmt.Errorf("read site settings: %w", err)
	}
	return settings, nil
}

func (s *Service) AdminSaveSettings(ctx context.Context, actorID int64, v SiteSettings) error {
	if (v.Language != "" && !i18n.IsSupported(v.Language)) || v.SessionDays <= 0 ||
		v.SessionDays > int64((1<<63-1)/(24*time.Hour)) ||
		v.QuotaDays < 0 || v.QuotaBytes < 0 || v.MaxCardBytes < 0 || v.BackupRetention < 0 {
		return ErrSettingInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := CheckAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	values := map[string]string{
		"site_lang":              v.Language,
		"session_days":           strconv.FormatInt(v.SessionDays, 10),
		"default_quota_days":     strconv.FormatInt(v.QuotaDays, 10),
		"default_quota_bytes":    strconv.FormatInt(v.QuotaBytes, 10),
		"default_max_card_bytes": strconv.FormatInt(v.MaxCardBytes, 10),
		"backup_retention":       strconv.FormatInt(v.BackupRetention, 10),
	}
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("save site setting %s: %w", key, err)
		}
	}
	return tx.Commit()
}
