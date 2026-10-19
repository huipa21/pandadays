package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/db/dbtest"
)

func setupServer(t *testing.T) (*Server, *sql.DB, string) {
	t.Helper()
	db := dbtest.Open(t)
	s := newTestServerWith(t, func(string) string { return "" }, db)
	token, _, err := auth.EnsureSetupToken(context.Background(), db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, db, token
}

func setupForm(csrf *http.Cookie, token string) url.Values {
	return url.Values{"csrf_token": {csrf.Value}, "token": {token}, "username": {"admin"},
		"email": {"admin@example.com"}, "password": {"password123"}, "confirm_password": {"password123"}}
}

func TestSetupValidToken(t *testing.T) {
	s, _, token := setupServer(t)
	h := s.Handler()

	if rr := get(h, "/login", ""); rr.Code != 303 || rr.Header().Get("Location") != "/setup" {
		t.Fatalf("login before setup: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	form := get(h, "/setup", "")
	if form.Code != 200 || !strings.Contains(form.Body.String(), `name="token"`) {
		t.Fatalf("setup form: status=%d", form.Code)
	}
	for _, want := range []string{`name="lang"`, `name="timezone"`, `data-browser-timezone`, `/static/timezone.js`} {
		if !strings.Contains(form.Body.String(), want) {
			t.Errorf("setup form missing %q", want)
		}
	}
	if !strings.Contains(form.Body.String(), `pattern="[a-z0-9-]{4,32}"`) {
		t.Fatal("setup username field is missing its format constraint")
	}
	csrf := anonymousCSRF(t, h)

	rr := postForm(h, "/setup", setupForm(csrf, token), csrf)
	cookie := sessionCookie(rr)
	if rr.Code != 303 || rr.Header().Get("Location") != "/" || cookie == nil {
		t.Fatalf("setup: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	session, err := s.auth.LookupSession(context.Background(), cookie.Value, time.Now())
	if err != nil || !session.User.IsAdmin || session.User.Username != "admin" {
		t.Fatalf("new admin not signed in: %+v err=%v", session, err)
	}
	if rr := get(h, "/login", ""); rr.Code != 200 {
		t.Fatalf("login after setup: status=%d", rr.Code)
	}
}

func TestSetupRejectsInvalidUsername(t *testing.T) {
	s, db, token := setupServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)
	values := setupForm(csrf, token)
	values.Set("username", "Bad_Name")
	rr := postForm(h, "/setup", values, csrf)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "Use 4–32 lowercase letters") {
		t.Fatalf("invalid username: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("invalid username created users=%d err=%v", users, err)
	}
	values.Set("username", "admin")
	if rr := postForm(h, "/setup", values, csrf); rr.Code != http.StatusSeeOther {
		t.Fatalf("valid username after rejection: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSetupPreferences(t *testing.T) {
	for _, tc := range []struct {
		name, lang, timezone string
	}{
		{"browser defaults", "", "Asia/Shanghai"},
		{"explicit preferences", "zh", "Pacific/Kiritimati"},
		{"without javascript", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, token := setupServer(t)
			h := s.Handler()
			csrf := anonymousCSRF(t, h)
			values := setupForm(csrf, token)
			values.Set("lang", tc.lang)
			values.Set("timezone", tc.timezone)
			rr := postForm(h, "/setup", values, csrf)
			cookie := sessionCookie(rr)
			if rr.Code != http.StatusSeeOther || cookie == nil {
				t.Fatalf("setup: status=%d body=%s", rr.Code, rr.Body.String())
			}
			session, err := s.auth.LookupSession(context.Background(), cookie.Value, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if session.User.Lang != tc.lang || session.User.Timezone != tc.timezone {
				t.Fatalf("preferences not saved: %+v", session.User)
			}
			var pending int
			if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'setup_token_%'`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("setup token rows=%d err=%v", pending, err)
			}
			req := httptest.NewRequest(http.MethodGet, "/settings", nil)
			req.Header.Set("Accept-Language", "zh-CN,en;q=0.8")
			req.AddCookie(cookie)
			settings := httptest.NewRecorder()
			h.ServeHTTP(settings, req)
			if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `<html lang="zh">`) {
				t.Fatalf("settings language: status=%d body=%s", settings.Code, settings.Body.String())
			}
		})
	}
}

func TestSetupInvalidPreferencesKeepToken(t *testing.T) {
	s, db, token := setupServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)
	for _, tc := range []struct {
		lang, timezone, message string
	}{
		{"fr", "Asia/Shanghai", "Choose a supported language."},
		{"zh", "Mars/Olympus", "Choose a valid IANA timezone."},
		{"en", "Local", "Choose a valid IANA timezone."},
	} {
		values := setupForm(csrf, token)
		values.Set("lang", tc.lang)
		values.Set("timezone", tc.timezone)
		rr := postForm(h, "/setup", values, csrf)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), tc.message) {
			t.Fatalf("invalid preferences: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `value="`+tc.timezone+`"`) {
			t.Fatal("timezone lost on validation error")
		}
		var users int
		if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != 0 {
			t.Fatalf("users=%d err=%v", users, err)
		}
	}
	if rr := postForm(h, "/setup", setupForm(csrf, token), csrf); rr.Code != http.StatusSeeOther {
		t.Fatalf("token consumed by invalid preferences: status=%d", rr.Code)
	}
}
func TestSetupReusedTokenAndPostAdmin404(t *testing.T) {
	s, _, token := setupServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)
	if rr := postForm(h, "/setup", setupForm(csrf, token), csrf); rr.Code != 303 {
		t.Fatalf("first setup: status=%d", rr.Code)
	}

	if rr := get(h, "/setup", ""); rr.Code != 404 {
		t.Fatalf("GET after admin exists: status=%d", rr.Code)
	}
	reuse := setupForm(csrf, token)
	reuse.Set("username", "mallory")
	reuse.Set("email", "mallory@example.com")
	if rr := postForm(h, "/setup", reuse, csrf); rr.Code != 404 {
		t.Fatalf("reused token: status=%d", rr.Code)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("users=%d err=%v", n, err)
	}
}

func TestSetupExistingAdmin404(t *testing.T) {
	db := dbtest.Open(t)
	s := newTestServerWith(t, func(string) string { return "" }, db)
	if _, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{Username: "root",
		Email: "root@example.com", Password: "password123", IsAdmin: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if rr := get(h, "/setup", ""); rr.Code != 404 {
		t.Fatalf("GET: status=%d", rr.Code)
	}
	csrf := anonymousCSRF(t, h)
	if rr := postForm(h, "/setup", setupForm(csrf, "anything"), csrf); rr.Code != 404 {
		t.Fatalf("POST: status=%d", rr.Code)
	}
}

func TestSetupRejectsInvalidAndExpiredToken(t *testing.T) {
	s, db, token := setupServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)

	rr := postForm(h, "/setup", setupForm(csrf, "not-the-token"), csrf)
	if rr.Code != 403 || !strings.Contains(rr.Body.String(), "invalid or has expired") ||
		!strings.Contains(rr.Body.String(), `value="admin@example.com"`) {
		t.Fatalf("wrong token: status=%d", rr.Code)
	}

	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'setup_token_expires_at'`,
		time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	expired := setupForm(csrf, token)
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(expired.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.7:4444" // avoid the previous failure's backoff
	req.AddCookie(csrf)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != 403 {
		t.Fatalf("expired token: status=%d", r.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("users=%d err=%v", n, err)
	}
}

func TestSetupValidationErrors(t *testing.T) {
	s, _, token := setupServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)

	mismatch := setupForm(csrf, token)
	mismatch.Set("confirm_password", "different123")
	if rr := postForm(h, "/setup", mismatch, csrf); rr.Code != 422 {
		t.Fatalf("mismatch: status=%d", rr.Code)
	}
	weak := setupForm(csrf, token)
	weak.Set("password", "short")
	weak.Set("confirm_password", "short")
	if rr := postForm(h, "/setup", weak, csrf); rr.Code != 422 || !strings.Contains(rr.Body.String(), "8–32") {
		t.Fatalf("weak password: status=%d", rr.Code)
	}
	noCSRF := setupForm(csrf, token)
	noCSRF.Del("csrf_token")
	if rr := postForm(h, "/setup", noCSRF, csrf); rr.Code != 403 {
		t.Fatalf("missing CSRF: status=%d", rr.Code)
	}
	if rr := postForm(h, "/setup", setupForm(csrf, token), csrf); rr.Code != 303 {
		t.Fatalf("token consumed by failed attempts: status=%d", rr.Code)
	}
}
