package auth

import (
	"context"
	"errors"
	"testing"
)

func TestResetAdminPassword(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, CreateUserParams{
		Username: "admin", Email: "admin@example.com", Password: "password123", IsAdmin: true,
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	bob := testUser(t, s, "bob")
	adminToken, _, err := s.CreateSession(ctx, admin.ID, true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	bobToken, _, err := s.CreateSession(ctx, bob.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, username string
		want           error
	}{
		{"empty", "  ", ErrUserNotFound},
		{"unknown", "nobody", ErrUserNotFound},
		{"email is not a username", "admin@example.com", ErrUserNotFound},
		{"non-admin", "bob1", ErrNotAdmin},
	} {
		if _, _, err := ResetAdminPassword(ctx, db, tc.username, testNow); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := s.LookupSession(ctx, bobToken, testNow); err != nil {
		t.Fatal("non-admin reset attempt touched bob's session")
	}

	u, password, err := ResetAdminPassword(ctx, db, " admin ", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != admin.ID || !u.MustChangePassword || len(password) != 16 {
		t.Fatalf("user=%+v password len=%d", u, len(password))
	}
	if _, err := s.LookupSession(ctx, adminToken, testNow); !errors.Is(err, ErrSession) {
		t.Fatal("admin session kept after reset")
	}
	if _, err := s.LookupSession(ctx, bobToken, testNow); err != nil {
		t.Fatal("other user's session deleted")
	}
	if _, err := s.Authenticate(ctx, "ip", "Admin", "password123", testNow); !errors.Is(err, ErrCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	got, err := s.Authenticate(ctx, "ip2", "Admin", password, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MustChangePassword {
		t.Fatal("must_change_password not set")
	}
}
