package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const SessionCookieName = "panda_session"

var ErrSession = errors.New("invalid or expired session")

type Session struct {
	User       User
	TokenHash  string
	CSRFToken  string
	Persistent bool
	ExpiresAt  time.Time
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func sessionCSRF(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("pandadays-csrf"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}

// CreateSession returns the raw token only to the caller, for the cookie.
func (s *Service) CreateSession(ctx context.Context, userID int64, persistent bool, now time.Time) (string, *Session, error) {
	lifetime := 12 * time.Hour
	if persistent {
		var value string
		if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'session_days'`).Scan(&value); err != nil {
			return "", nil, fmt.Errorf("read session_days: %w", err)
		}
		days, err := strconv.ParseInt(value, 10, 64)
		if err != nil || days <= 0 || days > int64((1<<63-1)/(24*time.Hour)) {
			return "", nil, fmt.Errorf("invalid session_days setting")
		}
		lifetime = time.Duration(days) * 24 * time.Hour
	}
	token, err := randomToken()
	if err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	expires := now.Add(lifetime)
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, user_id, persistent, expires_at, created_at, last_seen_at)
		SELECT ?, id, ?, ?, ?, ? FROM users WHERE id = ? AND is_active = 1`,
		tokenHash(token), persistent, expires.Unix(), now.Unix(), now.Unix(), userID)
	if err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", nil, fmt.Errorf("read session insert result: %w", err)
	}
	if n == 0 {
		return "", nil, ErrCredentials
	}
	session, err := s.LookupSession(ctx, token, now)
	if err != nil {
		return "", nil, err
	}
	return token, session, nil
}

func (s *Service) LookupSession(ctx context.Context, token string, now time.Time) (*Session, error) {
	if !validToken(token) {
		return nil, ErrSession
	}
	session := &Session{TokenHash: tokenHash(token), CSRFToken: sessionCSRF(token)}
	var expires int64
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.email,
		u.is_admin, u.is_active, u.must_change_password, COALESCE(u.lang, ''), COALESCE(u.timezone, ''),
		u.display_name, u.comment, u.created_at, COALESCE(length(u.avatar), 0) > 0,
		s.persistent, s.expires_at FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.is_active = 1`,
		session.TokenHash, now.Unix()).Scan(
		&session.User.ID, &session.User.Username, &session.User.Email,
		&session.User.IsAdmin, &session.User.IsActive, &session.User.MustChangePassword,
		&session.User.Lang, &session.User.Timezone, &session.User.DisplayName, &session.User.Comment,
		&created, &session.User.HasAvatar, &session.Persistent, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSession
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	session.ExpiresAt = time.Unix(expires, 0).UTC()
	session.User.CreatedAt = time.Unix(created, 0).UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?
		WHERE token_hash = ? AND user_id = ?`, now.Unix(), session.TokenHash, session.User.ID); err != nil {
		return nil, fmt.Errorf("update session last seen: %w", err)
	}
	return session, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions invalidates all sessions, or all except the current token.
func (s *Service) DeleteUserSessions(ctx context.Context, userID int64, exceptToken string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`,
		userID, tokenHash(exceptToken))
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

func SetSessionCookie(w http.ResponseWriter, token string, session *Session, secure bool, now time.Time) {
	cookie := &http.Cookie{Name: SessionCookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
	if session.Persistent {
		cookie.Expires = session.ExpiresAt
		cookie.MaxAge = int(session.ExpiresAt.Sub(now) / time.Second)
		if cookie.MaxAge <= 0 {
			cookie.MaxAge = -1
		}
	}
	http.SetCookie(w, cookie)
}

func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Path: "/", MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
