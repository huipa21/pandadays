package auth

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func adminActor(t *testing.T, s *Service, db *sql.DB) *User {
	t.Helper()
	u := testUser(t, s, "admin")
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	u.IsAdmin = true
	return u
}

func TestAdminCreateAndReset(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	actor := adminActor(t, s, db)
	u, password, err := s.AdminCreateUser(ctx, actor.ID, CreateUserParams{
		Username: " Bob ", Email: " bob@example.com ", IsAdmin: true,
		Password: "must-not-be-used",
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 16 || !u.MustChangePassword || !u.IsAdmin || u.Username != "Bob" {
		t.Fatalf("created user=%+v password length=%d", u, len(password))
	}
	logged, err := s.Authenticate(ctx, "127.0.0.1", "Bob", password, testNow)
	if err != nil || !logged.MustChangePassword {
		t.Fatalf("random password login: user=%+v err=%v", logged, err)
	}
	for _, table := range []string{"collections", "tags"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE owner_id = ? AND is_reserved = 1`, u.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("reserved %s count=%d err=%v", table, n, err)
		}
	}
	if _, _, err := s.AdminCreateUser(ctx, actor.ID, CreateUserParams{
		Username: "BOB@example.com", Email: "other@example.com",
	}, testNow); !errors.Is(err, ErrIdentity) {
		t.Fatalf("cross-identity collision: %v", err)
	}
	token, _, err := s.CreateSession(ctx, u.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	name, next, err := s.AdminResetPassword(ctx, actor.ID, u.ID, testNow)
	if err != nil || len(next) != 16 || next == password || name != "Bob" {
		t.Fatalf("reset password length=%d name=%q err=%v", len(next), name, err)
	}
	if _, err := s.LookupSession(ctx, token, testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("reset retained session: %v", err)
	}
	if _, err := s.Authenticate(ctx, "other-ip", "Bob", password, testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("old password accepted: %v", err)
	}
	if _, err := s.Authenticate(ctx, "third-ip", "Bob", next, testNow); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == next {
		t.Fatal("raw password stored")
	}
}

func TestAdminProtectionAndActivation(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	actor := adminActor(t, s, db)
	target := testUser(t, s, "target")
	root := t.TempDir()
	if err := s.AdminSetActive(ctx, actor.ID, actor.ID, false, testNow); !errors.Is(err, ErrAdminProtected) {
		t.Fatalf("self deactivation: %v", err)
	}
	if err := s.AdminDeleteUser(ctx, actor.ID, actor.ID, root); !errors.Is(err, ErrAdminProtected) {
		t.Fatalf("self deletion: %v", err)
	}
	// Exercise the last-admin invariant independently of the actor authorization guard.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectAdmin(ctx, tx, target.ID, actor.ID); !errors.Is(err, ErrAdminProtected) {
		t.Fatalf("last active admin: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(ctx, target.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdminSetActive(ctx, actor.ID, target.ID, false, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, token, testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("inactive session: %v", err)
	}
	if _, err := s.Authenticate(ctx, "ip", target.Username, "password123", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("inactive login: %v", err)
	}
	if err := s.AdminSetActive(ctx, actor.ID, target.ID, true, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, token, testNow); !errors.Is(err, ErrSession) {
		t.Fatalf("reactivation revived session: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminSetActive(ctx, actor.ID, target.ID, false, testNow); err != nil {
		t.Fatalf("deactivate second admin: %v", err)
	}
	if err := s.AdminDeleteUser(ctx, actor.ID, target.ID, root); err != nil {
		t.Fatalf("delete inactive admin: %v", err)
	}
}

func TestAdminMutationsRecheckActor(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	actor := adminActor(t, s, db)
	target := testUser(t, s, "target")
	for _, state := range []string{
		"is_admin = 0", "is_admin = 1, is_active = 0", "is_active = 1, must_change_password = 1",
	} {
		if _, err := db.Exec(`UPDATE users SET `+state+` WHERE id = ?`, actor.ID); err != nil {
			t.Fatal(err)
		}
		checks := []struct {
			name string
			call func() error
		}{
			{"create", func() error {
				_, _, err := s.AdminCreateUser(ctx, actor.ID, CreateUserParams{Username: "new", Email: "new@example.com"}, testNow)
				return err
			}},
			{"reset", func() error { _, _, err := s.AdminResetPassword(ctx, actor.ID, target.ID, testNow); return err }},
			{"activate", func() error { return s.AdminSetActive(ctx, actor.ID, target.ID, false, testNow) }},
			{"quotas", func() error { return s.AdminSetQuotas(ctx, actor.ID, target.ID, QuotaOverrides{}, testNow) }},
			{"delete", func() error { return s.AdminDeleteUser(ctx, actor.ID, target.ID, t.TempDir()) }},
			{"settings", func() error { return s.AdminSaveSettings(ctx, actor.ID, SiteSettings{SessionDays: 30}) }},
		}
		for _, check := range checks {
			if err := check.call(); !errors.Is(err, ErrNotAdmin) {
				t.Errorf("%s (%s): %v", check.name, state, err)
			}
		}
	}
}

func TestAdminDeleteMediaRollback(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	actor := adminActor(t, s, db)
	target := testUser(t, s, "target")
	root := t.TempDir()
	dir := filepath.Join(root, strconv.FormatInt(target.ID, 10))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "attachment")
	if err := os.WriteFile(path, []byte("kept on rollback"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Force a COMMIT failure after media has been staged.
	if _, err := db.Exec(`CREATE TABLE deletion_blocker (
		user_id INTEGER REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO deletion_blocker(user_id) VALUES (?)`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminDeleteUser(ctx, actor.ID, target.ID, root); err == nil {
		t.Fatal("expected deletion commit failure")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "kept on rollback" {
		t.Fatalf("media not restored: %q err=%v", data, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, target.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("user not rolled back: count=%d err=%v", n, err)
	}
	if _, err := db.Exec(`DROP TABLE deletion_blocker`); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminDeleteUser(ctx, actor.ID, target.ID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media retained: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged media retained: entries=%v err=%v", entries, err)
	}
}

func TestAdminSettingsValidationAndSessionLifetime(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	actor := adminActor(t, s, db)
	original, err := s.SiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*SiteSettings){
		func(v *SiteSettings) { v.Language = "fr" },
		func(v *SiteSettings) { v.SessionDays = 0 },
		func(v *SiteSettings) { v.SessionDays = 106752 },
		func(v *SiteSettings) { v.QuotaDays = -1 },
		func(v *SiteSettings) { v.QuotaBytes = -1 },
		func(v *SiteSettings) { v.MaxCardBytes = -1 },
		func(v *SiteSettings) { v.BackupRetention = -1 },
	} {
		v := original
		modify(&v)
		if err := s.AdminSaveSettings(ctx, actor.ID, v); !errors.Is(err, ErrSettingInvalid) {
			t.Errorf("invalid settings %+v: %v", v, err)
		}
		got, err := s.SiteSettings(ctx)
		if err != nil || got != original {
			t.Fatalf("invalid settings persisted: %+v err=%v", got, err)
		}
	}
	v := SiteSettings{Language: "zh", SessionDays: 7, QuotaDays: 2, QuotaBytes: 100, MaxCardBytes: 20, BackupRetention: 0}
	if err := s.AdminSaveSettings(ctx, actor.ID, v); err != nil {
		t.Fatal(err)
	}
	got, err := s.SiteSettings(ctx)
	if err != nil || got != v {
		t.Fatalf("settings=%+v err=%v", got, err)
	}
	for _, persistent := range []bool{true, false} {
		_, session, err := s.CreateSession(ctx, actor.ID, persistent, testNow)
		if err != nil {
			t.Fatal(err)
		}
		want := 12 * time.Hour
		if persistent {
			want = 7 * 24 * time.Hour
		}
		if session.ExpiresAt.Sub(testNow) != want {
			t.Errorf("session duration=%s want=%s", session.ExpiresAt.Sub(testNow), want)
		}
	}
}
