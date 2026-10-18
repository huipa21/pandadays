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

func authenticatedServer(t *testing.T) (*Server, *sql.DB, *auth.User, string) {
	t.Helper()
	db := dbtest.Open(t)
	s := newTestServerWith(t, func(string) string { return "" }, db)
	u, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{
		Username: "Alice", Email: "alice@example.com", Password: "password123",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.auth.CreateSession(context.Background(), u.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, db, u, token
}

func sessionRequest(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	return r
}

func TestCurrentUserAndLanguage(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET lang = 'zh', timezone = 'Asia/Shanghai' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, sessionRequest("GET", "/", token))
	if rr.Code != 200 {
		t.Fatalf("status=%d", rr.Code)
	}
	assertLang(t, rr.Body.String(), "zh")
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated response can be cached")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := CurrentUser(r.Context())
		session := CurrentSession(r.Context())
		if current == nil || current.ID != u.ID || current.Timezone != "Asia/Shanghai" || session == nil {
			t.Fatal("user/session not available in context")
		}
	})
	s.loadCurrentUser(next).ServeHTTP(httptest.NewRecorder(), sessionRequest("GET", "/", token))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, sessionRequest("GET", "/?lang=en", token))
	assertLang(t, rr.Body.String(), "en")

	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	h := s.loadCurrentUser(RequireLogin(next))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest("GET", "/", token))
	if rr.Code != 303 || rr.Header().Get("Location") != "/login" || rr.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("inactive account remained authenticated or cookie was not cleared")
	}
}

func TestAuthorizationGuards(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for _, tc := range []struct {
		name     string
		user     *auth.User
		admin    bool
		status   int
		location string
	}{
		{"anonymous login guard", nil, false, 303, "/login"},
		{"anonymous admin guard", nil, true, 303, "/login"},
		{"user login guard", &auth.User{}, false, 204, ""},
		{"user denied admin", &auth.User{}, true, 404, ""},
		{"admin accepted", &auth.User{IsAdmin: true}, true, 204, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/admin/users", nil)
			if tc.user != nil {
				r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, &auth.Session{User: *tc.user}))
			}
			h := RequireLogin(next)
			if tc.admin {
				h = RequireAdmin(next)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != tc.status || rr.Header().Get("Location") != tc.location {
				t.Fatalf("status=%d, location=%q", rr.Code, rr.Header().Get("Location"))
			}
		})
	}
	r := httptest.NewRequest("POST", "/days", nil)
	r.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	RequireLogin(next).ServeHTTP(rr, r)
	if rr.Header().Get("HX-Redirect") != "/login" || rr.Code != 200 {
		t.Fatal("htmx login redirect not a full navigation")
	}
}

func TestForcedPasswordChange(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET must_change_password = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/login", "/setup", "/admin/users", "/password/"} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, sessionRequest("GET", path, token))
		if rr.Code != 303 || rr.Header().Get("Location") != "/password" {
			t.Fatalf("%s: status=%d, location=%q", path, rr.Code, rr.Header().Get("Location"))
		}
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := s.loadCurrentUser(forcePasswordChange(next))
	for _, path := range []string{"/password", "/logout", "/healthz", "/static/app.css"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, sessionRequest("GET", path, token))
		if rr.Code != 204 {
			t.Fatalf("allowed path %s: status=%d", path, rr.Code)
		}
	}
	r := sessionRequest("POST", "/days", token)
	r.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Header().Get("HX-Redirect") != "/password" || rr.Code != 200 {
		t.Fatal("htmx forced change redirect not a full navigation")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest("GET", "/", ""))
	if rr.Code != 204 {
		t.Fatal("anonymous user forced to change password")
	}
}

func TestSessionLookupFailureIsNotAnonymousSuccess(t *testing.T) {
	s, db, _, token := authenticatedServer(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, sessionRequest("GET", "/", token))
	if rr.Code != 500 {
		t.Fatalf("database failure treated as anonymous success: %d", rr.Code)
	}
}

func TestCSRF(t *testing.T) {
	s := newTestServer(t)
	var receivedToken string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = CSRFToken(r.Context())
		w.WriteHeader(204)
	})
	h := s.resolveLang(s.csrf(next))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/login", nil))
	if rr.Code != 204 || receivedToken == "" {
		t.Fatal("anonymous CSRF token not generated")
	}
	anonymousToken := receivedToken
	cookie := rr.Result().Cookies()[0]
	if cookie.Name != csrfCookieName || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("CSRF cookie attributes incorrect")
	}
	for _, tc := range []struct {
		name, method, path, form, header, hx string
		cookie, session                      bool
		status                               int
	}{
		{"missing", "POST", "/login", "", "", "", true, false, 403},
		{"missing cookie", "POST", "/login", "", anonymousToken, "", false, false, 403},
		{"wrong", "POST", "/login", "csrf_token=wrong", "", "", true, false, 403},
		{"query rejected", "POST", "/login?csrf_token=" + url.QueryEscape(anonymousToken), "", "", "", true, false, 403},
		{"htmx header alone rejected", "POST", "/login", "", "", "true", true, false, 403},
		{"form", "POST", "/login", "csrf_token=" + url.QueryEscape(anonymousToken), "", "", true, false, 204},
		{"header", "POST", "/login", "", anonymousToken, "true", true, false, 204},
		{"put rejected", "PUT", "/settings", "", "", "", true, false, 403},
		{"patch rejected", "PATCH", "/settings", "", "", "", true, false, 403},
		{"delete accepted", "DELETE", "/settings", "", anonymousToken, "", true, false, 204},
		{"session missing", "POST", "/days", "", "", "", false, true, 403},
		{"session form", "POST", "/days", "csrf_token=session-proof", "", "", false, true, 204},
		{"session header", "POST", "/days", "", "session-proof", "true", false, true, 204},
		{"anonymous proof not session proof", "POST", "/days", "", anonymousToken, "true", true, true, 403},
		{"head allowed", "HEAD", "/", "", "", "", false, false, 204},
		{"options allowed", "OPTIONS", "/", "", "", "", false, false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-CSRF-Token", tc.header)
			r.Header.Set("HX-Request", tc.hx)
			if tc.cookie {
				r.AddCookie(cookie)
			}
			if tc.session {
				r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, &auth.Session{CSRFToken: "session-proof"}))
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status=%d, want %d", rr.Code, tc.status)
			}
			if tc.status == 403 && !strings.Contains(rr.Body.String(), "could not be verified") {
				t.Fatal("missing localized CSRF error")
			}
		})
	}
	for _, value := range []string{"forged.signature", anonymousToken + "x", strings.Repeat("a", 43) + ".forged"} {
		r := httptest.NewRequest("POST", "/login", nil)
		r.Header.Set("X-CSRF-Token", value)
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: value})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != 403 {
			t.Fatal("forged anonymous CSRF cookie accepted")
		}
	}
	r := httptest.NewRequest("POST", "/login", strings.NewReader("%not-encoded"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 403 {
		t.Fatal("invalid form accepted")
	}
}

func TestCSRFSessionIntegrationAndTemplates(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	session, err := s.auth.LookupSession(context.Background(), token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, sessionRequest("GET", "/", token))
	if !strings.Contains(rr.Body.String(), `name="csrf-token" content="`+session.CSRFToken+`"`) ||
		!strings.Contains(rr.Body.String(), `"X-CSRF-Token":"`+session.CSRFToken+`"`) {
		t.Fatal("session CSRF token missing from page or htmx headers")
	}
	var field strings.Builder
	if err := s.pages["home.html"].ExecuteTemplate(&field, "csrf-field", pageData{CSRFToken: session.CSRFToken}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(field.String(), `name="csrf_token" value="`+session.CSRFToken+`"`) {
		t.Fatal("hidden field template missing token")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := s.loadCurrentUser(s.resolveLang(s.csrf(next)))
	r := sessionRequest("POST", "/days", token)
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 204 {
		t.Fatalf("real session CSRF rejected: %d", rr.Code)
	}
	other, otherSession, err := s.auth.CreateSession(context.Background(), session.User.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r = sessionRequest("POST", "/days", other)
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 403 || session.CSRFToken == otherSession.CSRFToken {
		t.Fatal("CSRF token reusable across sessions")
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"192.0.2.1:1234", "192.0.2.1"},
		{"[2001:db8::1]:1234", "2001:db8::1"},
		{"192.0.2.2", "192.0.2.2"},
	} {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "attacker-controlled")
		if got := ClientIP(r); got != tc.want {
			t.Fatalf("ClientIP=%q, want %q", got, tc.want)
		}
	}
}
