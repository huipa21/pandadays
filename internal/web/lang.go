package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"pandadays/internal/i18n"
)

// resolveLang stores the request language in the context (SPEC §4.5).
// A supported ?lang= query parameter overrides everything, for previewing
// pages in another language without changing settings.
func (s *Server) resolveLang(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.Normalize(r.URL.Query().Get("lang"))
		if lang == "" {
			// User language is added once sessions exist (T04).
			lang = i18n.Resolve("", s.siteLang(r.Context()), r.Header.Get("Accept-Language"))
		}
		w.Header().Add("Vary", "Accept-Language")
		next.ServeHTTP(w, r.WithContext(i18n.WithLang(r.Context(), lang)))
	})
}

// siteLang returns the admin-set site_lang setting, else PANDA_LANG if it was
// set explicitly, else "" so that Accept-Language decides.
func (s *Server) siteLang(ctx context.Context) string {
	if s.db != nil {
		var v string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'site_lang'`).Scan(&v)
		switch {
		case err == nil:
			if l := i18n.Normalize(v); l != "" {
				return l
			}
		case !errors.Is(err, sql.ErrNoRows):
			s.log.Warn("read site_lang", "err", err)
		}
	}
	if s.cfg.LangSet {
		return s.cfg.Lang
	}
	return ""
}
