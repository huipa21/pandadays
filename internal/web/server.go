// Package web contains the HTTP server, routing, handlers, and middleware.
package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"pandadays/internal/config"
	assets "pandadays/web"
)

const shutdownTimeout = 10 * time.Second

type Server struct {
	cfg    config.Config
	log    *slog.Logger
	pages  map[string]*template.Template
	static fs.FS
}

func New(cfg config.Config, logger *slog.Logger) (*Server, error) {
	pages, err := parsePages(assets.Templates())
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: logger, pages: pages, static: assets.Static()}, nil
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
		t, err := template.New(name).ParseFS(tfs, "layout.html", name)
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
	s.render(w, r, "home.html", pageData{Lang: s.cfg.Lang})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.log.Error("template not found", "page", page)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		s.log.Error("render template", "page", page, "path", r.URL.Path, "err", err)
	}
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
