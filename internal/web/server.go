// Package web contains the HTTP server, routing, handlers, and middleware.
package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"pandadays/internal/config"
	"pandadays/internal/i18n"
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
	return &Server{cfg: cfg, log: logger, db: database, i18n: bundle, pages: pages, static: assets.Static()}, nil
}

// templateFuncs are placeholders for parsing; render rebinds "t" to the request language.
var templateFuncs = template.FuncMap{
	"t": func(key string, _ ...any) string { return key },
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
	mux.HandleFunc("GET /{$}", s.handleHome)

	return chain(mux,
		recoverPanics(s.log),
		requestLogger(s.log),
		securityHeaders,
		s.resolveLang,
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
	Lang string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "home.html", pageData{Lang: i18n.LangFrom(r.Context())})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
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
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log.Error("render template", "page", page, "path", r.URL.Path, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
