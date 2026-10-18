package web

import (
	"errors"
	"net/http"
	"time"

	"pandadays/internal/auth"
)

type loginPage struct {
	pageData
	Account  string
	Remember bool
	Error    string
}

type passwordPage struct {
	pageData
	Error string
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if CurrentUser(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if s.auth != nil {
		if admin, err := s.auth.HasAdmin(r.Context()); err == nil && !admin {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
	}
	s.render(w, r, "login.html", loginPage{pageData: newPageData(r)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	page := loginPage{pageData: newPageData(r), Account: r.PostForm.Get("account"),
		Remember: r.PostForm.Get("remember") != ""}
	now := time.Now()
	user, err := s.auth.Authenticate(r.Context(), ClientIP(r), page.Account, r.PostForm.Get("password"), now)
	switch {
	case errors.Is(err, auth.ErrCredentials):
		page.Error = "login.error.invalid"
		s.renderStatus(w, r, "login.html", http.StatusUnauthorized, page)
		return
	case errors.Is(err, auth.ErrRateLimited):
		page.Error = "login.error.rate_limited"
		s.renderStatus(w, r, "login.html", http.StatusTooManyRequests, page)
		return
	case err != nil:
		s.log.Error("authenticate", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}

	// Replace any session this browser already had.
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := s.auth.DeleteSession(r.Context(), cookie.Value); err != nil {
			s.log.Error("delete previous session", "err", err)
		}
	}
	token, session, err := s.auth.CreateSession(r.Context(), user.ID, page.Remember, now)
	if errors.Is(err, auth.ErrCredentials) {
		page.Error = "login.error.invalid"
		s.renderStatus(w, r, "login.html", http.StatusUnauthorized, page)
		return
	}
	if err != nil {
		s.log.Error("create session", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	s.log.Info("user logged in", "user_id", user.ID, "persistent", page.Remember)
	auth.SetSessionCookie(w, token, session, s.cfg.CookieSecure, now)
	target := "/"
	if session.User.MustChangePassword {
		target = "/password"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && s.auth != nil {
		if err := s.auth.DeleteSession(r.Context(), cookie.Value); err != nil {
			s.log.Error("logout", "err", err)
			s.authError(w, r, "error.internal", http.StatusInternalServerError)
			return
		}
	}
	auth.ClearSessionCookie(w, s.cfg.CookieSecure)
	authRedirect(w, r, "/login")
}

func (s *Server) handlePasswordForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "password.html", passwordPage{pageData: newPageData(r)})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	page := passwordPage{pageData: newPageData(r)}
	current, next := r.PostForm.Get("current_password"), r.PostForm.Get("new_password")
	if next != r.PostForm.Get("confirm_password") {
		page.Error = "password.error.mismatch"
		s.renderStatus(w, r, "password.html", http.StatusUnprocessableEntity, page)
		return
	}
	session := CurrentSession(r.Context())
	err := s.auth.ChangePassword(r.Context(), ClientIP(r), session.User.ID, session.TokenHash, current, next, time.Now())
	status := http.StatusUnprocessableEntity
	switch {
	case err == nil:
		s.log.Info("password changed", "user_id", session.User.ID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	case errors.Is(err, auth.ErrPasswordPolicy):
		page.Error = "password.error.policy"
	case errors.Is(err, auth.ErrCredentials):
		page.Error = "password.error.current"
	case errors.Is(err, auth.ErrPasswordUnchanged):
		page.Error = "password.error.unchanged"
	case errors.Is(err, auth.ErrRateLimited):
		page.Error, status = "password.error.rate_limited", http.StatusTooManyRequests
	default:
		s.log.Error("change password", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	s.renderStatus(w, r, "password.html", status, page)
}

// handleTimezone stores the browser timezone once, when the user has none.
func (s *Server) handleTimezone(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	_, err := s.auth.SetTimezoneIfEmpty(r.Context(), CurrentUser(r.Context()).ID, r.PostForm.Get("timezone"), time.Now())
	if errors.Is(err, auth.ErrTimezone) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.log.Error("set timezone", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
