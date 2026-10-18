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
		token := ""
		if session := CurrentSession(r.Context()); session != nil {
			token = session.CSRFToken
		} else if cookie, err := r.Cookie(csrfCookieName); err == nil && s.validAnonymousCSRF(cookie.Value) {
			token = cookie.Value
		}
		if !safe {
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" {
				if isAttachmentUpload(r) {
					if CurrentUser(r.Context()) == nil {
						authRedirect(w, r, "/login")
						return
					}
					if !s.limitAttachmentForm(w, r) {
						return
					}
				}
				// ParseForm does not accept query parameters as CSRF proof.
				// Multipart handlers can send the header to preserve streaming.
				var parseErr error
				if isAttachmentUpload(r) {
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
