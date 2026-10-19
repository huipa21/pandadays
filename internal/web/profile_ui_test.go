package web

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
)

func profilePost(t *testing.T, s *Server, path, token string, fields url.Values, avatar []byte, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if csrf {
		if err := writer.WriteField("csrf_token", sessionCSRF(t, s, token)); err != nil {
			t.Fatal(err)
		}
	}
	if avatar != nil {
		part, err := writer.CreateFormFile("avatar", "avatar.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(avatar); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func TestProfileAndUserInterface(t *testing.T) {
	s, _, actor, token := authenticatedServer(t)
	target, regularToken := secondUser(t, s)
	makeAdmin(t, s, actor.ID)
	fields := url.Values{"username": {"newpanda"}, "display_name": {"New Panda"}, "email": {"new@example.com"}, "comment": {"Private notes"}}
	var avatar bytes.Buffer
	if err := png.Encode(&avatar, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	rr := profilePost(t, s, "/admin/users", token, fields, avatar.Bytes(), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("multipart create: %d %s", rr.Code, rr.Body.String())
	}
	temporaryPassword(t, rr.Body.String())
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE username = 'newpanda'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/admin/users/" + strconv.FormatInt(id, 10)
	for _, lang := range []string{"en", "zh"} {
		body := get(s.Handler(), "/admin/users?lang="+lang, token).Body.String()
		for _, want := range []string{`class="search-field"`, `type="search"`, `class="btn icon-button"`,
			`class="user-row-link" href="` + path + `"`, "New Panda", `<time datetime=`,
			`action="` + path + `/reset-password"`, `action="` + path + `/deactivate"`, `href="` + path + `/delete"`, `hx-target="#app-dialog"`, `href="` + path + `/edit"`} {
			if !strings.Contains(body, want) {
				t.Errorf("list %s missing %s", lang, want)
			}
		}
		if strings.Contains(body, "Private notes") || strings.Contains(body, "new@example.com") || strings.Contains(body, `class="search-controls"`) {
			t.Error("list is not compact or still has search button")
		}
		body = get(s.Handler(), path+"?lang="+lang, token).Body.String()
		for _, want := range []string{"New Panda", "Private notes", "new@example.com", `name="quota_mb"`,
			`/reset-password"`, `class="icon-button" href="/admin/users"`, `/avatar"`} {
			if !strings.Contains(body, want) {
				t.Errorf("detail %s missing %s", lang, want)
			}
		}
	}
	if body := get(s.Handler(), "/admin/users?q=NEW+PANDA", token).Body.String(); !strings.Contains(body, path) {
		t.Fatal("display name search failed")
	}
	fields.Set("display_name", "Edited Panda")
	if rr := profilePost(t, s, path+"/edit", token, fields, nil, true); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != path {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body.String())
	}
	if body := get(s.Handler(), path+"/edit", token).Body.String(); !strings.Contains(body, `value="Edited Panda"`) {
		t.Fatal("edit form not populated")
	}
	if rr := sessionPost(t, s, path+"/reset-password", token, nil); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), `name="return_to" value="`+path+`"`) ||
		strings.Contains(rr.Body.String(), `name="return_to" value="`+path+`/reset-password"`) {
		t.Fatal("password response must return language and status actions to the canonical user page")
	}
	avatarPath := "/users/" + strconv.FormatInt(id, 10) + "/avatar"
	if rr := get(s.Handler(), avatarPath, token); rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), avatar.Bytes()) ||
		rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("admin avatar response")
	}
	if rr := get(s.Handler(), avatarPath, regularToken); rr.Code != http.StatusNotFound {
		t.Fatal("avatar leaked to another user")
	}
	for _, route := range []string{path + "/edit", "/admin/users"} {
		if rr := profilePost(t, s, route, regularToken, fields, nil, true); rr.Code != http.StatusNotFound {
			t.Fatalf("non-admin write: %s %d", route, rr.Code)
		}
		if rr := profilePost(t, s, route, token, fields, nil, false); rr.Code != http.StatusForbidden {
			t.Fatalf("CSRF bypass: %s %d", route, rr.Code)
		}
	}
	if rr := get(s.Handler(), path+"/edit", regularToken); rr.Code != http.StatusNotFound {
		t.Fatal("non-admin edit form")
	}
	fields.Set("email", actor.Email)
	if rr := profilePost(t, s, path+"/edit", token, fields, nil, true); rr.Code != http.StatusUnprocessableEntity {
		t.Fatal("duplicate edit accepted")
	}
	fields.Set("email", "new@example.com")
	if rr := profilePost(t, s, path+"/edit", token, fields, []byte("<svg/>"), true); rr.Code != http.StatusUnprocessableEntity {
		t.Fatal("invalid avatar accepted")
	}
	if rr := profilePost(t, s, path+"/edit", token, fields, make([]byte, auth.MaxAvatarBytes+(128<<10)), true); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form: %d", rr.Code)
	}
	profile := url.Values{"username": {"Renamed"}, "email": {target.Email}, "display_name": {"My Name"}, "timezone": {"Asia/Shanghai"}}
	if rr := profilePost(t, s, "/profile", regularToken, profile, avatar.Bytes(), true); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/profile" {
		t.Fatalf("profile save: %d %s", rr.Code, rr.Body.String())
	}
	saved, err := s.auth.LookupSession(context.Background(), regularToken, time.Now())
	if err != nil || saved.User.DisplayName != "My Name" || !saved.User.HasAvatar ||
		saved.User.Username != target.Username || saved.User.Timezone == "Asia/Shanghai" {
		t.Fatalf("profile not persisted as expected: %+v", saved.User)
	}
	body := get(s.Handler(), "/profile", regularToken).Body.String()
	for _, want := range []string{`value="` + target.Username + `" readonly`, `value="My Name"`, `name="email"`, `name="current_password"`,
		`name="avatar"`, `class="avatar-box"`, `class="avatar-remove"`, `action="/profile/avatar/delete"`, `class="password-row"`, `href="/password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("profile missing %s", want)
		}
	}
	for _, unwanted := range []string{`name="username"`, `name="timezone"`, "<progress", `href="/settings/export"`, `action="/settings/import"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("profile should not include %s", unwanted)
		}
	}
	if strings.Contains(body, "User settings") || strings.Contains(body, `<select name="lang">`) {
		t.Error("old subtitle/language dropdown remains")
	}
}

func TestHeaderLanguageToggle(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	form := url.Values{"lang": {"zh"}, "return_to": {"/settings?lang=en&filter=kept"}}
	rr := sessionPost(t, s, "/settings/language", token, form)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/settings?filter=kept" {
		t.Fatalf("language redirect: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if body := get(s.Handler(), "/settings", token).Body.String(); !strings.Contains(body, `<html lang="zh">`) {
		t.Fatal("language not persisted")
	}
	for _, target := range []string{"https://example.org", "//example.org", "/\\example.org", "/\nexample.org", ""} {
		form.Set("return_to", target)
		if rr := sessionPost(t, s, "/settings/language", token, form); rr.Header().Get("Location") != "/" {
			t.Fatalf("unsafe redirect accepted: %q", target)
		}
	}
	form.Set("lang", "fr")
	if rr := sessionPost(t, s, "/settings/language", token, form); rr.Code != http.StatusUnprocessableEntity {
		t.Fatal("unsupported language accepted")
	}
	form.Del("csrf_token")
	if rr := postForm(s.Handler(), "/settings/language", form, &http.Cookie{Name: auth.SessionCookieName, Value: token}); rr.Code != http.StatusForbidden {
		t.Fatal("language CSRF bypass")
	}
}
