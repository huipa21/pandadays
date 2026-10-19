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
	return r.URL.Path == "/settings" || r.URL.Path == "/admin/users" ||
		(strings.HasPrefix(r.URL.Path, "/admin/users/") && strings.HasSuffix(r.URL.Path, "/edit"))
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
		RemoveAvatar: r.PostForm.Get("remove_avatar") == "1",
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
