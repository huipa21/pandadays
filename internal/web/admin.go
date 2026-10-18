package web

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"pandadays/internal/auth"
)

type adminUsersPage struct {
	pageData
	Users    []auth.AdminUser
	Password string
	Account  string
	Error    string
}

type adminSettingsPage struct {
	pageData
	Settings auth.SiteSettings
	Error    string
}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/users", RequireAdmin(http.HandlerFunc(s.handleAdminUsers)))
	mux.Handle("POST /admin/users", RequireAdmin(http.HandlerFunc(s.createAdminUser)))
	for _, action := range []string{"reset-password", "activate", "deactivate", "delete", "quotas"} {
		mux.Handle("POST /admin/users/{id}/"+action, RequireAdmin(http.HandlerFunc(s.adminUserAction)))
	}
	mux.Handle("GET /admin/settings", RequireAdmin(http.HandlerFunc(s.handleAdminSettings)))
	mux.Handle("POST /admin/settings", RequireAdmin(http.HandlerFunc(s.saveAdminSettings)))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r)}, http.StatusOK)
}

func (s *Server) renderAdminUsers(w http.ResponseWriter, r *http.Request, page adminUsersPage, status int) {
	users, err := s.auth.AdminUsers(r.Context())
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	page.Users = users
	w.Header().Set("Cache-Control", "no-store")
	s.renderStatus(w, r, "admin_users.html", status, page)
}

func (s *Server) adminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotAdmin), errors.Is(err, auth.ErrUserNotFound):
		http.NotFound(w, r)
	case errors.Is(err, auth.ErrAdminProtected):
		s.authError(w, r, "admin.error.protected", http.StatusUnprocessableEntity)
	case errors.Is(err, auth.ErrQuotaInvalid):
		s.authError(w, r, "admin.error.quota", http.StatusUnprocessableEntity)
	default:
		s.log.Error("admin operation failed", "path", r.URL.Path, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
	}
}

func (s *Server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	u, password, err := s.auth.AdminCreateUser(r.Context(), CurrentUser(r.Context()).ID, auth.CreateUserParams{
		Username: r.PostForm.Get("username"), Email: r.PostForm.Get("email"),
		IsAdmin: r.PostForm.Get("is_admin") == "1",
	}, s.now())
	if errors.Is(err, auth.ErrIdentity) {
		s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Error: "setup.error.identity"}, http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Password: password, Account: u.Username}, http.StatusOK)
}

func parseQuota(value string) (sql.NullInt64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullInt64{}, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return sql.NullInt64{}, auth.ErrQuotaInvalid
	}
	return sql.NullInt64{Int64: n, Valid: true}, nil
}

func (s *Server) adminUserAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	actor := CurrentUser(r.Context()).ID
	switch action := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]; action {
	case "reset-password":
		username, password, err := s.auth.AdminResetPassword(r.Context(), actor, id, s.now())
		if err != nil {
			s.adminError(w, r, err)
			return
		}
		if actor == id {
			auth.ClearSessionCookie(w, s.cfg.CookieSecure)
		}
		s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r),
			Password: password, Account: username}, http.StatusOK)
		return
	case "activate", "deactivate":
		err = s.auth.AdminSetActive(r.Context(), actor, id, action == "activate", s.now())
	case "quotas":
		var q auth.QuotaOverrides
		fields := []struct {
			key string
			dst *sql.NullInt64
		}{{"quota_days", &q.Days}, {"quota_bytes", &q.Bytes}, {"max_card_bytes", &q.CardBytes}}
		for _, field := range fields {
			*field.dst, err = parseQuota(r.PostForm.Get(field.key))
			if err != nil {
				s.adminError(w, r, err)
				return
			}
		}
		err = s.auth.AdminSetQuotas(r.Context(), actor, id, q, s.now())
	case "delete":
		if r.PostForm.Get("confirm") != "1" {
			s.authError(w, r, "admin.error.confirm", http.StatusUnprocessableEntity)
			return
		}
		err = s.auth.AdminDeleteUser(r.Context(), actor, id, filepath.Join(s.cfg.DataDir, "media"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.auth.SiteSettings(r.Context())
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	s.render(w, r, "admin_settings.html", adminSettingsPage{pageData: newPageData(r), Settings: v})
}

func (s *Server) saveAdminSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	v := auth.SiteSettings{Language: r.PostForm.Get("site_lang")}
	fields := []struct {
		key string
		dst *int64
	}{
		{"session_days", &v.SessionDays}, {"default_quota_days", &v.QuotaDays},
		{"default_quota_bytes", &v.QuotaBytes}, {"default_max_card_bytes", &v.MaxCardBytes},
		{"backup_retention", &v.BackupRetention},
	}
	for _, field := range fields {
		n, err := parseQuota(r.PostForm.Get(field.key))
		if err != nil || !n.Valid {
			s.authError(w, r, "admin.error.settings", http.StatusUnprocessableEntity)
			return
		}
		*field.dst = n.Int64
	}
	if !s.beginMaintenance(w, r) {
		return
	}
	defer s.endMaintenance()
	err := s.auth.AdminSaveSettings(r.Context(), CurrentUser(r.Context()).ID, v)
	if errors.Is(err, auth.ErrSettingInvalid) {
		s.renderStatus(w, r, "admin_settings.html", http.StatusUnprocessableEntity,
			adminSettingsPage{pageData: newPageData(r), Settings: v, Error: "admin.error.settings"})
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	if err := s.backup.Prune(v.BackupRetention); err != nil {
		s.backupError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
