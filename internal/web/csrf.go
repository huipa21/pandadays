package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"os"
	"strings"

	"pandadays/internal/media"
)

const csrfCookieName = "panda_csrf"

type csrfContextKey struct{}

// CSRFToken is available to page data for hidden csrf_token form fields.
// htmx requests may instead send it in X-CSRF-Token.
func CSRFToken(ctx context.Context) string {
	token, _ := ctx.Value(csrfContextKey{}).(string)
	return token
}

func (s *Server) signCSRF(value string) string {
	mac := hmac.New(sha256.New, s.csrfKey[:])
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) validAnonymousCSRF(value string) bool {
	nonce, signature, ok := strings.Cut(value, ".")
	b, err := base64.RawURLEncoding.DecodeString(nonce)
	return ok && err == nil && len(b) == 32 &&
		hmac.Equal([]byte(signature), []byte(s.signCSRF(nonce)))
}

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These endpoints neither render forms nor mutate application state.
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		if isProfileForm(r) {
			if user := CurrentUser(r.Context()); user == nil {
				authRedirect(w, r, "/login")
				return
			} else if r.URL.Path != "/settings" && !user.IsAdmin {
				http.NotFound(w, r)
				return
			}
			err := parseProfileForm(w, r)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			if err != nil {
				var maxErr *http.MaxBytesError
				status := http.StatusBadRequest
				if errors.As(err, &maxErr) {
					status = http.StatusRequestEntityTooLarge
				}
				s.authError(w, r, "profile.error", status)
				return
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/admin/backup/restore" {
			user := CurrentUser(r.Context())
			if user == nil {
				authRedirect(w, r, "/login")
				return
			}
			if !user.IsAdmin {
				http.NotFound(w, r)
				return
			}
			filename, err := s.stageRestoreForm(r)
			if err != nil {
				s.log.Warn("stage restore upload", "err", err)
				s.authError(w, r, "backup.error.upload", http.StatusBadRequest)
				return
			}
			defer func() {
				if err := os.Remove(filename); err != nil {
					s.log.Error("remove restore upload", "err", err)
				}
			}()
		}
		token := ""
		if session := CurrentSession(r.Context()); session != nil {
			token = session.CSRFToken
		} else if cookie, err := r.Cookie(csrfCookieName); err == nil && s.validAnonymousCSRF(cookie.Value) {
			token = cookie.Value
		}
		if !safe {
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" {
				multipartForm := isAttachmentUpload(r) || isImportUpload(r)
				if multipartForm {
					if CurrentUser(r.Context()) == nil {
						authRedirect(w, r, "/login")
						return
					}
					var limited bool
					if isImportUpload(r) {
						limited = s.limitImportForm(w, r)
					} else {
						limited = s.limitAttachmentForm(w, r)
					}
					if !limited {
						return
					}
				}
				// ParseForm does not accept query parameters as CSRF proof.
				// Multipart handlers can send the header to preserve streaming.
				var parseErr error
				if multipartForm {
					parseErr = r.ParseMultipartForm(32 << 20)
				} else {
					parseErr = r.ParseForm()
				}
				if parseErr != nil {
					var maxErr *http.MaxBytesError
					if errors.As(parseErr, &maxErr) {
						s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
						return
					}
					s.authError(w, r, "error.csrf", http.StatusForbidden)
					return
				}
				provided = r.PostForm.Get("csrf_token")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(provided)) != 1 {
				s.authError(w, r, "error.csrf", http.StatusForbidden)
				return
			}
		} else if token == "" {
			var nonce [32]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				s.log.Error("generate CSRF token", "err", err)
				s.authError(w, r, "error.internal", http.StatusInternalServerError)
				return
			}
			value := base64.RawURLEncoding.EncodeToString(nonce[:])
			token = value + "." + s.signCSRF(value)
			http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/",
				HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfContextKey{}, token)))
	})
}

func isAttachmentUpload(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) == 3 && parts[0] == "days" && parts[2] == "attachments"
}

func isImportUpload(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/settings/import" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "multipart/form-data")
}

func (s *Server) limitImportForm(w http.ResponseWriter, r *http.Request) bool {
	limit, err := s.portability.UploadLimit(r.Context(), CurrentUser(r.Context()).ID)
	if err != nil {
		s.log.Error("check import form allowance", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, multipartBodyLimit(limit))
	return true
}

func (s *Server) limitAttachmentForm(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	dayID := parseID(parts[1])
	if dayID <= 0 {
		http.NotFound(w, r)
		return false
	}
	remaining, err := s.media.Remaining(r.Context(), CurrentUser(r.Context()).ID, dayID)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		s.log.Error("check attachment form allowance", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, multipartBodyLimit(remaining))
	return true
}
