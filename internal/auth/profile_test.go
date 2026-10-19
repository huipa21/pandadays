package auth

import (
	"bytes"
	"context"
	"errors"
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
		{"duplicate identity", ProfileParams{Username: actor.Email, Email: target.Email}, "", "", ErrIdentity},
		{"empty identity", ProfileParams{Username: "", Email: target.Email}, "", "", ErrIdentity},
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
