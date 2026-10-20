package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
	"pandadays/internal/media"
)

func seedMedia(t *testing.T, s *Server, ownerID int64, title, filename, contents string) (days.Day, media.Attachment) {
	t.Helper()
	d, err := s.days.Create(context.Background(), ownerID, days.DayInput{Title: title, Date: "2026-10-18"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.media.Upload(context.Background(), ownerID, d.ID, filename, strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	return d, a
}

func TestMediaLibraryListsOnlyOwnMedia(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	other, otherToken := secondUser(t, s)
	seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")
	seedMedia(t, s, owner.ID, "Party", "cake.txt", "sugar")
	_, foreign := seedMedia(t, s, other.ID, "Secret", "private.txt", "hidden")

	if rr := get(s.Handler(), "/media", ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous library: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	for _, lang := range []string{"en", "zh"} {
		rr := get(s.Handler(), "/media?lang="+lang, token)
		body := rr.Body.String()
		if rr.Code != http.StatusOK {
			t.Fatalf("library %s: %d", lang, rr.Code)
		}
		for _, want := range []string{`href="/media"`, "beach.txt", "cake.txt", `class="media-card-preview" href="/media/`} {
			if !strings.Contains(body, want) {
				t.Errorf("library %s missing %q", lang, want)
			}
		}
		if strings.Contains(body, `action="/attachments/`) || strings.Contains(body, ">Trip</a>") || strings.Contains(body, ">Party</a>") {
			t.Fatal("library grid exposes detail-only controls")
		}
		if strings.Contains(body, "private.txt") || strings.Contains(body, "Secret") ||
			strings.Contains(body, "/attachments/"+strconv.FormatInt(foreign.ID, 10)+`"`) {
			t.Errorf("library %s exposes another user's media", lang)
		}
	}
	if body := get(s.Handler(), "/media", otherToken).Body.String(); strings.Contains(body, "beach.txt") || !strings.Contains(body, "private.txt") {
		t.Fatal("second user's library is not scoped to their own media")
	}
}

func TestMediaLibraryEmpty(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	rr := get(s.Handler(), "/media", token)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "No media yet") {
		t.Fatalf("empty library: %d %s", rr.Code, rr.Body.String())
	}
	if body := get(s.Handler(), "/days/new", token).Body.String(); !strings.Contains(body, "No media yet") {
		t.Fatal("picker does not explain an empty library")
	}
}

func TestRemoveDayAttachmentKeepsReusableLibraryMedia(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	other, otherToken := secondUser(t, s)
	ctx := context.Background()
	d, a := seedMedia(t, s, owner.ID, "Trip", "photo.png", "\x89PNG\r\n\x1a\n")
	target, _ := seedMedia(t, s, owner.ID, "Party", "cake.txt", "sugar")
	if err := s.media.SetBackground(ctx, owner.ID, d.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	res, err := s.db.Exec(`INSERT INTO shares (owner_id, target_type, target_id) VALUES (?, 'day', ?)`, owner.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	shareID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO share_recipients (share_id, recipient_id) VALUES (?, ?)`, shareID, other.ID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(a.ID, 10)
	download := "/attachments/" + id
	remove := dayPath(d.ID) + "/attachments/" + id + "/remove"
	if rr := get(s.Handler(), download, otherToken); rr.Code != http.StatusOK {
		t.Fatalf("shared attachment before removal: %d", rr.Code)
	}
	for _, tc := range []struct {
		path, token string
	}{
		{remove, otherToken},
		{dayPath(target.ID) + "/attachments/" + id + "/remove", token},
		{dayPath(d.ID) + "/attachments/999999/remove", token},
	} {
		if rr := sessionPost(t, s, tc.path, tc.token, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("unauthorized removal: %d", rr.Code)
		}
	}
	if rr := attachmentRequest(s, http.MethodPost, remove, token, "invalid"); rr.Code != http.StatusForbidden {
		t.Fatalf("removal without CSRF: %d", rr.Code)
	}
	before, err := s.media.Usage(ctx, owner.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rr := sessionPost(t, s, remove, token, nil); rr.Code != http.StatusSeeOther ||
		rr.Header().Get("Location") != dayPath(d.ID)+"#attachments" {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	after, err := s.media.Usage(ctx, owner.ID, d.ID)
	if err != nil || after.CardUsed != 0 || after.UserUsed != before.UserUsed {
		t.Fatalf("removal usage: before=%+v after=%+v err=%v", before, after, err)
	}
	files, err := s.media.ListDay(ctx, owner.ID, d.ID)
	if err != nil || len(files) != 0 {
		t.Fatalf("removed attachment still on day: %+v %v", files, err)
	}
	files, err = s.media.ListAccessibleDay(ctx, other.ID, d.ID)
	if err != nil || len(files) != 0 {
		t.Fatalf("removed attachment still shared: %+v %v", files, err)
	}
	updated, err := s.days.Get(ctx, owner.ID, d.ID)
	if err != nil || updated.StyleBgAttachmentID != 0 {
		t.Fatalf("background not cleared: %+v %v", updated, err)
	}
	if err := s.media.SetBackground(ctx, owner.ID, d.ID, a.ID); err != media.ErrNotFound {
		t.Fatalf("removed media can be set as background: %v", err)
	}
	if rr := get(s.Handler(), download, otherToken); rr.Code != http.StatusNotFound {
		t.Fatalf("removed media still downloadable by recipient: %d", rr.Code)
	}
	if rr := get(s.Handler(), download, token); rr.Code != http.StatusOK || rr.Body.String() != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("owner lost media contents: %d %q", rr.Code, rr.Body.String())
	}
	for _, path := range []string{"/media", "/media/" + id, "/days/new"} {
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "photo.png") {
			t.Fatalf("removed media missing from %s: %d", path, rr.Code)
		}
	}
	rr := postMediaFiles(t, s, dayPath(target.ID)+"/attachments", token, sessionCSRF(t, s, token), false,
		url.Values{"library": {id}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("reuse removed media: %d %s", rr.Code, rr.Body.String())
	}
	files, err = s.media.ListDay(ctx, owner.ID, target.ID)
	if err != nil || len(files) != 2 {
		t.Fatalf("reused attachments: %+v %v", files, err)
	}
	if rr := sessionPost(t, s, download+"/delete", token, url.Values{"return_to": {"/media"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("permanent library deletion: %d", rr.Code)
	}
	if _, err := os.Stat(s.media.Path(owner.ID, a)); !os.IsNotExist(err) {
		t.Fatalf("permanent deletion left original file: %v", err)
	}
	for _, file := range files {
		if _, err := os.Stat(s.media.Path(owner.ID, file)); err != nil {
			t.Fatalf("permanent deletion removed reused copy: %v", err)
		}
	}
}

func TestMediaDetailIsOwnerScoped(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	_, otherToken := secondUser(t, s)
	d, a := seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")
	path := "/media/" + strconv.FormatInt(a.ID, 10)
	for _, lang := range []string{"en", "zh"} {
		rr := get(s.Handler(), path+"?lang="+lang, token)
		body := rr.Body.String()
		for _, want := range []string{"beach.txt", `class="card media-detail"`, `href="` + dayPath(d.ID) + `"`, `name="return_to" value="/media"`} {
			if rr.Code != http.StatusOK || !strings.Contains(body, want) {
				t.Fatalf("detail missing %q: %d", want, rr.Code)
			}
			if strings.Contains(body, `<div class="page-heading">`) {
				t.Error("media detail retains the library page heading")
			}
			if !strings.Contains(body, `<h2>beach.txt</h2>`) {
				t.Error("media detail lost its filename")
			}
		}
	}
	for _, path := range []string{path, "/media/999999", "/media/invalid"} {
		if rr := get(s.Handler(), path, otherToken); rr.Code != http.StatusNotFound {
			t.Fatalf("foreign or invalid detail %s: %d", path, rr.Code)
		}
	}
	if rr := get(s.Handler(), path, ""); rr.Code != http.StatusSeeOther {
		t.Fatalf("anonymous detail: %d", rr.Code)
	}
}

func TestExistingDayTabbedMediaSelection(t *testing.T) {
	for _, headerCSRF := range []bool{false, true} {
		t.Run(strconv.FormatBool(headerCSRF), func(t *testing.T) {
			s, db, owner, token := authenticatedServer(t)
			other, _ := secondUser(t, s)
			d, a := seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")
			_, foreign := seedMedia(t, s, other.ID, "Secret", "private.txt", "hidden")
			path := dayPath(d.ID) + "/attachments"
			csrf := sessionCSRF(t, s, token)
			pick := url.Values{"library": {strconv.FormatInt(a.ID, 10)}}
			pick.Add("library", strconv.FormatInt(a.ID, 10))
			rr := postMediaFiles(t, s, path, token, csrf, headerCSRF, pick, "fresh", "voice")
			if rr.Code != http.StatusSeeOther {
				t.Fatalf("attach selections: %d %s", rr.Code, rr.Body.String())
			}
			files, err := s.media.ListDay(context.Background(), owner.ID, d.ID)
			if err != nil || len(files) != 4 {
				t.Fatalf("attachments: %+v %v", files, err)
			}
			for _, invalid := range []string{strconv.FormatInt(foreign.ID, 10), "invalid"} {
				rr = postMediaFiles(t, s, path, token, csrf, headerCSRF, url.Values{"library": {invalid}}, "not saved")
				if rr.Code != http.StatusNotFound {
					t.Fatalf("foreign library: %d", rr.Code)
				}
			}
			if _, err := db.Exec(`UPDATE users SET max_card_bytes = 20 WHERE id = ?`, owner.ID); err != nil {
				t.Fatal(err)
			}
			rr = postMediaFiles(t, s, path, token, csrf, headerCSRF, pick, "123", "456")
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("combined quota: %d", rr.Code)
			}
			files, err = s.media.ListDay(context.Background(), owner.ID, d.ID)
			if err != nil || len(files) != 4 {
				t.Fatalf("rejected selections changed attachments: %+v %v", files, err)
			}
			for _, page := range []string{"/days/new", dayPath(d.ID)} {
				body := get(s.Handler(), page, token).Body.String()
				for _, want := range []string{`id="media-tab-library"`, `id="media-tab-upload"`, `id="media-tab-voice"`, `data-record-file`} {
					if !strings.Contains(body, want) {
						t.Fatalf("%s missing %s", page, want)
					}
				}
			}
		})
	}
}
func TestMediaLibraryDeleteReturnsToLibrary(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	_, a := seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")
	_, b := seedMedia(t, s, owner.ID, "Party", "cake.txt", "sugar")
	path := "/attachments/" + strconv.FormatInt(a.ID, 10) + "/delete"
	rr := sessionPost(t, s, path, token, url.Values{"return_to": {"/media"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/media" {
		t.Fatalf("library delete: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := s.media.Get(context.Background(), owner.ID, a.ID); err != media.ErrNotFound {
		t.Fatalf("deleted media still present: %v", err)
	}
	// Arbitrary return targets are ignored to prevent open redirects.
	rr = sessionPost(t, s, "/attachments/"+strconv.FormatInt(b.ID, 10)+"/delete", token, url.Values{"return_to": {"https://evil.example"}})
	if loc := rr.Header().Get("Location"); rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/days/") {
		t.Fatalf("untrusted return_to: %d %s", rr.Code, loc)
	}
}

func TestDayCreateCopiesLibraryMedia(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	src, a := seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")

	page := get(s.Handler(), "/days/new", token).Body.String()
	if !strings.Contains(page, `name="library" value="`+strconv.FormatInt(a.ID, 10)+`"`) || !strings.Contains(page, "beach.txt") {
		t.Fatal("picker does not list library media")
	}

	id := strconv.FormatInt(a.ID, 10)
	values := dayValues("title", "Reunion", "library", id)
	values.Add("library", id) // duplicate selections attach the item once
	rr := createDayWithFiles(t, s, token, sessionCSRF(t, s, token), false, values, "fresh")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create with library: %d %s", rr.Code, rr.Body.String())
	}
	created := parseID(strings.TrimPrefix(rr.Header().Get("Location"), "/days/"))
	files, err := s.media.ListDay(context.Background(), owner.ID, created)
	if err != nil || len(files) != 2 {
		t.Fatalf("new day attachments: %+v %v", files, err)
	}
	var copied media.Attachment
	for _, f := range files {
		if f.Filename == "beach.txt" {
			copied = f
		}
	}
	if copied.ID == 0 || copied.ID == a.ID || copied.UUID == a.UUID || copied.DayID != created {
		t.Fatalf("library item was not copied: %+v", copied)
	}
	if data, err := os.ReadFile(s.media.Path(owner.ID, copied)); err != nil || string(data) != "sand" {
		t.Fatalf("copied contents: %q %v", data, err)
	}
	usage, err := s.media.Usage(context.Background(), owner.ID, created)
	if err != nil || usage.CardUsed != 9 || usage.UserUsed != 13 {
		t.Fatalf("usage after copy: %+v %v", usage, err)
	}
	if err := s.media.Delete(context.Background(), owner.ID, copied.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.media.Path(owner.ID, a)); err != nil {
		t.Fatalf("deleting the copy removed the original: %v", err)
	}
	if orig, err := s.media.ListDay(context.Background(), owner.ID, src.ID); err != nil || len(orig) != 1 {
		t.Fatalf("original day attachments changed: %+v %v", orig, err)
	}
}

func TestDayCreateLibraryAuthorizationAndQuota(t *testing.T) {
	s, db, owner, token := authenticatedServer(t)
	other, _ := secondUser(t, s)
	_, own := seedMedia(t, s, owner.ID, "Trip", "beach.txt", "sand")
	_, foreign := seedMedia(t, s, other.ID, "Secret", "private.txt", "hidden")
	csrf := sessionCSRF(t, s, token)
	countDays := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, owner.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countDays()
	for _, value := range []string{strconv.FormatInt(foreign.ID, 10), "999999", "abc"} {
		rr := createDayWithFiles(t, s, token, csrf, false, dayValues("library", value))
		if rr.Code != http.StatusNotFound {
			t.Errorf("library %q: %d", value, rr.Code)
		}
	}
	if countDays() != before {
		t.Fatal("rejected library selection created a day")
	}
	if n, err := s.media.ListOwner(context.Background(), other.ID); err != nil || len(n) != 1 {
		t.Fatalf("foreign media changed: %v %v", n, err)
	}

	if _, err := db.Exec(`UPDATE users SET max_card_bytes = 3 WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	ownID := strconv.FormatInt(own.ID, 10)
	rr := createDayWithFiles(t, s, token, csrf, false, dayValues("library", ownID))
	if rr.Code != http.StatusRequestEntityTooLarge || countDays() != before {
		t.Fatalf("library over card quota: %d", rr.Code)
	}
	// The selection survives the error so the user does not have to pick again.
	if !strings.Contains(rr.Body.String(), `name="library" value="`+ownID+`" checked`) {
		t.Fatal("library selection was not preserved after an error")
	}
	if _, err := db.Exec(`UPDATE users SET max_card_bytes = NULL WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if rr := createDayWithFiles(t, s, token, csrf, false, dayValues("title", "", "library", ownID)); rr.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rr.Body.String(), `name="library" value="`+ownID+`" checked`) {
		t.Fatalf("validation error lost library selection: %d", rr.Code)
	}
}
