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
)

// anonymousCSRF fetches /login to obtain the signed anonymous CSRF cookie.
func anonymousCSRF(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/login", nil))
	for _, c := range rr.Result().Cookies() {
		if c.Name == csrfCookieName {
			return c
		}
	}
	t.Fatal("no anonymous CSRF cookie")
	return nil
}

func postForm(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "192.0.2.10:5555"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func login(h http.Handler, csrf *http.Cookie, account, password string, remember bool) *httptest.ResponseRecorder {
	form := url.Values{"csrf_token": {csrf.Value}, "account": {account}, "password": {password}}
	if remember {
		form.Set("remember", "1")
	}
	return postForm(h, "/login", form, csrf)
}

func sessionCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

func sessionCSRF(t *testing.T, s *Server, token string) string {
	t.Helper()
	session, err := s.auth.LookupSession(context.Background(), token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return session.CSRFToken
}

func get(h http.Handler, path, token string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest("GET", path, token))
	return rr
}

func TestLoginSuccess(t *testing.T) {
	s, _, _, _ := authenticatedServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)

	rr := login(h, csrf, "alice", "password123", false)
	if rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatalf("status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	cookie := sessionCookie(rr)
	if cookie == nil || cookie.MaxAge != 0 || !cookie.Expires.IsZero() || !cookie.HttpOnly {
		t.Fatalf("non-persistent login must set a session cookie: %+v", cookie)
	}
	home := get(h, "/", cookie.Value)
	if home.Code != 200 || !strings.Contains(home.Body.String(), "Alice") ||
		!strings.Contains(home.Body.String(), `action="/logout"`) {
		t.Fatalf("logged-in home not rendered: %d", home.Code)
	}
	if rr := get(h, "/login", cookie.Value); rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatal("logged-in user not redirected away from /login")
	}

	rr = login(h, csrf, "ALICE@example.com", "password123", true)
	persistent := sessionCookie(rr)
	if rr.Code != 303 || persistent == nil || persistent.MaxAge < 29*24*3600 || persistent.MaxAge > 30*24*3600 {
		t.Fatalf("persistent login by email failed: status=%d cookie=%+v", rr.Code, persistent)
	}

	// Logging in again from a browser with a session replaces the old session.
	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"csrf_token": {sessionCSRF(t, s, persistent.Value)}, "account": {"alice"}, "password": {"password123"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: persistent.Value})
	replaced := httptest.NewRecorder()
	h.ServeHTTP(replaced, r)
	if replaced.Code != 303 || sessionCookie(replaced) == nil {
		t.Fatalf("re-login: status=%d", replaced.Code)
	}
	if _, err := s.auth.LookupSession(context.Background(), persistent.Value, time.Now()); err == nil {
		t.Fatal("previous session survived a new login")
	}
}

func TestLoginFailure(t *testing.T) {
	s, _, _, _ := authenticatedServer(t)
	h := s.Handler()
	csrf := anonymousCSRF(t, h)

	wrong := login(h, csrf, "alice", "wrong-password", true)
	unknown := login(h, csrf, "nobody", "wrong-password", true)
	for name, rr := range map[string]*httptest.ResponseRecorder{"wrong": wrong, "unknown": unknown} {
		body := rr.Body.String()
		if rr.Code != 401 || sessionCookie(rr) != nil {
			t.Fatalf("%s: status=%d", name, rr.Code)
		}
		if !strings.Contains(body, "Incorrect username/email or password.") || !strings.Contains(body, `checked`) {
			t.Fatalf("%s: error message or form state missing", name)
		}
	}
	if !strings.Contains(wrong.Body.String(), `value="alice"`) {
		t.Fatal("account not preserved")
	}
	if strings.Contains(wrong.Body.String(), "wrong-password") {
		t.Fatal("password echoed back")
	}

	rr := login(h, csrf, "alice", "password123", false)
	if rr.Code != 429 || sessionCookie(rr) != nil || !strings.Contains(rr.Body.String(), "Too many attempts") {
		t.Fatalf("backoff not applied: status=%d", rr.Code)
	}

	if rr := postForm(h, "/login", url.Values{"account": {"alice"}, "password": {"password123"}}, csrf); rr.Code != 403 {
		t.Fatalf("login without CSRF token: status=%d", rr.Code)
	}

	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"csrf_token": {csrf.Value}, "account": {"nobody"}, "password": {"x"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept-Language", "zh-CN")
	r.AddCookie(csrf)
	zh := httptest.NewRecorder()
	h.ServeHTTP(zh, r)
	if !strings.Contains(zh.Body.String(), "用户名/邮箱或密码错误。") {
		t.Fatal("login error not localized")
	}
}

func TestLoginInactiveUserRejected(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rr := login(h, anonymousCSRF(t, h), "alice", "password123", false)
	if rr.Code != 401 || sessionCookie(rr) != nil || !strings.Contains(rr.Body.String(), "Incorrect username/email or password.") {
		t.Fatalf("inactive user logged in: status=%d", rr.Code)
	}
	if rr := get(h, "/", token); rr.Code != 303 || rr.Header().Get("Location") != "/login" {
		t.Fatal("inactive user's existing session still works")
	}
}

func TestLogout(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	h := s.Handler()
	session := &http.Cookie{Name: auth.SessionCookieName, Value: token}

	if rr := postForm(h, "/logout", url.Values{}, session); rr.Code != 403 {
		t.Fatalf("logout without CSRF: status=%d", rr.Code)
	}
	rr := postForm(h, "/logout", url.Values{"csrf_token": {sessionCSRF(t, s, token)}}, session)
	if rr.Code != 303 || rr.Header().Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	if c := sessionCookie(rr); c == nil || c.MaxAge != -1 {
		t.Fatal("session cookie not cleared")
	}
	if _, err := s.auth.LookupSession(context.Background(), token, time.Now()); err == nil {
		t.Fatal("session not deleted on logout")
	}
}

func mustChange(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE users SET must_change_password = 1 WHERE id = ?`, userID); err != nil {
		t.Fatal(err)
	}
}

func changePassword(h http.Handler, token, csrf, current, next, confirm string) *httptest.ResponseRecorder {
	return postForm(h, "/password", url.Values{"csrf_token": {csrf}, "current_password": {current},
		"new_password": {next}, "confirm_password": {confirm}},
		&http.Cookie{Name: auth.SessionCookieName, Value: token})
}

func TestForcedPasswordChangeFlow(t *testing.T) {
	s, db, u, other := authenticatedServer(t)
	mustChange(t, db, u.ID)
	h := s.Handler()

	rr := login(h, anonymousCSRF(t, h), "alice", "password123", false)
	if rr.Code != 303 || rr.Header().Get("Location") != "/password" {
		t.Fatalf("forced user login: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	token := sessionCookie(rr).Value
	csrf := sessionCSRF(t, s, token)
	if rr := get(h, "/", token); rr.Code != 303 || rr.Header().Get("Location") != "/password" {
		t.Fatal("forced user reached home")
	}
	page := get(h, "/password", token)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Please set a new password") ||
		strings.Contains(page.Body.String(), "timezone.js") {
		t.Fatalf("password page: status=%d", page.Code)
	}

	for _, tc := range []struct{ name, current, next, confirm, msg string }{
		{"mismatch", "password123", "new-password-1", "new-password-2", "do not match"},
		{"policy", "password123", "short", "short", "8–32 characters"},
		{"unchanged", "password123", "password123", "password123", "must be different"},
	} {
		rr := changePassword(h, token, csrf, tc.current, tc.next, tc.confirm)
		if rr.Code != 422 || !strings.Contains(rr.Body.String(), tc.msg) {
			t.Fatalf("%s: status=%d", tc.name, rr.Code)
		}
	}
	if rr := changePassword(h, token, "", "password123", "new-password-1", "new-password-1"); rr.Code != 403 {
		t.Fatalf("password change without CSRF: status=%d", rr.Code)
	}

	rr = changePassword(h, token, csrf, "password123", "new-password-1", "new-password-1")
	if rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatalf("change password: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	if rr := get(h, "/", token); rr.Code != 200 {
		t.Fatalf("current session lost or still forced: status=%d", rr.Code)
	}
	if _, err := s.auth.LookupSession(context.Background(), other, time.Now()); err == nil {
		t.Fatal("other session survived password change")
	}
	var flag bool
	if err := db.QueryRow(`SELECT must_change_password FROM users WHERE id = ?`, u.ID).Scan(&flag); err != nil || flag {
		t.Fatalf("must_change_password not cleared: %v %v", flag, err)
	}
	csrfCookie := anonymousCSRF(t, h)
	if rr := login(h, csrfCookie, "alice", "new-password-1", false); rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatalf("login with new password: status=%d", rr.Code)
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	h := s.Handler()
	csrf := sessionCSRF(t, s, token)
	rr := changePassword(h, token, csrf, "not-my-password", "new-password-1", "new-password-1")
	if rr.Code != 422 || !strings.Contains(rr.Body.String(), "current password is incorrect") {
		t.Fatalf("status=%d", rr.Code)
	}
	rr = changePassword(h, token, csrf, "password123", "new-password-1", "new-password-1")
	if rr.Code != 429 {
		t.Fatalf("current-password guessing not rate limited: status=%d", rr.Code)
	}
	if rr := get(h, "/password", ""); rr.Code != 303 || rr.Header().Get("Location") != "/login" {
		t.Fatal("anonymous user reached /password")
	}
}

func TestTimezoneCapture(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	h := s.Handler()
	if !strings.Contains(get(h, "/", token).Body.String(), `src="/static/timezone.js?v=`) {
		t.Fatal("timezone script missing for user without timezone")
	}
	if rr := get(h, "/static/timezone.js", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "/settings/timezone") {
		t.Fatal("timezone script not served")
	}
	post := func(tz, csrf string) int {
		r := httptest.NewRequest("POST", "/settings/timezone", strings.NewReader(url.Values{"timezone": {tz}}.Encode()))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-CSRF-Token", csrf)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr.Code
	}
	csrf := sessionCSRF(t, s, token)
	if code := post("Asia/Shanghai", "wrong"); code != 403 {
		t.Fatalf("timezone without valid CSRF: %d", code)
	}
	for _, bad := range []string{"", "Local", "Not/AZone", "../../etc/passwd"} {
		if code := post(bad, csrf); code != 400 {
			t.Fatalf("timezone %q: status=%d", bad, code)
		}
	}
	if code := post("Asia/Shanghai", csrf); code != 204 {
		t.Fatalf("status=%d", code)
	}
	if code := post("Europe/Paris", csrf); code != 204 {
		t.Fatalf("status=%d", code)
	}
	var tz string
	if err := db.QueryRow(`SELECT timezone FROM users WHERE id = ?`, u.ID).Scan(&tz); err != nil || tz != "Asia/Shanghai" {
		t.Fatalf("timezone=%q err=%v; auto-capture must not overwrite", tz, err)
	}
	if strings.Contains(get(h, "/", token).Body.String(), "timezone.js") {
		t.Fatal("timezone script still loaded after capture")
	}
}
