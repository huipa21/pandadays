package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"pandadays/internal/auth"
)

type sessionContextKey struct{}

func CurrentSession(ctx context.Context) *auth.Session {
	session, _ := ctx.Value(sessionContextKey{}).(*auth.Session)
	return session
}

func CurrentUser(ctx context.Context) *auth.User {
	if session := CurrentSession(ctx); session != nil {
		return &session.User
	}
	return nil
}

// ClientIP intentionally ignores untrusted proxy forwarding headers.
func ClientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) loadCurrentUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if errors.Is(err, http.ErrNoCookie) || s.auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			s.authError(w, r, "error.session", http.StatusBadRequest)
			return
		}
		session, err := s.auth.LookupSession(r.Context(), cookie.Value, time.Now())
		if errors.Is(err, auth.ErrSession) {
			auth.ClearSessionCookie(w, s.cfg.CookieSecure)
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			s.log.Error("load current user", "err", err)
			s.authError(w, r, "error.internal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CurrentUser(r.Context()) == nil {
			authRedirect(w, r, "/login")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireAdmin(next http.Handler) http.Handler {
	return RequireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CurrentUser(r.Context()).IsAdmin {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func forcePasswordChange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := CurrentUser(r.Context())
		// Health checks and assets are infrastructure, not account pages.
		exempt := r.URL.Path == "/password" || r.URL.Path == "/logout" ||
			r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/")
		if user != nil && user.MustChangePassword && !exempt {
			authRedirect(w, r, "/password")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authRedirect(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.EqualFold(r.Header.Get("HX-Request"), "true") {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) authError(w http.ResponseWriter, r *http.Request, key string, status int) {
	http.Error(w, s.i18n.T(s.requestLang(r), key), status)
}
