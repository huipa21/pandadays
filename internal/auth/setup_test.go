package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func setupParams() CreateUserParams {
	return CreateUserParams{Username: "admin", Email: "admin@example.com", Password: "password123"}
}

func countSetupRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'setup_token_%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnsureSetupTokenIssuesOnce(t *testing.T) {
	_, db := testService(t)
	ctx := context.Background()

	token, expires, err := EnsureSetupToken(ctx, db, testNow)
	if err != nil || !validToken(token) || !expires.Equal(testNow.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("token=%q expires=%v err=%v", token, expires, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'setup_token_hash'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || stored != tokenHash(token) {
		t.Fatal("setup token must be stored only as its SHA-256 hash")
	}

	again, current, err := EnsureSetupToken(ctx, db, testNow.Add(59*time.Minute))
	if err != nil || again != "" || !current.Equal(expires) {
		t.Fatalf("unexpired token must not be replaced: token=%q expires=%v err=%v", again, current, err)
	}

	renewed, _, err := EnsureSetupToken(ctx, db, testNow.Add(time.Hour))
	if err != nil || renewed == "" || renewed == token {
		t.Fatalf("expired token must be replaced: token=%q err=%v", renewed, err)
	}
	if countSetupRows(t, db) != 2 {
		t.Fatal("setup token rows must be upserted, not duplicated")
	}
}

func TestEnsureSetupTokenSkippedWithAdmin(t *testing.T) {
	s, db := testService(t)
	p := setupParams()
	p.IsAdmin = true
	if _, err := s.CreateUser(context.Background(), p, testNow); err != nil {
		t.Fatal(err)
	}
	token, expires, err := EnsureSetupToken(context.Background(), db, testNow)
	if err != nil || token != "" || !expires.IsZero() || countSetupRows(t, db) != 0 {
		t.Fatalf("token=%q expires=%v err=%v", token, expires, err)
	}
}

func TestCompleteSetupValid(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	token, _, err := EnsureSetupToken(ctx, db, testNow)
	if err != nil {
		t.Fatal(err)
	}
	p := setupParams()
	p.Username = "  admin "
	p.MustChangePassword = true // ignored: setup chooses its own password
	u, err := s.CompleteSetup(ctx, "192.0.2.1", token, p, testNow.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin || u.MustChangePassword || u.Username != "admin" {
		t.Fatalf("unexpected admin: %+v", u)
	}
	if countSetupRows(t, db) != 0 {
		t.Fatal("setup token not deleted")
	}
	var reserved int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM collections WHERE owner_id = ? AND is_reserved = 1) +
		(SELECT COUNT(*) FROM tags WHERE owner_id = ? AND is_reserved = 1)`, u.ID, u.ID).Scan(&reserved); err != nil || reserved != 2 {
		t.Fatalf("reserved defaults=%d err=%v", reserved, err)
	}
	if _, err := s.Authenticate(ctx, "192.0.2.1", "admin", "password123", testNow); err != nil {
		t.Fatalf("admin cannot log in: %v", err)
	}

	// Reuse: the token is gone and an admin exists.
	if _, err := s.CompleteSetup(ctx, "192.0.2.1", token, setupParams(), testNow); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("reused token: err=%v", err)
	}
	if again, _, err := EnsureSetupToken(ctx, db, testNow.Add(2*time.Hour)); err != nil || again != "" {
		t.Fatal("no token may be issued after setup")
	}
}

func TestCompleteSetupRejectsBadTokens(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()

	if _, err := s.CompleteSetup(ctx, "192.0.2.1", "anything", setupParams(), testNow); !errors.Is(err, ErrSetupToken) {
		t.Fatalf("no token stored: err=%v", err)
	}
	token, _, err := EnsureSetupToken(ctx, db, testNow)
	if err != nil {
		t.Fatal(err)
	}
	other, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	// Separate IPs keep the backoff from masking the result.
	cases := []struct {
		name, ip, token string
		now             time.Time
	}{
		{"wrong", "192.0.2.2", other, testNow},
		{"empty", "192.0.2.3", "", testNow},
		{"expired", "192.0.2.4", token, testNow.Add(time.Hour)},
	}
	for _, tc := range cases {
		if _, err := s.CompleteSetup(ctx, tc.ip, tc.token, setupParams(), tc.now); !errors.Is(err, ErrSetupToken) {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	if admin, _ := s.HasAdmin(ctx); admin {
		t.Fatal("admin created with a bad token")
	}
	if countSetupRows(t, db) != 2 {
		t.Fatal("rejected attempts must not consume the token")
	}
}

func TestCompleteSetupValidationKeepsToken(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	token, _, err := EnsureSetupToken(ctx, db, testNow)
	if err != nil {
		t.Fatal(err)
	}
	weak := setupParams()
	weak.Password = "short"
	if _, err := s.CompleteSetup(ctx, "192.0.2.1", token, weak, testNow); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("weak password: err=%v", err)
	}
	same := setupParams()
	same.Email = "  "
	if _, err := s.CompleteSetup(ctx, "192.0.2.1", token, same, testNow); !errors.Is(err, ErrIdentity) {
		t.Fatalf("colliding identity: err=%v", err)
	}
	if countSetupRows(t, db) != 2 {
		t.Fatal("failed validation must not consume the token")
	}
	if _, err := s.CompleteSetup(ctx, "192.0.2.1", token, setupParams(), testNow); err != nil {
		t.Fatalf("token unusable after validation errors: %v", err)
	}
}

func TestCompleteSetupRateLimited(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	token, _, err := EnsureSetupToken(ctx, db, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteSetup(ctx, "192.0.2.9", "wrong", setupParams(), testNow); !errors.Is(err, ErrSetupToken) {
		t.Fatal(err)
	}
	if _, err := s.CompleteSetup(ctx, "192.0.2.9", token, setupParams(), testNow); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("backoff not applied: err=%v", err)
	}
	if _, err := s.CompleteSetup(ctx, "192.0.2.9", token, setupParams(), testNow.Add(time.Second)); err != nil {
		t.Fatalf("after backoff: %v", err)
	}
}
