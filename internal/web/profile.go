package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"pandadays/internal/auth"
)

func isProfileForm(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	return r.URL.Path == "/profile" || r.URL.Path == "/admin/users" ||
		(strings.HasPrefix(r.URL.Path, "/admin/users/") && strings.HasSuffix(r.URL.Path, "/edit"))
}

type profilePage struct {
	pageData
	Fields auth.User
	Error  string
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	s.renderProfile(w, r, profilePage{pageData: newPageData(r)}, http.StatusOK)
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, page profilePage, status int) {
	user := CurrentUser(r.Context())
	if page.Error == "" {
		page.Fields = *user
	}
	page.Fields.ID, page.Fields.Username, page.Fields.HasAvatar = user.ID, user.Username, user.HasAvatar
	s.renderStatus(w, r, "profile.html", status, page)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	p, err := profileParams(r)
	p.Comment = user.Comment
	p.ClientIP = ClientIP(r)
	if err == nil {
		err = s.auth.UpdateProfile(r.Context(), user.ID, user.ID, p, user.Lang, user.Timezone, s.now())
	}
	page := profilePage{pageData: newPageData(r), Fields: auth.User{DisplayName: p.DisplayName, Email: p.Email}}
	status := http.StatusUnprocessableEntity
	switch {
	case err == nil:
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	case errors.Is(err, auth.ErrIdentity):
		page.Error = "profile.error.email"
	case errors.Is(err, auth.ErrProfile):
		page.Error = "profile.error"
	case errors.Is(err, auth.ErrCredentials):
		page.Error = "profile.error.password"
	case errors.Is(err, auth.ErrRateLimited):
		page.Error, status = "password.error.rate_limited", http.StatusTooManyRequests
	default:
		s.log.Error("update profile", "user_id", user.ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	s.renderProfile(w, r, page, status)
}

func (s *Server) removeAvatar(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	if err := s.auth.RemoveAvatar(r.Context(), user.ID, s.now()); err != nil {
		s.log.Error("remove avatar", "user_id", user.ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

func parseProfileForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, auth.MaxAvatarBytes+(128<<10))
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	if kind == "multipart/form-data" {
		return r.ParseMultipartForm(auth.MaxAvatarBytes + (128 << 10))
	}
	return r.ParseForm()
}

func profileParams(r *http.Request) (auth.ProfileParams, error) {
	p := auth.ProfileParams{
		Username: r.PostForm.Get("username"), DisplayName: r.PostForm.Get("display_name"),
		Email: r.PostForm.Get("email"), Comment: r.PostForm.Get("comment"),
		RemoveAvatar:    r.PostForm.Get("remove_avatar") == "1",
		CurrentPassword: r.PostForm.Get("current_password"),
	}
	if r.MultipartForm == nil {
		return p, nil
	}
	file, _, err := r.FormFile("avatar")
	if errors.Is(err, http.ErrMissingFile) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer file.Close()
	p.Avatar, err = io.ReadAll(io.LimitReader(file, auth.MaxAvatarBytes+1))
	if err != nil {
		return p, err
	}
	if len(p.Avatar) > auth.MaxAvatarBytes {
		return p, auth.ErrProfile
	}
	return p, nil
}

func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return
	}
	data, err := s.auth.Avatar(r.Context(), CurrentUser(r.Context()).ID, id)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(data)
}
