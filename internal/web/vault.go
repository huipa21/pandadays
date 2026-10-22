package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"pandadays/internal/auth"
	"pandadays/internal/days"
)

func privatePath(r *http.Request, path string) string {
	if days.IsPrivate(r.Context()) && vaultPath(path) {
		return "/private" + path
	}
	return path
}

func vaultPath(path string) bool {
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	if strings.HasSuffix(path, "/shares") {
		return false
	}
	if path == "/" || path == "/unlock" || path == "/settings" || path == "/settings/language" ||
		path == "/settings/timezone" || path == "/settings/export" ||
		path == "/settings/import" || path == "/settings/import/confirm" {
		return true
	}
	for _, prefix := range []string{"/days", "/archive", "/collections", "/tags", "/attachments", "/media"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// Strip only the authorized namespace, before CSRF's multipart route detection.
func (s *Server) vaultNamespace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/private/") {
			r = r.Clone(days.WithPrivate(r.Context(), true))
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/private")
			r.URL.RawPath = ""
			if !vaultPath(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setVaultUnlocked(r *http.Request, unlocked bool) error {
	session := CurrentSession(r.Context())
	_, err := s.db.ExecContext(r.Context(), `UPDATE sessions SET vault_unlocked = ?
		WHERE token_hash = ? AND user_id = ?`, unlocked, session.TokenHash, session.User.ID)
	return err
}

func (s *Server) vaultGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := CurrentSession(r.Context())
		if days.IsPrivate(r.Context()) {
			if session == nil {
				authRedirect(w, r, "/login")
				return
			}
			if r.URL.Path == "/unlock" {
				s.handleVaultUnlock(w, r)
				return
			}
			if !session.VaultUnlocked {
				authRedirect(w, r, "/unlock")
				return
			}
		}
		if session != nil && session.VaultUnlocked {
			r = r.WithContext(days.WithPrivateIncluded(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleVaultLock(w http.ResponseWriter, r *http.Request) {
	if err := s.setVaultUnlocked(r, false); err != nil {
		s.dayFail(w, r, "lock vault", err)
		return
	}
	target := r.PostFormValue("return_to")
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") ||
		strings.HasPrefix(target, "/private") || strings.ContainsAny(target, "\\\r\n") {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleVaultUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key, status := "", http.StatusOK
	if r.Method == http.MethodPost {
		user := CurrentUser(r.Context())
		verified, err := s.auth.Authenticate(r.Context(), ClientIP(r), user.Username, r.PostFormValue("password"), s.now())
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			key, status = "login.error.rate_limited", http.StatusTooManyRequests
		case errors.Is(err, auth.ErrCredentials):
			key, status = "login.error.invalid", http.StatusUnprocessableEntity
		case err != nil:
			s.dayFail(w, r, "verify vault password", err)
			return
		case verified.ID != user.ID:
			http.NotFound(w, r)
			return
		default:
			if err := s.setVaultUnlocked(r, true); err != nil {
				s.dayFail(w, r, "unlock vault", err)
				return
			}
			if r.Header.Get("Accept") == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
				return
			}
			authRedirect(w, r, "/")
			return
		}
	} else if err := s.setVaultUnlocked(r, false); err != nil {
		s.dayFail(w, r, "lock vault for entry", err)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{s.i18n.T(newPageData(r).Lang, key)})
		return
	}
	s.renderStatus(w, r, "vault.html", status, struct {
		pageData
		Error string
	}{newPageData(r), key})
}
