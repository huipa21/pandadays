package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"pandadays/internal/db/dbtest"
)

var testNow = time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)

func testService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := dbtest.Open(t)
	s, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func testUser(t *testing.T, s *Service, name string) *User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), CreateUserParams{
		Username: name, Email: name + "@example.com", Password: "password123",
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		valid          bool
	}{
		{"empty", "", false},
		{"short", "1234567", false},
		{"minimum", "12345678", true},
		{"maximum", strings.Repeat("a", 32), true},
		{"too many runes", strings.Repeat("a", 33), false},
		{"unicode minimum", strings.Repeat("\u732b", 8), true},
		{"72 bytes", strings.Repeat("\u732b", 24), true},
		{"75 bytes", strings.Repeat("\u732b", 25), false},
		{"unicode 32 runes", strings.Repeat("\u00e9", 32), true},
		{"emoji bytes", strings.Repeat("\U0001f43c", 19), false},
		{"invalid utf8", "1234567\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			if !tc.valid {
				if _, err := HashPassword(tc.password); !errors.Is(err, ErrPasswordPolicy) {
					t.Fatalf("hash accepted invalid password: %v", err)
				}
			}
		})
	}
}

func TestPasswordHashAndGenerator(t *testing.T) {
	password := strings.Repeat("\u732b", 24)
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost < 12 {
		t.Fatalf("cost=%d, err=%v", cost, err)
	}
	for _, tc := range []struct {
		password string
		want     bool
	}{{password, true}, {"wrong-password", false}, {strings.Repeat("x", 73), false}, {password + "x", false}} {
		ok, err := CheckPassword(hash, tc.password)
		if err != nil || ok != tc.want {
			t.Fatalf("check=%v, err=%v", ok, err)
		}
	}
	if _, err := CheckPassword("corrupt-hash", password); err == nil {
		t.Fatal("corrupt hash was silently treated as a mismatch")
	}
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		p, err := RandomPassword()
		if err != nil || len(p) != 16 || ValidatePassword(p) != nil || seen[p] {
			t.Fatalf("invalid or repeated generated password, err=%v", err)
		}
		seen[p] = true
	}
}

func TestCreateUserDefaultsAndRollback(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	u := testUser(t, s, "Alice")
	v := testUser(t, s, "Bob")
	for _, id := range []int64{u.ID, v.ID} {
		for _, table := range []string{"collections", "tags"} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+`
				WHERE owner_id = ? AND name = 'Default' AND is_reserved = 1`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s owner %d: count=%d, err=%v", table, id, count, err)
			}
		}
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword(hash, "password123"); err != nil || !ok {
		t.Fatalf("stored password invalid: %v", err)
	}
	for _, p := range []CreateUserParams{
		{Username: "ALICE", Email: "other@example.com"},
		{Username: "other", Email: "ALICE@EXAMPLE.COM"},
		{Username: "Alice@example.com", Email: "other@example.com"},
		{Username: "other", Email: "alice"},
		{Username: " ", Email: "other@example.com"},
	} {
		p.Password = "password123"
		if _, err := s.CreateUser(ctx, p, testNow); !errors.Is(err, ErrIdentity) {
			t.Fatalf("duplicate/empty identity accepted: %v", err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_default_tag BEFORE INSERT ON tags
		BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateUser(ctx, CreateUserParams{Username: "Carol", Email: "carol@example.com", Password: "password123"}, testNow)
	if err == nil {
		t.Fatal("expected reserved tag failure")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("partial user survived: count=%d, err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("partial collection survived: count=%d, err=%v", count, err)
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	u := testUser(t, s, "Alice")
	for _, account := range []string{"ALICE", " Alice@EXAMPLE.COM "} {
		got, err := s.Authenticate(ctx, "192.0.2.1", account, "password123", testNow)
		if err != nil || got.ID != u.ID {
			t.Fatalf("login by username/email: %v", err)
		}
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "alice", "wrong", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "ALICE", "password123", testNow); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("backoff bypassed: %v", err)
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "alice", "password123", testNow.Add(time.Second)); err != nil {
		t.Fatalf("backoff did not expire: %v", err)
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "unknown", "password123", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "alice", "password123", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("inactive user: %v", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	u := testUser(t, s, "Alice")
	v := testUser(t, s, "Bob")
	var tokens []string
	for _, persistent := range []bool{false, true} {
		token, session, err := s.CreateSession(ctx, u.ID, persistent, testNow)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
		want := 12 * time.Hour
		if persistent {
			want = 30 * 24 * time.Hour
		}
		if !validToken(token) || token == session.TokenHash || token == session.CSRFToken ||
			session.User.ID != u.ID || session.Persistent != persistent || !session.ExpiresAt.Equal(testNow.Add(want)) {
			t.Fatal("invalid session shape or lifetime")
		}
		var stored string
		if err := db.QueryRow(`SELECT token_hash FROM sessions WHERE token_hash = ?`, session.TokenHash).Scan(&stored); err != nil || stored != tokenHash(token) {
			t.Fatalf("token not stored hashed: %v", err)
		}
		got, err := s.LookupSession(ctx, token, testNow.Add(time.Hour))
		if err != nil || got.CSRFToken != session.CSRFToken {
			t.Fatalf("lookup failed or CSRF changed: %v", err)
		}
		var lastSeen int64
		if err := db.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash = ?`, session.TokenHash).Scan(&lastSeen); err != nil || lastSeen != testNow.Add(time.Hour).Unix() {
			t.Fatalf("last_seen_at=%d, err=%v", lastSeen, err)
		}
		if _, err := s.LookupSession(ctx, token, testNow.Add(want-time.Second)); err != nil {
			t.Fatalf("session expired too soon: %v", err)
		}
		if _, err := s.LookupSession(ctx, token, testNow.Add(want)); !errors.Is(err, ErrSession) {
			t.Fatalf("session valid at expiry: %v", err)
		}
		rr := httptest.NewRecorder()
		SetSessionCookie(rr, token, session, true, testNow)
		cookie := rr.Result().Cookies()[0]
		if cookie.Name != SessionCookieName || cookie.Value != token || !cookie.HttpOnly ||
			!cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
			t.Fatal("incorrect cookie attributes")
		}
		if persistent {
			if cookie.MaxAge != int(want/time.Second) || !cookie.Expires.Equal(session.ExpiresAt) {
				t.Fatal("persistent cookie lifetime incorrect")
			}
		} else if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
			t.Fatal("non-persistent cookie persisted")
		}
	}
	if tokens[0] == tokens[1] || sessionCSRF(tokens[0]) == sessionCSRF(tokens[1]) {
		t.Fatal("session tokens or CSRF tokens reused")
	}
	other, _, err := s.CreateSession(ctx, v.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserSessions(ctx, u.ID, tokens[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, tokens[0], testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("other session not invalidated: %v", err)
	}
	for _, token := range []string{tokens[1], other} {
		if _, err := s.LookupSession(ctx, token, testNow); err != nil {
			t.Fatalf("current/other user's session invalidated: %v", err)
		}
	}
	if err := s.DeleteSession(ctx, tokens[1]); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{tokens[1], "", "bad-token", strings.Repeat("a", 500)} {
		if _, err := s.LookupSession(ctx, token, testNow); !errors.Is(err, ErrSession) {
			t.Fatalf("invalid token accepted: %v", err)
		}
	}
	if err := s.DeleteUserSessions(ctx, v.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, other, testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("all-session deletion failed: %v", err)
	}
}

func TestSessionSettingsAndInactiveUser(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	u := testUser(t, s, "Alice")
	if _, err := db.Exec(`UPDATE settings SET value = '7' WHERE key = 'session_days'`); err != nil {
		t.Fatal(err)
	}
	token, session, err := s.CreateSession(ctx, u.ID, true, testNow)
	if err != nil || session.ExpiresAt.Sub(testNow) != 7*24*time.Hour {
		t.Fatalf("session_days not applied: %v", err)
	}
	for _, value := range []string{"0", "-1", "invalid", "9999999999999999999", "106752"} {
		if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'session_days'`, value); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateSession(ctx, u.ID, true, testNow); err == nil {
			t.Fatalf("invalid session_days %q accepted", value)
		}
	}
	if _, _, err := s.CreateSession(ctx, u.ID, false, testNow); err != nil {
		t.Fatalf("nonpersistent session depends on session_days: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, token, testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("inactive user's session accepted: %v", err)
	}
	if _, _, err := s.CreateSession(ctx, u.ID, false, testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("created session for inactive user: %v", err)
	}
	if _, _, err := s.CreateSession(ctx, 999999, false, testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("created session for unknown user: %v", err)
	}
	rr := httptest.NewRecorder()
	ClearSessionCookie(rr, false)
	cookie := rr.Result().Cookies()[0]
	if cookie.MaxAge != -1 || cookie.Value != "" || cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatal("incorrect cookie clearing attributes")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	now := testNow
	for i := 0; i < 15; i++ {
		if !l.Allow("ip1", " Alice ", now) {
			t.Fatal("attempt blocked after backoff elapsed")
		}
		l.Failure("ip1", "ALICE", now)
		delay := time.Second << min(i, 10)
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		if l.Allow("ip1", "alice", now.Add(delay-time.Nanosecond)) {
			t.Fatal("backoff shorter than expected")
		}
		if !l.Allow("ip2", "alice", now) || !l.Allow("ip1", "bob", now) {
			t.Fatal("backoff not scoped to IP+account")
		}
		now = now.Add(delay)
	}
	l.Success("ip1", "Alice")
	if !l.Allow("ip1", "alice", now) || len(l.entries) != 0 {
		t.Fatal("success did not reset backoff")
	}
	l.Failure("ip1", "alice", now)
	if !l.Allow("ip1", "alice", now.Add(time.Hour)) || len(l.entries) != 0 {
		t.Fatal("stale entries not pruned")
	}
	for i := 0; i < 10000; i++ {
		l.entries[accountKey("ip", strconv.Itoa(i))] = loginAttempt{touched: now.Add(time.Hour)}
	}
	if l.Allow("new", "account", now.Add(time.Hour)) {
		t.Fatal("entry limit not enforced")
	}
	l.Failure("new", "account", now.Add(time.Hour))
	if len(l.entries) != 10000 {
		t.Fatal("limiter exceeded entry limit")
	}
}
