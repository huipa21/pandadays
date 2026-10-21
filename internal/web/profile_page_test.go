package web

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProfileEmailChangeRequiresPassword(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	if rr := get(s.Handler(), "/profile", ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous profile: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	fields := url.Values{"display_name": {"Panda"}, "email": {"changed@example.com"}}
	if rr := profilePost(t, s, "/profile", token, fields, nil, false); rr.Code != http.StatusForbidden {
		t.Fatalf("CSRF bypass: %d", rr.Code)
	}
	for _, password := range []string{"", "wrong-password"} {
		fields.Set("current_password", password)
		rr := profilePost(t, s, "/profile", token, fields, nil, true)
		body := rr.Body.String()
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "current password") ||
			!strings.Contains(body, `value="changed@example.com"`) {
			t.Fatalf("email change with password %q: %d %s", password, rr.Code, body)
		}
	}
	var email string
	if err := s.db.QueryRow(`SELECT email FROM users WHERE id = ?`, user.ID).Scan(&email); err != nil || email != user.Email {
		t.Fatalf("email changed without password: %q %v", email, err)
	}

	// Display-name-only edits do not need the password.
	fields = url.Values{"display_name": {"Renamed Panda"}, "email": {user.Email}}
	if rr := profilePost(t, s, "/profile", token, fields, nil, true); rr.Code != http.StatusSeeOther {
		t.Fatalf("display name update: %d %s", rr.Code, rr.Body.String())
	}
}

func TestProfileEmailChangeWithPassword(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	fields := url.Values{"display_name": {""}, "email": {"changed@example.com"}, "current_password": {"password123"}}
	if rr := profilePost(t, s, "/profile", token, fields, nil, true); rr.Code != http.StatusSeeOther {
		t.Fatalf("email change: %d %s", rr.Code, rr.Body.String())
	}
	saved, err := s.auth.LookupSession(context.Background(), token, time.Now())
	if err != nil || saved.User.Email != "changed@example.com" || saved.User.Username != user.Username {
		t.Fatalf("email change not saved: %+v %v", saved, err)
	}
}

func TestProfileRemoveAvatar(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	var avatar bytes.Buffer
	if err := png.Encode(&avatar, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"email": {user.Email}}
	if rr := profilePost(t, s, "/profile", token, fields, avatar.Bytes(), true); rr.Code != http.StatusSeeOther {
		t.Fatalf("avatar upload: %d %s", rr.Code, rr.Body.String())
	}
	body := get(s.Handler(), "/profile", token).Body.String()
	if !strings.Contains(body, `data-has-avatar="true"`) || strings.Contains(body, `class="avatar-remove"`) {
		t.Fatal("existing avatar must use the editor without an overlaid remove button")
	}
	if rr := postForm(s.Handler(), "/profile/avatar/delete", url.Values{}, &http.Cookie{Name: "panda_session", Value: token}); rr.Code != http.StatusForbidden {
		t.Fatalf("avatar removal without CSRF: %d", rr.Code)
	}

	if rr := sessionPost(t, s, "/profile/avatar/delete", token, nil); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/profile" {
		t.Fatalf("avatar removal: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	saved, err := s.auth.LookupSession(context.Background(), token, time.Now())
	if err != nil || saved.User.HasAvatar {
		t.Fatalf("avatar still present: %v", err)
	}
}

func TestAvatarEditorForms(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	makeAdmin(t, s, user.ID)
	for _, path := range []string{"/profile", "/admin/users/new", "/admin/users/" + strconv.FormatInt(user.ID, 10) + "/edit"} {
		for _, lang := range []string{"en", "zh"} {
			rr := get(s.Handler(), path+"?lang="+lang, token)
			body := rr.Body.String()
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: %d", path, rr.Code)
			}
			for _, want := range []string{`<details class="avatar-editor"`, `<summary class="avatar-box"`,
				`data-avatar-controls`, `data-avatar-preview`, `data-avatar-input`, `data-avatar-remove`,
				`name="remove_avatar"`, `data-avatar-remove-button hidden`, `data-close-dialog hidden`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing %s", path, want)
				}
			}
			if strings.Count(body, `name="avatar"`) != 1 || strings.Contains(body, `class="avatar-remove"`) {
				t.Errorf("%s must have one avatar input and no overlaid remove button", path)
			}
		}
	}
}

func TestProfileRemoveAvatarOnSave(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	var avatar bytes.Buffer
	if err := png.Encode(&avatar, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"email": {user.Email}}
	if rr := profilePost(t, s, "/profile", token, fields, avatar.Bytes(), true); rr.Code != http.StatusSeeOther {
		t.Fatalf("avatar upload: %d", rr.Code)
	}
	fields.Set("remove_avatar", "1")
	if rr := profilePost(t, s, "/profile", token, fields, nil, true); rr.Code != http.StatusSeeOther {
		t.Fatalf("avatar removal on save: %d %s", rr.Code, rr.Body.String())
	}
	saved, err := s.auth.LookupSession(context.Background(), token, time.Now())
	if err != nil || saved.User.HasAvatar {
		t.Fatalf("avatar still present after save: %v", err)
	}
}
