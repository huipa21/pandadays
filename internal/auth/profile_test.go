package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
)

func profileAvatar(t *testing.T, width int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestProfilePersistenceAuthorizationAndValidation(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	actor, target := testUser(t, s, "Admin"), testUser(t, s, "Target")
	avatar := profileAvatar(t, 2)
	p := ProfileParams{Username: target.Username, Email: target.Email, DisplayName: "Panda", Comment: "Notes", Avatar: avatar}
	if err := s.UpdateProfile(ctx, actor.ID, target.ID, p, "", "", testNow); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("non-admin edit: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProfile(ctx, actor.ID, target.ID, p, "zh", "Asia/Shanghai", testNow); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(ctx, target.ID, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.LookupSession(ctx, token, testNow)
	if err != nil || session.User.DisplayName != "Panda" || session.User.Comment != "Notes" ||
		!session.User.HasAvatar || !session.User.CreatedAt.Equal(testNow) || session.User.Lang != "zh" {
		t.Fatalf("saved profile not loaded: %+v %v", session, err)
	}
	for _, id := range []int64{actor.ID, target.ID} {
		data, err := s.Avatar(ctx, id, target.ID)
		if err != nil || !bytes.Equal(data, avatar) {
			t.Fatalf("authorized avatar: %v", err)
		}
	}
	stranger := testUser(t, s, "Stranger")
	if _, err := s.Avatar(ctx, stranger.ID, target.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("other user avatar accessible: %v", err)
	}
	for _, tc := range []struct {
		name           string
		p              ProfileParams
		lang, timezone string
		want           error
	}{
		{"duplicate email", ProfileParams{Email: actor.Email, CurrentPassword: "password123"}, "", "", ErrIdentity},
		{"email matches another username", ProfileParams{Email: actor.Username, CurrentPassword: "password123"}, "", "", ErrIdentity},
		{"empty email", ProfileParams{Email: " "}, "", "", ErrIdentity},
		{"long name", ProfileParams{Username: target.Username, Email: target.Email, DisplayName: strings.Repeat("a", 101)}, "", "", ErrProfile},
		{"long comment", ProfileParams{Username: target.Username, Email: target.Email, Comment: strings.Repeat("a", 10001)}, "", "", ErrProfile},
		{"bad avatar", ProfileParams{Username: target.Username, Email: target.Email, Avatar: []byte("<svg/>")}, "", "", ErrProfile},
		{"large avatar", ProfileParams{Username: target.Username, Email: target.Email, Avatar: make([]byte, MaxAvatarBytes+1)}, "", "", ErrProfile},
		{"wide avatar", ProfileParams{Username: target.Username, Email: target.Email, Avatar: profileAvatar(t, 2049)}, "", "", ErrProfile},
		{"bad language", p, "fr", "", ErrLanguage},
		{"bad timezone", p, "", "Mars/Olympus", ErrTimezone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.UpdateProfile(ctx, target.ID, target.ID, tc.p, tc.lang, tc.timezone, testNow); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			saved, err := s.LookupSession(ctx, token, testNow)
			if err != nil || saved.User.DisplayName != "Panda" || saved.User.Lang != "zh" {
				t.Fatalf("invalid update partially committed: %+v %v", saved, err)
			}
		})
	}
	p.Avatar = nil
	if err := s.UpdateProfile(ctx, target.ID, target.ID, p, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if data, err := s.Avatar(ctx, target.ID, target.ID); err != nil || !bytes.Equal(data, avatar) {
		t.Fatal("empty upload did not preserve avatar")
	}
	p.RemoveAvatar = true
	if err := s.UpdateProfile(ctx, target.ID, target.ID, p, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Avatar(ctx, target.ID, target.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatal("avatar not removed")
	}
}

func TestProfileUsernameFixedAndEmailNeedsPassword(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	user, admin := testUser(t, s, "Panda"), testUser(t, s, "Admin")
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	identity := func() (string, string) {
		t.Helper()
		var username, email string
		if err := db.QueryRow(`SELECT username, email FROM users WHERE id = ?`, user.ID).Scan(&username, &email); err != nil {
			t.Fatal(err)
		}
		return username, email
	}

	if err := s.UpdateProfile(ctx, user.ID, user.ID, ProfileParams{Username: "Renamed", Email: user.Email}, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if username, _ := identity(); username != "Panda" {
		t.Fatalf("username changed to %q", username)
	}
	for i, password := range []string{"", "wrong-password"} {
		p := ProfileParams{Email: "new@example.com", CurrentPassword: password, ClientIP: fmt.Sprintf("192.0.2.%d", i+1)}
		if err := s.UpdateProfile(ctx, user.ID, user.ID, p, "", "", testNow); !errors.Is(err, ErrCredentials) {
			t.Fatalf("email change with password %q: %v", password, err)
		}
	}
	if _, email := identity(); email != user.Email {
		t.Fatalf("email changed without password: %q", email)
	}
	p := ProfileParams{Email: "new@example.com", CurrentPassword: "password123", ClientIP: "192.0.2.9"}
	if err := s.UpdateProfile(ctx, user.ID, user.ID, p, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if username, email := identity(); username != "Panda" || email != "new@example.com" {
		t.Fatalf("identity = %q %q", username, email)
	}
	// Administrators manage other users' email without their password, but cannot rename them.
	p = ProfileParams{Username: "AdminRenamed", Email: "admin-set@example.com"}
	if err := s.UpdateProfile(ctx, admin.ID, user.ID, p, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if username, email := identity(); username != "Panda" || email != "admin-set@example.com" {
		t.Fatalf("admin identity update = %q %q", username, email)
	}
}

func TestRemoveAvatar(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	user := testUser(t, s, "Panda")
	p := ProfileParams{Email: user.Email, Avatar: profileAvatar(t, 1)}
	if err := s.UpdateProfile(ctx, user.ID, user.ID, p, "", "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAvatar(ctx, user.ID, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Avatar(ctx, user.ID, user.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("avatar not removed: %v", err)
	}
}

func TestAdminCreateProfile(t *testing.T) {
	s, db := testService(t)
	actor := testUser(t, s, "Admin")
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, actor.ID); err != nil {
		t.Fatal(err)
	}
	u, _, err := s.AdminCreateUser(context.Background(), actor.ID, CreateUserParams{
		Username: "New", Email: "new@example.com", DisplayName: " New Panda ", Comment: " Note ", Avatar: profileAvatar(t, 1),
	}, testNow)
	if err != nil || u.DisplayName != "New Panda" || u.Comment != "Note" || !u.HasAvatar {
		t.Fatalf("create profile: %+v %v", u, err)
	}
}
