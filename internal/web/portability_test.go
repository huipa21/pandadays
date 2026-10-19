package web

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/media"
	"pandadays/internal/portability"
)

func portabilityServer(t *testing.T) (*Server, *auth.User, string) {
	t.Helper()
	s, db, user, token := authenticatedServer(t)
	root := t.TempDir()
	s.media = media.New(db, filepath.Join(root, "media"))
	s.portability = portability.New(db, filepath.Join(root, "media"), filepath.Join(root, "tmp"))
	return s, user, token
}

// uploadImport posts a multipart form; csrfField puts the token in the form
// body like the browser form does, otherwise it is sent as a header.
func uploadImport(t *testing.T, s *Server, token, csrf string, csrfField bool, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if csrfField && csrf != "" {
		if err := mw.WriteField("csrf_token", csrf); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(data)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/settings/import", &body)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if !csrfField && csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func confirmImport(s *Server, token, csrf, importToken string) *httptest.ResponseRecorder {
	return postForm(s.Handler(), "/settings/import/confirm",
		url.Values{"csrf_token": {csrf}, "token": {importToken}},
		&http.Cookie{Name: auth.SessionCookieName, Value: token})
}

var importTokenRE = regexp.MustCompile(`name="token" value="([0-9a-f]+)"`)

func importToken(t *testing.T, body string) string {
	t.Helper()
	m := importTokenRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no import token in page:\n%s", body)
	}
	return m[1]
}

func countDays(t *testing.T, s *Server, owner int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExportDownload(t *testing.T) {
	s, user, token := portabilityServer(t)
	if rr := get(s.Handler(), "/settings/export", ""); rr.Code != http.StatusSeeOther {
		t.Fatalf("anonymous export: %d", rr.Code)
	}
	if _, err := s.days.Create(context.Background(), user.ID, days.DayInput{Title: "=cmd", Date: "2026-01-02"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rr := get(s.Handler(), "/settings/export", token)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export: %d %v", rr.Code, rr.Header())
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename=pandadays-export-alice-`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	data := rr.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "days.csv" {
		t.Fatalf("archive: %v", err)
	}
	f, _ := zr.File[0].Open()
	var csv bytes.Buffer
	csv.ReadFrom(f)
	if !bytes.HasPrefix(csv.Bytes(), []byte("\xEF\xBB\xBF")) || !strings.Contains(csv.String(), "'=cmd") {
		t.Fatalf("days.csv = %q", csv.String())
	}
}

func TestImportPreviewAndConfirm(t *testing.T) {
	s, user, token := portabilityServer(t)
	csrf := sessionCSRF(t, s, token)
	data := []byte("title,date,collection,tags\nTrip,2026-03-04,Travel,fun\nTrip,2026-03-04,Travel,fun\nBad,not-a-date,,\n")

	if rr := uploadImport(t, s, token, "", true, "days.csv", data); rr.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", rr.Code)
	}
	if rr := uploadImport(t, s, token, "wrong", true, "days.csv", data); rr.Code != http.StatusForbidden {
		t.Fatalf("wrong CSRF: %d", rr.Code)
	}

	rr := uploadImport(t, s, token, csrf, true, "days.csv", data)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Travel") || !strings.Contains(body, "fun") {
		t.Fatalf("preview lacks new groups:\n%s", body)
	}
	importTok := importToken(t, body)
	if countDays(t, s, user.ID) != 0 {
		t.Fatal("preview must not import")
	}

	// Another user cannot confirm Alice's upload.
	bob, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{
		Username: "bob1", Email: "bob@example.com", Password: "password123",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bobToken, _, err := s.auth.CreateSession(context.Background(), bob.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rr := confirmImport(s, bobToken, sessionCSRF(t, s, bobToken), importTok); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-user confirm: %d", rr.Code)
	}
	if countDays(t, s, bob.ID) != 0 {
		t.Fatal("cross-user confirm imported data")
	}

	if rr := confirmImport(s, token, "", importTok); rr.Code != http.StatusForbidden {
		t.Fatalf("confirm without CSRF: %d", rr.Code)
	}
	if rr := confirmImport(s, token, csrf, importTok); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	if n := countDays(t, s, user.ID); n != 1 {
		t.Fatalf("imported days = %d, want 1", n)
	}
	// The staged upload is single-use.
	if rr := confirmImport(s, token, csrf, importTok); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second confirm: %d", rr.Code)
	}

	// Header token with a streamed body works too; everything is now a duplicate,
	// so there is nothing to confirm.
	rr = uploadImport(t, s, token, csrf, false, "days.csv", data)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `name="token"`) {
		t.Fatalf("header upload: %d %s", rr.Code, rr.Body.String())
	}
	if n := countDays(t, s, user.ID); n != 1 {
		t.Fatalf("days after re-upload = %d, want 1", n)
	}
}

func TestImportRejectsUnsafeArchive(t *testing.T) {
	s, user, token := portabilityServer(t)
	csrf := sessionCSRF(t, s, token)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"days.csv", "../evil.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("title,date\nX,2026-01-01\n"))
	}
	zw.Close()
	rr := uploadImport(t, s, token, csrf, true, "export.zip", buf.Bytes())
	if rr.Code != http.StatusUnprocessableEntity || strings.Contains(rr.Body.String(), `name="token"`) {
		t.Fatalf("unsafe archive: %d", rr.Code)
	}
	if countDays(t, s, user.ID) != 0 {
		t.Fatal("unsafe archive imported data")
	}
}
