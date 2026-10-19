package web

import (
	"errors"
	"net/http"
	"time"

	"pandadays/internal/auth"
)

type setupPage struct {
	pageData
	Username  string
	Email     string
	Language  string
	Timezone  string
	Timezones []string
	Error     string
}

func newSetupPage(r *http.Request) setupPage {
	page := setupPage{pageData: newPageData(r), Timezones: settingsTimezones}
	page.CaptureTimezone = true
	return page
}

// setupOpen 404s once an admin exists, so /setup is invisible after setup.
func (s *Server) setupOpen(w http.ResponseWriter, r *http.Request) bool {
	if s.auth == nil {
		http.NotFound(w, r)
		return false
	}
	admin, err := s.auth.HasAdmin(r.Context())
	if err != nil {
		s.log.Error("check setup state", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return false
	}
	if admin {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if !s.setupOpen(w, r) {
		return
	}
	s.render(w, r, "setup.html", newSetupPage(r))
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.setupOpen(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	page := newSetupPage(r)
	page.Username, page.Email = r.PostForm.Get("username"), r.PostForm.Get("email")
	page.Language, page.Timezone = r.PostForm.Get("lang"), r.PostForm.Get("timezone")
	password := r.PostForm.Get("password")
	if password != r.PostForm.Get("confirm_password") {
		page.Error = "password.error.mismatch"
		s.renderStatus(w, r, "setup.html", http.StatusUnprocessableEntity, page)
		return
	}
	now := time.Now()
	user, err := s.auth.CompleteSetup(r.Context(), ClientIP(r), r.PostForm.Get("token"),
		auth.CreateUserParams{Username: page.Username, Email: page.Email, Password: password,
			Lang: page.Language, Timezone: page.Timezone}, now)
	status := http.StatusUnprocessableEntity
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrSetupClosed):
		http.NotFound(w, r)
		return
	case errors.Is(err, auth.ErrSetupToken):
		page.Error, status = "setup.error.token", http.StatusForbidden
	case errors.Is(err, auth.ErrRateLimited):
		page.Error, status = "setup.error.rate_limited", http.StatusTooManyRequests
	case errors.Is(err, auth.ErrPasswordPolicy):
		page.Error = "setup.error.policy"
	case errors.Is(err, auth.ErrIdentity):
		page.Error = "setup.error.identity"
	case errors.Is(err, auth.ErrUsername):
		page.Error = "username.error"
	case errors.Is(err, auth.ErrLanguage):
		page.Error = "settings.error.language"
	case errors.Is(err, auth.ErrTimezone):
		page.Error = "settings.error.timezone"
	default:
		s.log.Error("complete setup", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	if err != nil {
		s.renderStatus(w, r, "setup.html", status, page)
		return
	}
	s.log.Info("initial admin created", "user_id", user.ID)

	// Sign the new admin in; fall back to the login page if that fails.
	token, session, err := s.auth.CreateSession(r.Context(), user.ID, false, now)
	if err != nil {
		s.log.Error("create session after setup", "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	auth.SetSessionCookie(w, token, session, s.cfg.CookieSecure, now)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
