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

func createDayWithFiles(t *testing.T, s *Server, token, csrf string, headerCSRF bool, values url.Values, files ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, entries := range values {
		for _, value := range entries {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !headerCSRF {
		if err := writer.WriteField("csrf_token", csrf); err != nil {
			t.Fatal(err)
		}
	}
	for i, contents := range files {
		part, err := writer.CreateFormFile("file", "file"+strconv.Itoa(i)+".txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/days", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if headerCSRF {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func TestDayCreateAttachments(t *testing.T) {
	for _, headerCSRF := range []bool{false, true} {
		t.Run(strconv.FormatBool(headerCSRF), func(t *testing.T) {
			s, _, owner, token := authenticatedServer(t)
			csrf := sessionCSRF(t, s, token)
			rr := createDayWithFiles(t, s, token, csrf, headerCSRF, dayValues(), "hello", "world")
			if rr.Code != http.StatusSeeOther {
				t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
			}
			d := onlyDay(t, s, owner.ID)
			files, err := s.media.ListDay(context.Background(), owner.ID, d.ID)
			if err != nil || len(files) != 2 {
				t.Fatalf("attachments: %v %v", files, err)
			}
			usage, err := s.media.Usage(context.Background(), owner.ID, d.ID)
			if err != nil || usage.CardUsed != 10 || usage.UserUsed != 10 {
				t.Fatalf("usage: %+v %v", usage, err)
			}
			for _, file := range files {
				data, err := os.ReadFile(s.media.Path(owner.ID, file))
				if err != nil || (string(data) != "hello" && string(data) != "world") {
					t.Fatalf("file contents: %q %v", data, err)
				}
			}
		})
	}
}

func TestDayCreateAttachmentsValidationAndAuthorization(t *testing.T) {
	s, db, owner, token := authenticatedServer(t)
	csrf := sessionCSRF(t, s, token)
	if _, err := db.Exec(`UPDATE users SET max_card_bytes = 5, quota_bytes = 7 WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := secondUser(t, s)
	foreignCollection, err := s.collections.DefaultID(context.Background(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, csrf string
		form              url.Values
		files             []string
		status            int
	}{
		{"anonymous", "", "", dayValues(), []string{"x"}, http.StatusSeeOther},
		{"csrf", token, "invalid", dayValues(), []string{"x"}, http.StatusForbidden},
		{"invalid fields", token, csrf, dayValues("title", ""), []string{"x"}, http.StatusUnprocessableEntity},
		{"foreign collection", token, csrf, dayValues("collection", strconv.FormatInt(foreignCollection, 10)), []string{"x"}, http.StatusUnprocessableEntity},
		{"combined card quota", token, csrf, dayValues(), []string{"123", "456"}, http.StatusRequestEntityTooLarge},
		{"body limit", token, csrf, dayValues(), []string{strings.Repeat("x", 140<<10)}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := createDayWithFiles(t, s, tc.token, tc.csrf, false, tc.form, tc.files...)
			if rr.Code != tc.status {
				t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, owner.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed create left days: %d %v", count, err)
			}
		})
	}
	if _, err := db.Exec(`UPDATE users SET used_bytes = 5 WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if rr := createDayWithFiles(t, s, token, csrf, true, dayValues(), "123"); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("account quota: %d", rr.Code)
	}
	if _, err := db.Exec(`UPDATE users SET quota_bytes = 5 WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if rr := createDayWithFiles(t, s, token, csrf, false, dayValues()); rr.Code != http.StatusSeeOther {
		t.Fatalf("creating without files at full quota: %d %s", rr.Code, rr.Body.String())
	}
}

func TestDayCreateAttachmentFailureKeepsSavedDay(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.media = media.New(s.db, blocked)
	rr := createDayWithFiles(t, s, token, sessionCSRF(t, s, token), false, dayValues(), "hello")
	d := onlyDay(t, s, owner.ID)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "The day was saved") ||
		!strings.Contains(rr.Body.String(), `action="`+dayPath(d.ID)+`/edit"`) ||
		!strings.Contains(rr.Body.String(), `href="`+dayPath(d.ID)+`#attachments"`) {
		t.Fatalf("upload failure lost saved day or retry link: %d %s", rr.Code, rr.Body.String())
	}
	usage, err := s.media.Usage(context.Background(), owner.ID, d.ID)
	if err != nil || usage.UserUsed != 0 || usage.CardUsed != 0 {
		t.Fatalf("failed upload changed usage: %+v %v", usage, err)
	}
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
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != dayPath(day.ID)+"#attachments" {
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
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != dayPath(day.ID)+"#attachments" {
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

func TestVoiceRecordingIsAudioAndLargeVideoUploads(t *testing.T) {
	s, owner, token, day := attachmentServer(t)
	voice := append([]byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x84webm\x16\x54\xae\x6b\xae\x88\x86\x86A_OPUS"), make([]byte, 256)...)
	if rr := uploadAttachment(t, s, token, day.ID, "voice-20261018.webm", voice); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload voice: %d %s", rr.Code, rr.Body.String())
	}
	video := append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp41"), bytes.Repeat([]byte{0xab}, 3<<20)...)
	if rr := uploadAttachment(t, s, token, day.ID, "clip.mp4", video); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload video: %d %s", rr.Code, rr.Body.String())
	}
	movie := append([]byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  \x00\x00\x00\x08wide"), bytes.Repeat([]byte{0xcd}, 1<<20)...)
	if rr := uploadAttachment(t, s, token, day.ID, "IMG_0001.MOV", movie); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload mov: %d %s", rr.Code, rr.Body.String())
	}
	files, err := s.media.ListDay(context.Background(), owner, day.ID)
	if err != nil || len(files) != 3 {
		t.Fatalf("files: %v %v", files, err)
	}
	for _, f := range files {
		want := map[string]struct{ kind, mime string }{
			"voice-20261018.webm": {"audio", "audio/webm"},
			"clip.mp4":            {"video", "video/mp4"},
			"IMG_0001.MOV":        {"video", "video/quicktime"},
		}[f.Filename]
		if f.Kind != want.kind || f.MIME != want.mime {
			t.Errorf("%s stored as %s/%s, want %s/%s", f.Filename, f.Kind, f.MIME, want.kind, want.mime)
		}
		if f.Filename == "clip.mp4" && f.Size != int64(len(video)) {
			t.Errorf("video size = %d, want %d", f.Size, len(video))
		}
		if stat, err := os.Stat(s.media.Path(owner, f)); err != nil || stat.Size() != f.Size {
			t.Errorf("%s on disk: %v %v", f.Filename, stat, err)
		}
		served := attachmentRequest(s, http.MethodGet, "/attachments/"+strconv.FormatInt(f.ID, 10), token, "")
		if served.Header().Get("Content-Type") != want.mime || !strings.HasPrefix(served.Header().Get("Content-Disposition"), "inline") {
			t.Errorf("%s served as %v", f.Filename, served.Header())
		}
	}
	page := get(s.Handler(), dayPath(day.ID), token).Body.String()
	for _, want := range []string{`<audio controls preload="metadata" src="/attachments/`, `<video controls playsinline preload="metadata" src="/attachments/`} {
		if !strings.Contains(page, want) {
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
