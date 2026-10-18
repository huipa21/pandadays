package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/media"
)

func attachmentServer(t *testing.T) (*Server, int64, string, days.Day) {
	t.Helper()
	s, db, user, token := authenticatedServer(t)
	s.media = media.New(db, filepath.Join(t.TempDir(), "media"))
	day, err := s.days.Create(context.Background(), user.ID,
		days.DayInput{Title: "Attachments", Date: "2026-10-18"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, user.ID, token, day
}

func uploadAttachment(t *testing.T, s *Server, token string, dayID int64, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, dayPath(dayID)+"/attachments", &body)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("X-CSRF-Token", csrfTokenForTest(t, s, token))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func csrfTokenForTest(t *testing.T, s *Server, token string) string {
	t.Helper()
	return sessionCSRF(t, s, token)
}

func attachmentRequest(s *Server, method, path, token, csrf string) *httptest.ResponseRecorder {
	r := sessionRequest(method, path, token)
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func TestAttachmentQuotaAndUsage(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	if _, err := s.db.Exec(`UPDATE users SET quota_bytes = 5, max_card_bytes = 4 WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if rr := uploadAttachment(t, s, token, day.ID, "data.txt", []byte("12345")); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("exceeded card limit: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.db.Exec(`UPDATE users SET max_card_bytes = 10 WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if rr := uploadAttachment(t, s, token, day.ID, "data.txt", []byte("12345")); rr.Code != http.StatusSeeOther {
		t.Fatalf("within limits: %d %s", rr.Code, rr.Body.String())
	}
	if rr := uploadAttachment(t, s, token, day.ID, "second.txt", []byte("1")); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("exceeded user limit: %d %s", rr.Code, rr.Body.String())
	}
	var used int64
	if err := s.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, owner).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 5 {
		t.Fatalf("used_bytes = %d, want 5", used)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE owner_id = ?`, owner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored attachments = %d, want 1", count)
	}
}

func TestAttachmentAuthorizationSafeDownloadRangeAndDelete(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	body := []byte("<html>secret</html>")
	if rr := uploadAttachment(t, s, token, day.ID, "page.html", body); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload HTML: %d %s", rr.Code, rr.Body.String())
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM attachments WHERE owner_id = ?`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	attachment, err := s.media.Get(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	filePath := s.media.Path(owner, attachment)
	path := "/attachments/" + strconv.FormatInt(id, 10)
	download := attachmentRequest(s, http.MethodGet, path, token, "")
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") ||
		!bytes.Equal(download.Body.Bytes(), body) {
		t.Fatalf("unsafe HTML response: %d headers=%v body=%q", download.Code, download.Header(), download.Body.String())
	}
	rangeRequest := sessionRequest(http.MethodGet, path, token)
	rangeRequest.Header.Set("Range", "bytes=1-3")
	rangeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "htm" ||
		rangeResponse.Header().Get("Content-Range") != "bytes 1-3/19" {
		t.Fatalf("range response: %d %v %q", rangeResponse.Code, rangeResponse.Header(), rangeResponse.Body.String())
	}

	_, bobToken := secondUser(t, s)
	if rr := attachmentRequest(s, http.MethodGet, path, bobToken, ""); rr.Code != http.StatusNotFound {
		t.Errorf("foreign attachment download: %d", rr.Code)
	}
	if rr := uploadAttachment(t, s, bobToken, day.ID, "foreign.txt", []byte("x")); rr.Code != http.StatusNotFound {
		t.Errorf("foreign day upload: %d", rr.Code)
	}
	defaultCollection, err := s.collections.DefaultID(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	var bobID int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE username = 'Bob'`).Scan(&bobID); err != nil {
		t.Fatal(err)
	}
	res, err := s.db.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'collection', ?)`,
		owner, defaultCollection)
	if err != nil {
		t.Fatal(err)
	}
	shareID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO share_recipients (share_id, recipient_id) VALUES (?, ?)`, shareID, bobID); err != nil {
		t.Fatal(err)
	}
	if rr := attachmentRequest(s, http.MethodGet, path, bobToken, ""); rr.Code != http.StatusOK {
		t.Errorf("shared collection attachment: %d", rr.Code)
	}
	if _, err := s.db.Exec(`UPDATE days SET archived_at = ? WHERE id = ?`, time.Now().Unix(), day.ID); err != nil {
		t.Fatal(err)
	}
	if rr := attachmentRequest(s, http.MethodGet, path, bobToken, ""); rr.Code != http.StatusNotFound {
		t.Errorf("archived collection attachment: %d", rr.Code)
	}
	res, err = s.db.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'day', ?)`, owner, day.ID)
	if err != nil {
		t.Fatal(err)
	}
	directShareID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO share_recipients (share_id, recipient_id) VALUES (?, ?)`, directShareID, bobID); err != nil {
		t.Fatal(err)
	}
	if rr := attachmentRequest(s, http.MethodGet, path, bobToken, ""); rr.Code != http.StatusOK {
		t.Errorf("directly shared archived attachment: %d", rr.Code)
	}
	csrf := csrfTokenForTest(t, s, token)
	deleted := attachmentRequest(s, http.MethodPost, path+"/delete", token, csrf)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != dayPath(day.ID) {
		t.Fatalf("delete response: %d %q", deleted.Code, deleted.Header().Get("Location"))
	}
	var used int64
	if err := s.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, owner).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("used_bytes after delete = %d", used)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("attachment file remains after delete: %v", err)
	}
}

func TestAttachmentNativeMultipartFormRemainsSupported(t *testing.T) {
	s, _, token, day := attachmentServer(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", csrfTokenForTest(t, s, token)); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "native.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("native")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, dayPath(day.ID)+"/attachments", &body)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	r.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != dayPath(day.ID) {
		t.Fatalf("native multipart form: %d %q %s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
}

func TestAttachmentSVGIsDownloadAndBackgroundCleanup(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if rr := uploadAttachment(t, s, token, day.ID, "vector.svg", svg); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload SVG: %d %s", rr.Code, rr.Body.String())
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM attachments WHERE owner_id = ?`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	download := attachmentRequest(s, http.MethodGet, "/attachments/"+strconv.FormatInt(id, 10), token, "")
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unsafe SVG response: %d %v", download.Code, download.Header())
	}

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	if rr := uploadAttachment(t, s, token, day.ID, "image.png", png); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload image: %d %s", rr.Code, rr.Body.String())
	}
	var imageID int64
	if err := s.db.QueryRow(`SELECT id FROM attachments WHERE owner_id = ? AND kind = 'image'`, owner).Scan(&imageID); err != nil {
		t.Fatal(err)
	}
	csrf := csrfTokenForTest(t, s, token)
	background := attachmentRequest(s, http.MethodPost,
		dayPath(day.ID)+"/attachments/"+strconv.FormatInt(imageID, 10)+"/background", token, csrf)
	if background.Code != http.StatusSeeOther {
		t.Fatalf("set background: %d %s", background.Code, background.Body.String())
	}
	if err := s.media.Delete(context.Background(), owner, imageID); err != nil {
		t.Fatal(err)
	}
	var bg any
	if err := s.db.QueryRow(`SELECT style_bg_attachment_id FROM days WHERE id = ?`, day.ID).Scan(&bg); err != nil {
		t.Fatal(err)
	}
	if bg != nil {
		t.Fatalf("background attachment not cleared: %v", bg)
	}
	page := get(s.Handler(), dayPath(day.ID), token)
	for _, want := range []string{"Card storage:", "Account storage:", "vector.svg", `name="file"`, "Record voice note"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("card missing %q", want)
		}
	}
}

func TestDeletingDayRemovesAttachmentFilesAndUsage(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	if rr := uploadAttachment(t, s, token, day.ID, "note.txt", []byte("note")); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", rr.Code)
	}
	files, err := s.media.ListDay(context.Background(), owner, day.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %v %v", files, err)
	}
	path := s.media.Path(owner, files[0])
	rr := sessionPost(t, s, dayPath(day.ID)+"/delete", token, url.Values{})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete day: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("attachment file remains after day delete: %v", err)
	}
	var used int64
	if err := s.db.QueryRow(`SELECT used_bytes FROM users WHERE id = ?`, owner).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("used_bytes after day delete = %d", used)
	}
}

func TestAttachmentEmptyUploadRejectedByQuotaStatus(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	if _, err := s.db.Exec(`UPDATE users SET quota_bytes = 0 WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if rr := uploadAttachment(t, s, token, day.ID, "empty.txt", nil); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("zero remaining upload: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAttachmentReadBoundError(t *testing.T) {
	s, _, token, day := attachmentServer(t)
	r := httptest.NewRequest(http.MethodPost, dayPath(day.ID)+"/attachments", io.NopCloser(strings.NewReader(strings.Repeat("x", 70<<10))))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	r.Header.Set("Content-Type", "multipart/form-data; boundary=bad")
	r.Header.Set("X-CSRF-Token", csrfTokenForTest(t, s, token))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("malformed oversized request: %d", rr.Code)
	}
}
