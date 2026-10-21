package web

import (
	"database/sql"
	"errors"
	"math/big"
	"net/http"
	"net/url"
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
	Query    string
	Create   bool
	TargetID int64
	Edit     bool
	Delete   bool
	Fields   auth.User
}

type adminSettingsPage struct {
	pageData
	Settings auth.SiteSettings
	Error    string
}

type adminUserActions struct {
	pageData
	Target auth.AdminUser
}

func (p adminUsersPage) Actions(u auth.AdminUser) adminUserActions {
	return adminUserActions{pageData: p.pageData, Target: u}
}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/users", RequireAdmin(http.HandlerFunc(s.handleAdminUsers)))
	mux.Handle("GET /admin/users/new", RequireAdmin(http.HandlerFunc(s.handleAdminUserCreate)))
	mux.Handle("GET /admin/users/{id}", RequireAdmin(http.HandlerFunc(s.handleAdminUser)))
	mux.Handle("GET /admin/users/{id}/edit", RequireAdmin(http.HandlerFunc(s.handleAdminUserEdit)))
	mux.Handle("GET /admin/users/{id}/delete", RequireAdmin(http.HandlerFunc(s.handleAdminUserDelete)))
	mux.Handle("POST /admin/users/{id}/edit", RequireAdmin(http.HandlerFunc(s.saveAdminUser)))
	mux.Handle("POST /admin/users", RequireAdmin(http.HandlerFunc(s.createAdminUser)))
	for _, action := range []string{"reset-password", "activate", "deactivate", "delete", "quotas"} {
		mux.Handle("POST /admin/users/{id}/"+action, RequireAdmin(http.HandlerFunc(s.adminUserAction)))
	}
	mux.Handle("GET /admin/settings", RequireAdmin(http.HandlerFunc(s.handleAdminSettings)))
	mux.Handle("POST /admin/settings", RequireAdmin(http.HandlerFunc(s.saveAdminSettings)))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Query: strings.TrimSpace(r.URL.Query().Get("q"))}, http.StatusOK)
}

func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Create: true}, http.StatusOK)
}

func (s *Server) handleAdminUser(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return
	}
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), TargetID: id}, http.StatusOK)
}

func (s *Server) handleAdminUserEdit(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return
	}
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), TargetID: id, Edit: true}, http.StatusOK)
}

// handleAdminUserDelete shows the user's details for delete confirmation:
// a dialog fragment for htmx, or a full page without JavaScript.
func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 || id == CurrentUser(r.Context()).ID {
		http.NotFound(w, r)
		return
	}
	s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), TargetID: id, Delete: true}, http.StatusOK)
}

func (s *Server) saveAdminUser(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return
	}
	users, err := s.auth.AdminUsers(r.Context())
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	var target *auth.User
	for _, u := range users {
		if u.ID == id {
			user := u.User
			target = &user
			break
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	p, err := profileParams(r)
	p.ClientIP = ClientIP(r)
	if err == nil {
		err = s.auth.UpdateProfile(r.Context(), CurrentUser(r.Context()).ID, id, p, target.Lang, target.Timezone, s.now())
	}
	if errors.Is(err, auth.ErrIdentity) || errors.Is(err, auth.ErrProfile) || errors.Is(err, auth.ErrCredentials) || errors.Is(err, auth.ErrRateLimited) {
		key, status := "profile.error", http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, auth.ErrIdentity):
			key = "profile.error.email"
		case errors.Is(err, auth.ErrCredentials):
			key = "profile.error.password"
		case errors.Is(err, auth.ErrRateLimited):
			key, status = "password.error.rate_limited", http.StatusTooManyRequests
		}
		page := adminUsersPage{pageData: newPageData(r), Edit: true, TargetID: id, Error: key,
			Fields: auth.User{ID: id, Username: target.Username, DisplayName: p.DisplayName, Email: p.Email, Comment: p.Comment, HasAvatar: target.HasAvatar}}
		s.renderAdminUsers(w, r, page, status)
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) renderAdminUsers(w http.ResponseWriter, r *http.Request, page adminUsersPage, status int) {
	if page.Create {
		s.renderStatus(w, r, "admin_user_create.html", status, page)
		return
	}
	users, err := s.auth.AdminUsers(r.Context())
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	for _, user := range users {
		if page.TargetID != 0 && user.ID != page.TargetID {
			continue
		}
		if page.Query != "" && !strings.Contains(strings.ToLower(user.Username), strings.ToLower(page.Query)) &&
			!strings.Contains(strings.ToLower(user.DisplayName), strings.ToLower(page.Query)) &&
			!strings.Contains(strings.ToLower(user.Email), strings.ToLower(page.Query)) {
			continue
		}
		page.Users = append(page.Users, user)
	}
	name := "admin_users.html"
	if page.TargetID != 0 {
		page.ReturnTo = "/admin/users/" + strconv.FormatInt(page.TargetID, 10)
		if len(page.Users) == 0 {
			http.NotFound(w, r)
			return
		}
		name = "admin_user.html"
		if page.Delete {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "HX-Request")
			if isHTMX(r) {
				s.renderPartial(w, r, "admin_user_delete.html", "delete-user", page)
			} else {
				s.renderStatus(w, r, "admin_user_delete.html", status, page)
			}
			return
		}
		if page.Edit {
			name = "admin_user_create.html"
			if page.Error == "" {
				page.Fields = page.Users[0].User
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderStatus(w, r, name, status, page)
}

func (s *Server) adminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotAdmin), errors.Is(err, auth.ErrUserNotFound):
		http.NotFound(w, r)
	case errors.Is(err, auth.ErrAdminProtected):
		s.authError(w, r, "admin.error.protected", http.StatusUnprocessableEntity)
	case errors.Is(err, auth.ErrQuotaInvalid):
		s.authError(w, r, "admin.error.quota", http.StatusUnprocessableEntity)
	case errors.Is(err, auth.ErrProfile):
		s.authError(w, r, "profile.error", http.StatusUnprocessableEntity)
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
	p, err := profileParams(r)
	var u *auth.User
	var password string
	if err == nil {
		u, password, err = s.auth.AdminCreateUser(r.Context(), CurrentUser(r.Context()).ID, auth.CreateUserParams{
			Username: p.Username, Email: p.Email, DisplayName: p.DisplayName, Comment: p.Comment, Avatar: p.Avatar,
			IsAdmin: r.PostForm.Get("is_admin") == "1",
		}, s.now())
	}
	if errors.Is(err, auth.ErrUsername) {
		s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Create: true, Error: "username.error",
			Fields: auth.User{Username: p.Username, DisplayName: p.DisplayName, Email: p.Email, Comment: p.Comment}}, http.StatusUnprocessableEntity)
		return
	}
	if errors.Is(err, auth.ErrIdentity) || errors.Is(err, auth.ErrProfile) {
		s.renderAdminUsers(w, r, adminUsersPage{pageData: newPageData(r), Create: true, Error: "profile.error",
			Fields: auth.User{Username: p.Username, DisplayName: p.DisplayName, Email: p.Email, Comment: p.Comment}}, http.StatusUnprocessableEntity)
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

const megabyte = 1 << 20

func formatMegabytes(n int64) string {
	value := new(big.Rat).SetFrac(big.NewInt(n), big.NewInt(megabyte)).FloatString(20)
	return strings.TrimRight(strings.TrimRight(value, "0"), ".")
}

// New forms use M; byte fields remain accepted for existing clients.
func parseStorageQuota(form url.Values, key string) (sql.NullInt64, error) {
	mKey := strings.TrimSuffix(key, "_bytes") + "_mb"
	if !form.Has(mKey) {
		return parseQuota(form.Get(key))
	}
	value := strings.TrimSpace(form.Get(mKey))
	if value == "" {
		return sql.NullInt64{}, nil
	}
	if len(value) > 64 || strings.Count(value, ".") > 1 || strings.Trim(value, "0123456789.") != "" {
		return sql.NullInt64{}, auth.ErrQuotaInvalid
	}
	if !strings.Contains(value, ".") {
		value += ".0"
	}
	n, ok := new(big.Rat).SetString(value)
	if !ok || n.Sign() < 0 {
		return sql.NullInt64{}, auth.ErrQuotaInvalid
	}
	n.Mul(n, big.NewRat(megabyte, 1))
	if !n.IsInt() || !n.Num().IsInt64() {
		return sql.NullInt64{}, auth.ErrQuotaInvalid
	}
	return sql.NullInt64{Int64: n.Num().Int64(), Valid: true}, nil
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
			Password: password, Account: username, TargetID: id}, http.StatusOK)
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
			if field.key == "quota_days" {
				*field.dst, err = parseQuota(r.PostForm.Get(field.key))
			} else {
				*field.dst, err = parseStorageQuota(r.PostForm, field.key)
			}
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
		if err == nil {
			if collectErr := s.media.Collect(r.Context()); collectErr != nil {
				s.log.Error("remove unreferenced media for deleted user", "user_id", id, "err", collectErr)
			}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	target := "/admin/users"
	detail := target + "/" + strconv.FormatInt(id, 10)
	if r.PostForm.Get("return_to") == detail && !strings.HasSuffix(r.URL.Path, "/delete") {
		target = detail
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
		if field.key == "default_quota_bytes" || field.key == "default_max_card_bytes" {
			n, err = parseStorageQuota(r.PostForm, field.key)
		}
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
