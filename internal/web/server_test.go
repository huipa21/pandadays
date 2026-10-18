package web

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pandadays/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerWith(t, func(string) string { return "" }, nil)
}

func newTestServerWith(t *testing.T, getenv func(string) string, database *sql.DB) *Server {
	t.Helper()
	cfg, err := config.Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), database)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func TestHealthz(t *testing.T) {
	rr := do(t, newTestServer(t).Handler(), http.MethodGet, "/healthz")
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("got %d %q", rr.Code, rr.Body.String())
	}
}

func TestHomeRendersLayout(t *testing.T) {
	h := newTestServer(t).Handler()
	if rr := do(t, h, http.MethodGet, "/"); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous home: status = %d, location = %q", rr.Code, rr.Header().Get("Location"))
	}
	rr := do(t, h, http.MethodGet, "/login")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="en">`, `/static/app.css`, `/static/htmx.min.js`, `name="viewport"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestStaticAssetsEmbedded(t *testing.T) {
	h := newTestServer(t).Handler()
	for path, ct := range map[string]string{
		"/static/app.css":     "text/css",
		"/static/htmx.min.js": "javascript",
	} {
		rr := do(t, h, http.MethodGet, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, ct) {
			t.Errorf("%s: content-type = %q", path, got)
		}
	}
	if rr := do(t, h, http.MethodGet, "/static/"); rr.Code != http.StatusNotFound {
		t.Errorf("directory listing: status = %d, want 404", rr.Code)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newTestServer(t).Handler()
	if rr := do(t, h, http.MethodGet, "/nope"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown path: status = %d", rr.Code)
	}
	if rr := do(t, h, http.MethodPost, "/healthz"); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz: status = %d", rr.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, path := range []string{"/", "/healthz", "/static/app.css", "/nope"} {
		rr := do(t, h, http.MethodGet, path)
		hdr := rr.Header()
		if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "media-src 'self'") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
		checks := map[string]string{
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
			"X-Content-Type-Options": "nosniff",
		}
		for k, v := range checks {
			if got := hdr.Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", path, k, got, v)
			}
		}
	}
}

func TestRecoverPanics(t *testing.T) {
	h := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	if rr := do(t, h, http.MethodGet, "/"); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestRequestLoggerOmitsQuery(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := requestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	do(t, h, http.MethodGet, "/setup?token=secret")
	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("log leaked query: %s", out)
	}
	if !strings.Contains(out, "status=418") || !strings.Contains(out, "path=/setup") {
		t.Fatalf("unexpected log: %s", out)
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	s := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
