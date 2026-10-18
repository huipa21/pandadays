package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/db"
)

func backupServer(t *testing.T) (*Server, *auth.User, string) {
	t.Helper()
	root := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(root, db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(context.Background(), conn, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWith(t, func(key string) string {
		if key == "PANDA_DATA_DIR" {
			return root
		}
		return ""
	}, conn)
	t.Cleanup(func() { s.Close() })
	user, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{
		Username: "admin", Email: "admin@example.com", Password: "password123", IsAdmin: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.auth.CreateSession(context.Background(), user.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, user, token
}

func restoreRequest(t *testing.T, token, csrf, password string, data []byte, header bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	// The file deliberately comes before the password and CSRF fields.
	part, err := writer.CreateFormFile("file", "backup.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("password", password); err != nil {
		t.Fatal(err)
	}
	if !header {
		if err := writer.WriteField("csrf_token", csrf); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/backup/restore", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	if header {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	return r
}

func backupBytes(t *testing.T, s *Server, token string, retain bool) []byte {
	t.Helper()
	form := url.Values{}
	if retain {
		form.Set("retain", "1")
	}
	rr := sessionPost(t, s, "/admin/backup", token, form)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("create backup: %d %s", rr.Code, rr.Body.String())
	}
	manifest, err := s.backup.Validate(context.Background(), bytes.NewReader(rr.Body.Bytes()))
	if err != nil || manifest.AppVersion != "dev" {
		t.Fatalf("downloaded archive: %+v %v", manifest, err)
	}
	return rr.Body.Bytes()
}

func TestBackupAuthorizationAndRetention(t *testing.T) {
	s, user, token := backupServer(t)
	if rr := get(s.Handler(), "/admin/backup", ""); rr.Code != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", rr.Code)
	}
	if _, err := s.db.Exec(`UPDATE users SET is_admin=0 WHERE id=?`, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/backup", "/admin/backup/pandadays-backup-test.tar.gz"} {
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
			t.Fatalf("non-admin GET %s: %d", path, rr.Code)
		}
	}
	for _, path := range []string{"/admin/backup", "/admin/backup/pandadays-backup-test.tar.gz/delete"} {
		if rr := sessionPost(t, s, path, token, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("non-admin POST %s: %d", path, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, restoreRequest(t, token, sessionCSRF(t, s, token), "password123", nil, false))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-admin restore: %d", rr.Code)
	}
	makeAdmin(t, s, user.ID)
	if rr := postForm(s.Handler(), "/admin/backup", nil, &http.Cookie{Name: auth.SessionCookieName, Value: token}); rr.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", rr.Code)
	}
	if _, err := s.db.Exec(`UPDATE settings SET value='1' WHERE key='backup_retention'`); err != nil {
		t.Fatal(err)
	}
	first := backupBytes(t, s, token, true)
	files, err := s.backup.List()
	if err != nil || len(files) != 1 {
		t.Fatalf("retained list: %+v %v", files, err)
	}
	name := files[0].Name
	rr = get(s.Handler(), "/admin/backup/"+name, token)
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), first) || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("retained download differs or is cacheable")
	}
	backupBytes(t, s, token, true)
	files, err = s.backup.List()
	if err != nil || len(files) != 1 || files[0].Name == name {
		t.Fatalf("retention not pruned: %+v %v", files, err)
	}
	name = files[0].Name
	rr = sessionPost(t, s, "/admin/backup/"+name+"/delete", token, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed delete: %d", rr.Code)
	}
	rr = sessionPost(t, s, "/admin/backup/"+name+"/delete", token, url.Values{"confirm": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	backupBytes(t, s, token, false)
	files, err = s.backup.List()
	if err != nil || len(files) != 0 {
		t.Fatalf("download-only was retained: %+v %v", files, err)
	}
	backupBytes(t, s, token, true)
	form := siteSettingsForm()
	form.Set("backup_retention", "0")
	rr = sessionPost(t, s, "/admin/settings", token, form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("save zero retention: %d %s", rr.Code, rr.Body.String())
	}
	backupBytes(t, s, token, true)
	files, err = s.backup.List()
	if err != nil || len(files) != 0 {
		t.Fatalf("zero retention: %+v %v", files, err)
	}
	for _, lang := range []string{"en", "zh"} {
		rr = get(s.Handler(), "/admin/backup?lang="+lang, token)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `<html lang="`+lang+`">`) || strings.Contains(rr.Body.String(), ">backup.") {
			t.Fatalf("backup page %s: %d %s", lang, rr.Code, rr.Body.String())
		}
	}
}

func TestRestoreHTTPDrainsAndReopens(t *testing.T) {
	s, user, token := backupServer(t)
	ctx := context.Background()
	day, err := s.days.Create(ctx, user.ID, days.DayInput{Title: "Original", Date: "2026-10-18"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := s.media.Upload(ctx, user.ID, day.ID, "test.txt", strings.NewReader("original media"))
	if err != nil {
		t.Fatal(err)
	}
	data := backupBytes(t, s, token, false)
	if _, err := s.db.Exec(`UPDATE days SET title='Changed' WHERE owner_id=? AND id=?`, user.ID, day.ID); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("changed-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, user.ID); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(s.cfg.DataDir, "media", "1", attachment.UUID)
	if err := os.WriteFile(mediaPath, []byte("changed media"), 0o600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	original := s.handler
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(started)
			<-release
			w.WriteHeader(http.StatusNoContent)
			return
		}
		original.ServeHTTP(w, r)
	})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client := server.Client()
	client.Timeout = 15 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	slowDone := make(chan error, 1)
	go func() {
		res, err := client.Get(server.URL + "/slow")
		if err == nil {
			err = res.Body.Close()
		}
		slowDone <- err
	}()
	<-started
	csrf := sessionCSRF(t, s, token)
	request := restoreRequest(t, token, csrf, "changed-password", data, false)
	request.URL, _ = url.Parse(server.URL + request.URL.Path)
	request.RequestURI = ""
	type result struct {
		response *http.Response
		err      error
	}
	done := make(chan result, 1)
	go func() {
		res, err := client.Do(request)
		done <- result{res, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.gate.Lock()
		maintenance := s.maintenance
		s.gate.Unlock()
		if maintenance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore never entered maintenance")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, path := range []string{"/healthz", "/", "/login", "/static/app.css", "/admin/backup"} {
		response, err := client.Get(server.URL + path + "?lang=zh")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), s.i18n.T("zh", "backup.maintenance")) || response.Header.Get("Retry-After") == "" {
			t.Fatalf("during restore %s: %d %s %v", path, response.StatusCode, body, err)
		}
	}
	select {
	case result := <-done:
		t.Fatalf("restore did not drain slow request: %+v", result)
	default:
	}
	oldDB := s.db
	close(release)
	resultValue := <-done
	if resultValue.err != nil {
		t.Fatal(resultValue.err)
	}
	response := resultValue.response
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/login" {
		t.Fatalf("restore: %d %v", response.StatusCode, response.Header)
	}
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
	if s.db == oldDB || oldDB.Ping() == nil {
		t.Fatal("database handle was not replaced and closed")
	}
	restored, err := s.days.Get(ctx, user.ID, day.ID)
	if err != nil || restored.Title != "Original" {
		t.Fatalf("restored day: %+v %v", restored, err)
	}
	content, err := os.ReadFile(mediaPath)
	if err != nil || string(content) != "original media" {
		t.Fatalf("restored media: %q %v", content, err)
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions not wiped: %d %v", sessions, err)
	}
	if rr := get(s.Handler(), "/", token); rr.Code != http.StatusSeeOther {
		t.Fatalf("old session survived: %d", rr.Code)
	}
	if _, err := s.auth.Authenticate(ctx, "test", user.Username, "password123", time.Now()); err != nil {
		t.Fatalf("restored credentials: %v", err)
	}
	fresh, _, err := s.auth.CreateSession(ctx, user.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the HTTP server's original handler, not a newly constructed one.
	for _, path := range []string{"/collections", "/tags", "/settings", "/shared", "/settings/export", "/admin/backup"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: fresh})
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("rebound %s: %d %s", path, res.StatusCode, body)
		}
	}
	files, err := s.backup.List()
	if err != nil || len(files) != 1 || !strings.HasPrefix(files[0].Name, "pre-restore-") {
		t.Fatalf("missing recovery snapshot: %+v %v", files, err)
	}
	assertNoRestoreUploads(t, s)
}

func assertNoRestoreUploads(t *testing.T, s *Server) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(s.cfg.DataDir, "tmp", "restore-upload-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("restore upload leaked: %+v %v", matches, err)
	}
}

func TestRestoreRejectsAndRecovers(t *testing.T) {
	s, _, token := backupServer(t)
	data := backupBytes(t, s, token, false)
	csrf := sessionCSRF(t, s, token)
	now := time.Now()
	s.now = func() time.Time { return now }
	for _, tc := range []struct {
		name, password, csrf string
		data                 []byte
		status               int
		header               bool
	}{
		{"csrf", "password123", "", data, http.StatusForbidden, false},
		{"password", "wrong-password", csrf, data, http.StatusUnprocessableEntity, false},
		{"archive", "password123", csrf, []byte("not a backup"), http.StatusUnprocessableEntity, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := s.db
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, restoreRequest(t, token, tc.csrf, tc.password, tc.data, tc.header))
			if rr.Code != tc.status || s.db != old {
				t.Fatalf("rejection: %d %s", rr.Code, rr.Body.String())
			}
			assertNoRestoreUploads(t, s)
		})
		now = now.Add(2 * time.Second)
	}
	// A filesystem failure after draining/closing must reopen the original DB.
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "backups"), []byte("blocks snapshot directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := s.db
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, restoreRequest(t, token, csrf, "password123", data, true))
	if rr.Code != http.StatusInternalServerError || old == s.db || s.db.Ping() != nil {
		t.Fatalf("restore failure recovery: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.auth.LookupSession(context.Background(), token, time.Now()); errors.Is(err, auth.ErrSession) || err != nil {
		t.Fatalf("failed restore invalidated session: %v", err)
	}
	if rr := get(s.Handler(), "/", token); rr.Code != http.StatusOK {
		t.Fatalf("maintenance did not end: %d %s", rr.Code, rr.Body.String())
	}
	assertNoRestoreUploads(t, s)
}

func TestConcurrentMaintenanceDoesNotDeadlock(t *testing.T) {
	s := newTestServer(t)
	start := make(chan struct{}, 2)
	barrier := make(chan struct{})
	results := make(chan int, 2)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start <- struct{}{}
		<-barrier
		if s.beginMaintenance(w, r) {
			defer s.endMaintenance()
			w.WriteHeader(http.StatusNoContent)
		}

	})
	for range 2 {
		go func() {
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", nil))
			results <- rr.Code
		}()
	}
	<-start
	<-start
	close(barrier)
	statuses := map[int]int{}
	for range 2 {
		select {
		case status := <-results:
			statuses[status]++
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent maintenance deadlocked")
		}
	}
	if statuses[http.StatusNoContent] != 1 || statuses[http.StatusServiceUnavailable] != 1 {
		t.Fatalf("maintenance results: %v", statuses)
	}
}

func TestRestoreReopenFailureStaysInMaintenance(t *testing.T) {
	s, _, token := backupServer(t)
	data := backupBytes(t, s, token, false)
	csrf := sessionCSRF(t, s, token)
	// The engine restores its original root, but the HTTP server cannot reopen
	// its configured path. It must not create an empty replacement database.
	s.cfg.DataDir = t.TempDir()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, restoreRequest(t, token, csrf, "password123", data, false))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("reopen failure: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, db.FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created empty database after failed reopen: %v", err)
	}
	if rr := get(s.Handler(), "/healthz?lang=zh", token); rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), s.i18n.T("zh", "backup.maintenance")) {
		t.Fatalf("maintenance lifted after failed reopen: %d %s", rr.Code, rr.Body.String())
	}
	assertNoRestoreUploads(t, s)
}

func TestRestoreMalformedFormCleansUpload(t *testing.T) {
	s, _, token := backupServer(t)
	csrf := sessionCSRF(t, s, token)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "backup.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("staged file"))
	writer.WriteField("password", strings.Repeat("x", (64<<10)+1))
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/admin/backup/restore", &body)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", csrf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, request)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized form field: %d %s", rr.Code, rr.Body.String())
	}
	assertNoRestoreUploads(t, s)
}
