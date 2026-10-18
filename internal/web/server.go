// Package web contains the HTTP server, routing, handlers, and middleware.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/config"
	"pandadays/internal/days"
	"pandadays/internal/i18n"
	"pandadays/internal/media"
	assets "pandadays/web"
)

const shutdownTimeout = 10 * time.Second

type Server struct {
	cfg     config.Config
	log     *slog.Logger
	db      *sql.DB
	i18n    *i18n.Bundle
	pages   map[string]*template.Template
	static  fs.FS
	auth    *auth.Service
	csrfKey [32]byte

	collections *days.Groups
	tags        *days.Groups
	days        *days.Days
	media       *media.Store

	// now is the clock used for date calculations; tests may replace it.
	now func() time.Time
}

// New builds a Server. database may be nil (e.g. in tests that need no storage).
func New(cfg config.Config, logger *slog.Logger, database *sql.DB) (*Server, error) {
	bundle, err := i18n.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	pages, err := parsePages(assets.Templates())
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: logger, db: database, i18n: bundle, pages: pages, static: assets.Static(), now: time.Now}
	if _, err := rand.Read(s.csrfKey[:]); err != nil {
		return nil, fmt.Errorf("initialize CSRF key: %w", err)
	}
	if database != nil {
		s.auth, err = auth.NewService(database)
		if err != nil {
			return nil, err
		}
		s.collections = days.NewCollections(database)
		s.tags = days.NewTags(database)
		s.days = days.NewDays(database)
		s.media = media.New(database, filepath.Join(cfg.DataDir, "media"))
	}
	return s, nil
}

// templateFuncs are placeholders for parsing; render rebinds "t" to the request language.
var templateFuncs = template.FuncMap{
	"t":     func(key string, _ ...any) string { return key },
	"bytes": formatBytes,
}

// parsePages builds one template set per page, each combining layout.html with the page file.
func parsePages(tfs fs.FS) (map[string]*template.Template, error) {
	names, err := fs.Glob(tfs, "*.html")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template)
	for _, name := range names {
		if name == "layout.html" {
			continue
		}
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(tfs, "layout.html", name)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(s.static)))
	mux.Handle("GET /{$}", RequireLogin(http.HandlerFunc(s.handleHome)))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.Handle("GET /password", RequireLogin(http.HandlerFunc(s.handlePasswordForm)))
	mux.Handle("POST /password", RequireLogin(http.HandlerFunc(s.handlePassword)))
	mux.Handle("POST /settings/timezone", RequireLogin(http.HandlerFunc(s.handleTimezone)))
	if s.db != nil {
		s.registerGroupRoutes(mux, s.collections, "collections")
		s.registerGroupRoutes(mux, s.tags, "tags")
		s.registerDayRoutes(mux)
		s.registerAttachmentRoutes(mux)
	}

	return chain(mux,
		recoverPanics(s.log),
		requestLogger(s.log),
		securityHeaders,
		s.loadCurrentUser,
		s.resolveLang,
		forcePasswordChange,
		s.csrf,
	)
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("server listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok"))
}

type pageData struct {
	Lang      string
	CSRFToken string
	User      *auth.User
	// CaptureTimezone loads the script that posts the browser timezone (SPEC §4.5).
	CaptureTimezone bool
}

func newPageData(r *http.Request) pageData {
	user := CurrentUser(r.Context())
	return pageData{
		Lang:            i18n.LangFrom(r.Context()),
		CSRFToken:       CSRFToken(r.Context()),
		User:            user,
		CaptureTimezone: user != nil && user.Timezone == "" && !user.MustChangePassword,
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.renderStatus(w, r, page, http.StatusOK, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, page string, status int, data any) {
	s.execute(w, r, page, "layout", status, data)
}

// renderPartial renders one named template of a page set without the layout (htmx swaps).
func (s *Server) renderPartial(w http.ResponseWriter, r *http.Request, page, name string, data any) {
	s.execute(w, r, page, name, http.StatusOK, data)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request, page, name string, status int, data any) {
	base, ok := s.pages[page]
	if !ok {
		s.log.Error("template not found", "page", page)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	t, err := base.Clone()
	if err != nil {
		s.log.Error("clone template", "page", page, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	t.Funcs(template.FuncMap{"t": s.i18n.Func(i18n.LangFrom(r.Context()))})

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render template", "page", page, "path", r.URL.Path, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func staticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || r.URL.Path[len(r.URL.Path)-1] == '/' {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}
