package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"pandadays/internal/auth"
)

type settingsPage struct {
	pageData
	Language  string
	Timezone  string
	UsedBytes int64
	Quota     int64
	Timezones []string
	Error     string
	Status    int
}

var settingsTimezones = []string{
	"Africa/Cairo", "Africa/Johannesburg", "Africa/Lagos", "Africa/Nairobi",
	"America/Anchorage", "America/Bogota", "America/Chicago", "America/Denver",
	"America/Halifax", "America/Los_Angeles", "America/Mexico_City", "America/New_York",
	"America/Phoenix", "America/Sao_Paulo", "America/St_Johns", "America/Toronto",
	"Asia/Bangkok", "Asia/Dubai", "Asia/Hong_Kong", "Asia/Jakarta", "Asia/Jerusalem",
	"Asia/Karachi", "Asia/Kathmandu", "Asia/Kolkata", "Asia/Seoul", "Asia/Shanghai",
	"Asia/Singapore", "Asia/Taipei", "Asia/Tokyo", "Australia/Adelaide",
	"Australia/Brisbane", "Australia/Perth", "Australia/Sydney", "Europe/Amsterdam",
	"Europe/Athens", "Europe/Berlin", "Europe/Dublin", "Europe/Helsinki",
	"Europe/Istanbul", "Europe/Lisbon", "Europe/London", "Europe/Madrid",
	"Europe/Moscow", "Europe/Paris", "Europe/Rome", "Europe/Stockholm",
	"Pacific/Auckland", "Pacific/Honolulu", "UTC",
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, settingsPage{pageData: newPageData(r)})
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, page settingsPage) {
	page.Language = CurrentUser(r.Context()).Lang
	page.Timezone = CurrentUser(r.Context()).Timezone
	page.Timezones = append([]string(nil), settingsTimezones...)
	if page.Timezone != "" {
		if _, err := auth.ValidateTimezone(page.Timezone); err == nil {
			found := false
			for _, name := range page.Timezones {
				if name == page.Timezone {
					found = true
					break
				}
			}
			if !found {
				page.Timezones = append(page.Timezones, page.Timezone)
			}
		}
	}
	err := s.db.QueryRowContext(r.Context(), `SELECT u.used_bytes,
		COALESCE(u.quota_bytes,
			(SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_quota_bytes'))
		FROM users u WHERE u.id = ?`, CurrentUser(r.Context()).ID).
		Scan(&page.UsedBytes, &page.Quota)
	if err != nil {
		s.log.Error("read user storage usage", "user_id", CurrentUser(r.Context()).ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	if page.UsedBytes < 0 || page.Quota < 0 {
		s.log.Error("invalid user storage usage", "user_id", CurrentUser(r.Context()).ID)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	status := page.Status
	if status == 0 {
		status = http.StatusOK
	}
	s.renderStatus(w, r, "settings.html", status, page)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	user := CurrentUser(r.Context())
	lang, timezone := user.Lang, r.PostForm.Get("timezone")
	if r.PostForm.Has("lang") {
		lang = r.PostForm.Get("lang")
	}
	err := s.auth.UpdatePreferences(r.Context(), user.ID, lang, timezone, s.now())
	switch {
	case errors.Is(err, auth.ErrLanguage):
		page := settingsPage{pageData: newPageData(r), Error: "settings.error.language", Status: http.StatusUnprocessableEntity}
		s.renderSettings(w, r, page)
	case errors.Is(err, auth.ErrTimezone):
		page := settingsPage{pageData: newPageData(r), Error: "settings.error.timezone", Status: http.StatusUnprocessableEntity}
		s.renderSettings(w, r, page)
	case err != nil:
		s.log.Error("update user preferences", "user_id", user.ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

func (s *Server) saveLanguage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	lang := r.PostForm.Get("lang")
	if lang != "en" && lang != "zh" {
		s.authError(w, r, "settings.error.language", http.StatusUnprocessableEntity)
		return
	}
	user := CurrentUser(r.Context())
	if err := s.auth.UpdatePreferences(r.Context(), user.ID, lang, user.Timezone, s.now()); err != nil {
		s.log.Error("update language", "user_id", user.ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	target := r.PostForm.Get("return_to")
	u, err := url.Parse(target)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n") {
		target = "/"
	} else {
		q := u.Query()
		q.Del("lang")
		u.RawQuery = q.Encode()
		target = u.String()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
