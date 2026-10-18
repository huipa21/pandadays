package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SetupTokenTTL is how long a printed setup token stays valid (SPEC §4.1).
const SetupTokenTTL = time.Hour

var (
	// ErrSetupClosed means an admin already exists; /setup must 404.
	ErrSetupClosed = errors.New("setup already completed")
	// ErrSetupToken covers missing, wrong, and expired tokens alike.
	ErrSetupToken = errors.New("invalid or expired setup token")
)

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func hasAdmin(ctx context.Context, q querier) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin = 1)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check admin exists: %w", err)
	}
	return exists, nil
}

// readSetupToken returns the stored hash and expiry; ok is false if either row
// is missing or malformed.
func readSetupToken(ctx context.Context, q querier) (hash string, expires time.Time, ok bool, err error) {
	var expiresValue string
	err = q.QueryRowContext(ctx, `SELECT h.value, e.value FROM settings h, settings e
		WHERE h.key = 'setup_token_hash' AND e.key = 'setup_token_expires_at'`).Scan(&hash, &expiresValue)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("read setup token: %w", err)
	}
	unix, err := strconv.ParseInt(expiresValue, 10, 64)
	if err != nil || hash == "" {
		return "", time.Time{}, false, nil
	}
	return hash, time.Unix(unix, 0).UTC(), true, nil
}

// HasAdmin reports whether initial setup has been completed.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	return hasAdmin(ctx, s.db)
}

// EnsureSetupToken issues a new one-time setup token when no admin exists and
// no unexpired token is stored. Only the SHA-256 hash is persisted; the raw
// token is returned once for printing. token is "" when nothing was issued.
func EnsureSetupToken(ctx context.Context, db *sql.DB, now time.Time) (token string, expires time.Time, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("ensure setup token: %w", err)
	}
	defer tx.Rollback()
	if admin, err := hasAdmin(ctx, tx); err != nil || admin {
		return "", time.Time{}, err
	}
	if _, current, ok, err := readSetupToken(ctx, tx); err != nil {
		return "", time.Time{}, err
	} else if ok && now.Before(current) {
		return "", current, nil
	}
	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate setup token: %w", err)
	}
	expires = now.Add(SetupTokenTTL).Truncate(time.Second).UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES
		('setup_token_hash', ?), ('setup_token_expires_at', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		tokenHash(token), strconv.FormatInt(expires.Unix(), 10)); err != nil {
		return "", time.Time{}, fmt.Errorf("store setup token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, fmt.Errorf("commit setup token: %w", err)
	}
	return token, expires, nil
}

// CompleteSetup verifies the setup token and, in one transaction, creates the
// first admin and deletes the token. Failed token checks share the login
// backoff, keyed per client IP.
func (s *Service) CompleteSetup(ctx context.Context, ip, token string, p CreateUserParams, now time.Time) (*User, error) {
	const limiterKey = "\x00setup"
	if admin, err := s.HasAdmin(ctx); err != nil {
		return nil, err
	} else if admin {
		return nil, ErrSetupClosed
	}
	if !s.limiter.Allow(ip, limiterKey, now) {
		return nil, ErrRateLimited
	}
	if ok, err := setupTokenMatches(ctx, s.db, token, now); err != nil {
		return nil, err
	} else if !ok {
		s.limiter.Failure(ip, limiterKey, now)
		return nil, ErrSetupToken
	}
	p.Username, p.Email = strings.TrimSpace(p.Username), strings.TrimSpace(p.Email)
	if p.Username == "" || p.Email == "" {
		return nil, ErrIdentity
	}
	if err := validatePreferences(p.Lang, p.Timezone); err != nil {
		return nil, err
	}
	p.IsAdmin, p.MustChangePassword = true, false
	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("complete setup: %w", err)
	}
	defer tx.Rollback()
	// Re-check under the write lock: a concurrent setup may have won.
	if admin, err := hasAdmin(ctx, tx); err != nil {
		return nil, err
	} else if admin {
		return nil, ErrSetupClosed
	}
	if ok, err := setupTokenMatches(ctx, tx, token, now); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrSetupToken
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings
		WHERE key IN ('setup_token_hash', 'setup_token_expires_at')`); err != nil {
		return nil, fmt.Errorf("delete setup token: %w", err)
	}
	u, err := insertUser(ctx, tx, p, hash, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit setup: %w", err)
	}
	s.limiter.Success(ip, limiterKey)
	return u, nil
}

// setupTokenMatches compares hashes in constant time and rejects expired tokens.
func setupTokenMatches(ctx context.Context, q querier, token string, now time.Time) (bool, error) {
	stored, expires, ok, err := readSetupToken(ctx, q)
	if err != nil || !ok || !now.Before(expires) {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(tokenHash(strings.TrimSpace(token)))) == 1, nil
}
