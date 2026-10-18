package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChangePassword(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	u := testUser(t, s, "alice")
	if _, err := db.Exec(`UPDATE users SET must_change_password = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	keep, keepSession, err := s.CreateSession(ctx, u.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.CreateSession(ctx, u.ID, true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	bob := testUser(t, s, "bob")
	bobToken, _, err := s.CreateSession(ctx, bob.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, current, next string
		want                error
	}{
		{"policy", "password123", "short", ErrPasswordPolicy},
		{"unchanged", "password123", "password123", ErrPasswordUnchanged},
	} {
		if err := s.ChangePassword(ctx, "ip", u.ID, keepSession.TokenHash, tc.current, tc.next, testNow); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v", tc.name, err)
		}
	}
	if err := s.ChangePassword(ctx, "ip", u.ID, keepSession.TokenHash, "password123", "new-password-1", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, keep, testNow); err != nil {
		t.Fatal("current session deleted")
	}
	if _, err := s.LookupSession(ctx, other, testNow); !errors.Is(err, ErrSession) {
		t.Fatal("other session kept")
	}
	if _, err := s.LookupSession(ctx, bobToken, testNow); err != nil {
		t.Fatal("another user's session deleted")
	}
	var flag bool
	if err := db.QueryRow(`SELECT must_change_password FROM users WHERE id = ?`, u.ID).Scan(&flag); err != nil || flag {
		t.Fatal("must_change_password not cleared")
	}
	if _, err := s.Authenticate(ctx, "ip2", "alice", "new-password-1", testNow); err != nil {
		t.Fatal("new password rejected")
	}

	if err := s.ChangePassword(ctx, "ip", u.ID, keepSession.TokenHash, "password123", "another-pass", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("old password accepted: %v", err)
	}
	if err := s.ChangePassword(ctx, "ip", u.ID, keepSession.TokenHash, "new-password-1", "another-pass", testNow); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("no backoff after wrong current password: %v", err)
	}
	if err := s.ChangePassword(ctx, "ip", u.ID, keepSession.TokenHash, "new-password-1", "another-pass", testNow.Add(2*time.Second)); err != nil {
		t.Fatalf("backoff did not expire: %v", err)
	}

	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, "ip", bob.ID, "", "password123", "new-password-1", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("inactive user changed password: %v", err)
	}
}

func TestTimezone(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"Asia/Shanghai", true}, {"UTC", true}, {"America/New_York", true},
		{"", false}, {"Local", false}, {"Mars/Olympus", false}, {"../etc/passwd", false},
	} {
		if _, err := ValidateTimezone(tc.name); (err == nil) != tc.ok {
			t.Errorf("ValidateTimezone(%q) err=%v", tc.name, err)
		}
	}

	s, db := testService(t)
	ctx := context.Background()
	u := testUser(t, s, "alice")
	if _, err := s.SetTimezoneIfEmpty(ctx, u.ID, "Nope/Zone", testNow); !errors.Is(err, ErrTimezone) {
		t.Fatalf("invalid timezone accepted: %v", err)
	}
	if set, err := s.SetTimezoneIfEmpty(ctx, u.ID, "Asia/Shanghai", testNow); err != nil || !set {
		t.Fatalf("set=%v err=%v", set, err)
	}
	if set, err := s.SetTimezoneIfEmpty(ctx, u.ID, "Europe/Paris", testNow); err != nil || set {
		t.Fatalf("existing timezone overwritten: set=%v err=%v", set, err)
	}
	var tz string
	if err := db.QueryRow(`SELECT timezone FROM users WHERE id = ?`, u.ID).Scan(&tz); err != nil || tz != "Asia/Shanghai" {
		t.Fatalf("timezone=%q err=%v", tz, err)
	}
}
