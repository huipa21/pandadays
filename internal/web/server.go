// Package web contains the HTTP server, routing, handlers, and middleware.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/backup"
	"pandadays/internal/config"
	"pandadays/internal/days"
	"pandadays/internal/i18n"
	"pandadays/internal/media"
	"pandadays/internal/portability"
	"pandadays/internal/share"
	assets "pandadays/web"
)

const shutdownTimeout = 10 * time.Second

type Server struct {
	cfg    config.Config
	log    *slog.Logger
	db     *sql.DB
	i18n   *i18n.Bundle
	pages  map[string]*template.Template
	static fs.FS
	// assetVersion is appended to static URLs to bust browser caches on upgrade.
	assetVersion string
	auth         *auth.Service
	csrfKey      [32]byte

	collections     *days.Groups
	tags            *days.Groups
	days            *days.Days
	media           *media.Store
	shares          *share.Store
	portability     *portability.Service
	backup          *backup.Service
	handler         http.Handler
	gate            sync.Mutex
	active          int
	maintenance     bool
	drained         chan struct{}
	maintenanceLang string

	// now is the clock used for date calculations; tests may replace it.
	now func() time.Time
}

// New builds a Server. database may be nil (e.g. in tests that need no storage).
func New(cfg config.Config, logger *slog.Logger, database *sql.DB, appVersion ...string) (*Server, error) {
	bundle, err := i18n.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	pages, err := parsePages(assets.Templates())
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: logger, db: database, i18n: bundle, pages: pages, static: assets.Static(), now: time.Now}
	if s.assetVersion, err = staticVersion(s.static); err != nil {
		return nil, err
	}
	if _, err := rand.Read(s.csrfKey[:]); err != nil {
		return nil, fmt.Errorf("initialize CSRF key: %w", err)
	}
	version := "dev"
	if len(appVersion) > 0 {
		version = appVersion[0]
	}
	s.backup = backup.New(cfg.DataDir, version)
	if err := s.bindDatabase(database); err != nil {
		return nil, err
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) bindDatabase(database *sql.DB) error {
	if database == nil {
		return nil
	}
	service, err := auth.NewService(database)
	if err != nil {
		return err
	}
	s.db, s.auth = database, service
	s.collections = days.NewCollections(database)
	s.tags = days.NewTags(database)
	s.days = days.NewDays(database)
	s.media = media.New(database, filepath.Join(s.cfg.DataDir, "media"))
	s.shares = share.New(database)
	s.portability = portability.New(database, filepath.Join(s.cfg.DataDir, "media"), filepath.Join(s.cfg.DataDir, "tmp"))
	s.prepareMediaStore()
	return nil
}

// prepareMediaStore moves legacy per-owner files into the shared content store
// and removes blobs left unreferenced by an interrupted deletion. Failures are
// logged; legacy files stay readable and unreferenced blobs stay queued.
func (s *Server) prepareMediaStore() {
	ctx := context.Background()
	moved, err := s.media.MigrateLegacy(ctx)
	if err != nil && s.log != nil {
		s.log.Error("move legacy attachments into content store", "err", err)
	}
	if moved > 0 && s.log != nil {
		s.log.Info("moved legacy attachments into content store", "count", moved)
	}
	if err := s.media.Collect(ctx); err != nil && s.log != nil {
		s.log.Error("remove unreferenced media", "err", err)
	}
}

// Close releases the current database handle after serving has stopped.
func (s *Server) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// templateFuncs are placeholders for parsing; execute rebinds "t" to the request language
// and "asset" to the content-versioned static URL.
var templateFuncs = template.FuncMap{
	"t":             func(key string, _ ...any) string { return key },
	"asset":         func(name string) string { return "/static/" + name },
	"bytes":         formatBytes,
	"megabytes":     formatMegabytes,
	"localtime":     func(time.Time) template.HTML { return "" },
	"pendingShares": func() int { return 0 },
	"privatePrefix": func() string { return "" },
}

// localTime renders t in the viewer's timezone. Without a saved timezone the
// element is marked so app.js re-renders it in the browser's timezone.
func localTime(viewer *auth.User) func(time.Time) template.HTML {
	return func(t time.Time) template.HTML {
		attr := ""
		if viewer == nil || viewer.Timezone == "" {
			attr = " data-local-time"
		}
		t = t.In(userLocation(viewer))
		return template.HTML(`<time datetime="` + t.Format(time.RFC3339) + `"` + attr + `>` +
			template.HTMLEscapeString(t.Format("2006-01-02 15:04 MST")) + `</time>`)
	}
}

// staticVersion hashes every static file so asset URLs change whenever their
// content does, preventing stale cached CSS/JS after an upgrade.
func staticVersion(fsys fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash static assets: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
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
	return chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handler.ServeHTTP(w, r)
	}), recoverPanics(s.log), requestLogger(s.log), securityHeaders, s.maintenanceGate)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(s.static)))
	mux.Handle("GET /{$}", RequireLogin(http.HandlerFunc(s.handleHome)))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.Handle("POST /vault/unlock", RequireLogin(http.HandlerFunc(s.handleVaultUnlock)))
	mux.Handle("POST /vault/lock", RequireLogin(http.HandlerFunc(s.handleVaultLock)))
	mux.Handle("GET /password", RequireLogin(http.HandlerFunc(s.handlePasswordForm)))
	mux.Handle("POST /password", RequireLogin(http.HandlerFunc(s.handlePassword)))
	mux.Handle("GET /profile", RequireLogin(http.HandlerFunc(s.handleProfile)))
	mux.Handle("POST /profile", RequireLogin(http.HandlerFunc(s.saveProfile)))
	mux.Handle("POST /profile/avatar/delete", RequireLogin(http.HandlerFunc(s.removeAvatar)))
	mux.Handle("GET /settings", RequireLogin(http.HandlerFunc(s.handleSettings)))
	mux.Handle("POST /settings", RequireLogin(http.HandlerFunc(s.saveSettings)))
	mux.Handle("POST /settings/language", RequireLogin(http.HandlerFunc(s.saveLanguage)))
	mux.Handle("GET /users/{id}/avatar", RequireLogin(http.HandlerFunc(s.handleAvatar)))
	mux.Handle("POST /settings/timezone", RequireLogin(http.HandlerFunc(s.handleTimezone)))
	if s.db != nil {
		s.registerGroupRoutes(mux, s.collections, "collections")
		s.registerGroupRoutes(mux, s.tags, "tags")
		s.registerDayRoutes(mux)
		s.registerAttachmentRoutes(mux)
		s.registerMediaRoutes(mux)
		s.registerShareRoutes(mux)
		s.registerAdminRoutes(mux)
		s.registerPortabilityRoutes(mux)
		s.registerBackupRoutes(mux)
	}

	return chain(mux,
		s.loadCurrentUser,
		s.resolveLang,
		forcePasswordChange,
		adminSiteOnly,
		s.vaultNamespace,
		s.csrf,
		s.vaultGate,
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
	VaultUnlocked bool
	Private       bool
	Lang          string
	CSRFToken     string
	User          *auth.User
	ReturnTo      string
	LanguageURL   string
	SearchQuery   string
	// CaptureTimezone loads the script that posts the browser timezone (SPEC §4.5).
	CaptureTimezone bool
}

func newPageData(r *http.Request) pageData {
	user := CurrentUser(r.Context())
	u := *r.URL
	q := u.Query()
	nextLang := "zh"
	if i18n.LangFrom(r.Context()) == "zh" {
		nextLang = "en"
	}
	q.Set("lang", nextLang)
	u.RawQuery = q.Encode()
	searchQuery := ""
	if r.URL.Path == "/" {
		searchQuery = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	return pageData{
		Lang:            i18n.LangFrom(r.Context()),
		CSRFToken:       CSRFToken(r.Context()),
		User:            user,
		Private:         days.IsPrivate(r.Context()),
		VaultUnlocked:   CurrentSession(r.Context()) != nil && CurrentSession(r.Context()).VaultUnlocked,
		ReturnTo:        privatePath(r, r.URL.RequestURI()),
		LanguageURL:     u.RequestURI(),
		SearchQuery:     searchQuery,
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
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	t, err := base.Clone()
	if err != nil {
		s.log.Error("clone template", "page", page, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	pending := 0
	if user := CurrentUser(r.Context()); name == "layout" && user != nil && !user.IsAdmin && s.shares != nil {
		pending, err = s.shares.PendingCount(r.Context(), user.ID)
		if err != nil {
			s.shareError(w, r, "count invitations", err)
			return
		}
	}
	t.Funcs(template.FuncMap{
		"privatePrefix": func() string {
			if days.IsPrivate(r.Context()) {
				return "/private"
			}
			return ""
		},
		"t":             s.i18n.Func(i18n.LangFrom(r.Context())),
		"asset":         func(name string) string { return "/static/" + name + "?v=" + s.assetVersion },
		"localtime":     localTime(CurrentUser(r.Context())),
		"pendingShares": func() int { return pending },
	})

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render template", "page", page, "path", r.URL.Path, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
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
