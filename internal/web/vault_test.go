package web

import (
	"bytes"
	"context"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/share"
)

func unlockVault(t *testing.T, s *Server, token string) {
	t.Helper()
	rr := sessionPost(t, s, "/private/unlock", token, url.Values{"password": {"password123"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/private/" {
		t.Fatalf("unlock: %d %s %s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
}

func TestVaultPasswordGateAndExplicitLock(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	for _, path := range []string{"/private/", "/private/days/new", "/private/archive", "/private/media", "/private/settings/export"} {
		rr := get(s.Handler(), path, token)
		if rr.Code != 303 || rr.Header().Get("Location") != "/private/unlock" {
			t.Fatalf("locked %s: %d %s", path, rr.Code, rr.Header().Get("Location"))
		}
	}
	if rr := get(s.Handler(), "/private/", ""); rr.Code != 303 || rr.Header().Get("Location") != "/login" {
		t.Fatal("anonymous Vault access")
	}
	if rr := sessionPost(t, s, "/private/unlock", token, url.Values{"password": {"wrong"}}); rr.Code != 422 {
		t.Fatalf("wrong password: %d", rr.Code)
	}
	later := time.Now().Add(2 * time.Second)
	s.now = func() time.Time { return later }
	rr := postForm(s.Handler(), "/private/unlock", url.Values{"password": {"password123"}},
		&http.Cookie{Name: auth.SessionCookieName, Value: token})
	if rr.Code != 403 {
		t.Fatalf("CSRF bypass: %d", rr.Code)
	}
	unlockVault(t, s, token)
	for _, path := range []string{"/private/", "/private/days/new", "/private/tags", "/private/collections", "/private/archive", "/private/media", "/private/settings"} {
		if rr := get(s.Handler(), path, token); rr.Code != 200 {
			t.Fatalf("unlocked %s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	_, anotherToken := secondUser(t, s)
	if rr := get(s.Handler(), "/private/", anotherToken); rr.Code != 303 {
		t.Fatal("unlock crossed sessions")
	}
	secondToken, _, err := s.auth.CreateSession(context.Background(), u.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rr := get(s.Handler(), "/private/", secondToken); rr.Code != 303 {
		t.Fatal("unlock crossed sessions for same owner")
	}
	// Asset and avatar requests must not lock the current Vault.
	get(s.Handler(), "/static/app.css", token)
	get(s.Handler(), "/users/"+strconv.FormatInt(u.ID, 10)+"/avatar", token)
	if rr := get(s.Handler(), "/private/", token); rr.Code != 200 {
		t.Fatal("subresource request locked Vault")
	}
	get(s.Handler(), "/", token)
	rr = htmxGet(s, "/private/?when=past", token, nil)
	if rr.Code != 200 || rr.Header().Get("HX-Redirect") != "" {
		t.Fatal("normal navigation locked Vault")
	}
	if rr := sessionPost(t, s, "/vault/lock", token, nil); rr.Code != 303 {
		t.Fatalf("lock: %d", rr.Code)
	}
	rr = htmxGet(s, "/private/?when=past", token, nil)
	if rr.Header().Get("HX-Redirect") != "/private/unlock" {
		t.Fatal("toggle off did not lock htmx access")
	}
	unlockVault(t, s, token)
	get(s.Handler(), "/private/unlock", token)
	if rr := get(s.Handler(), "/private/", token); rr.Code != 303 {
		t.Fatal("new Vault entry did not require password")
	}
}

func TestPrivateDayVisibilitySearchAndControls(t *testing.T) {
	s, owner, token := homeServer(t)
	ctx := context.Background()
	col, err := s.collections.Create(ctx, owner, days.GroupInput{Name: "Trips"}, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.tags.Create(ctx, owner, days.GroupInput{Name: "Travel"}, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	normal := mustDay(t, s, owner, days.DayInput{Title: "Normal", Date: "2027-01-01", CollectionID: col.ID, TagIDs: []int64{tag.ID}})
	secret := mustDay(t, s, owner, days.DayInput{Title: "HiddenTitle", Comment: "HiddenComment", Date: "2027-02-01",
		Private: true, CollectionID: col.ID, TagIDs: []int64{tag.ID}})
	secretPath := dayPath(secret.ID)
	for _, path := range []string{"/", "/?q=HiddenTitle", "/?q=HiddenComment", "/collections/" + strconv.FormatInt(col.ID, 10), "/tags/" + strconv.FormatInt(tag.ID, 10)} {
		rr := get(s.Handler(), path, token)
		if rr.Code != 200 || strings.Contains(strings.Join(listedTitles(rr.Body.String()), ","), "HiddenTitle") {
			t.Fatalf("private day leaked to %s: %d", path, rr.Code)
		}
	}
	for _, path := range []string{secretPath, secretPath + "/edit"} {
		if rr := get(s.Handler(), path, token); rr.Code != 404 {
			t.Fatalf("direct private route %s: %d", path, rr.Code)
		}
	}
	for _, suffix := range []string{"/edit", "/archive", "/unarchive", "/pause", "/play", "/delete"} {
		if rr := sessionPost(t, s, secretPath+suffix, token, dayValues()); rr.Code != 404 {
			t.Fatalf("private mutation %s: %d", suffix, rr.Code)
		}
	}
	unlockVault(t, s, token)
	for _, path := range []string{"/private/", "/private/?q=HiddenTitle", "/private/?q=HiddenComment", "/private/collections/" + strconv.FormatInt(col.ID, 10), "/private/tags/" + strconv.FormatInt(tag.ID, 10)} {
		rr := get(s.Handler(), path, token)
		body := rr.Body.String()
		if rr.Code != 200 || !strings.Contains(body, "HiddenTitle") {
			t.Fatalf("Vault list %s: %d %s", path, rr.Code, body)
		}
	}
	rr := get(s.Handler(), "/private"+secretPath, token)
	for _, want := range []string{`href="/private` + secretPath + `/edit"`, `href="/private` + secretPath + `?display=days"`,
		`action="/private` + secretPath + `/archive"`, `action="/private` + secretPath + `/attachments"`, `hx-history="false"`,
		`action="/"`, `href="/private/collections/`, `href="/private/tags/`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("missing private control %s", want)
		}
	}
	if strings.Contains(rr.Body.String(), `id="day-sharing"`) {
		t.Fatal("private day offers sharing")
	}
	if rr := get(s.Handler(), "/private"+dayPath(normal.ID), token); rr.Code != 404 {
		t.Fatal("normal day appeared in Vault")
	}
	rr = htmxGet(s, "/private/?when=future", token, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `href="/private`+secretPath+`"`) || strings.Contains(rr.Body.String(), "<html") {
		t.Fatal("private htmx list differs from homepage behavior")
	}
	rr = sessionPost(t, s, "/private"+secretPath+"/archive", token, nil)
	if rr.Code != 303 || rr.Header().Get("Location") != "/private"+secretPath {
		t.Fatal("private archive redirect")
	}
	if rr := get(s.Handler(), "/private/archive", token); !strings.Contains(rr.Body.String(), "HiddenTitle") {
		t.Fatal("private archive missing day")
	}
	for _, path := range []string{"/private" + secretPath + "/shares", "/private/collections/" + strconv.FormatInt(col.ID, 10) + "/shares"} {
		if rr := sessionPost(t, s, path, token, url.Values{"recipients": {"bob1"}}); rr.Code != 404 {
			t.Fatalf("private sharing allowed: %d", rr.Code)
		}
	}
	sessionPost(t, s, "/vault/lock", token, nil)
	if rr := get(s.Handler(), "/archive", token); strings.Contains(rr.Body.String(), "HiddenTitle") {
		t.Fatal("private archive leaked")
	}
}

func TestDayPrivacyCheckboxAndTransitions(t *testing.T) {
	s, _, owner, token := authenticatedServer(t)
	form := get(s.Handler(), "/days/new", token).Body.String()
	if !strings.Contains(form, `name="private" value="1">`) || strings.Contains(form, `name="private" value="1" checked`) {
		t.Fatal("new normal days must default to public")
	}
	rr := sessionPost(t, s, "/days", token, dayValues("private", "1"))
	if rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatalf("create private: %d %s", rr.Code, rr.Body.String())
	}
	home := get(s.Handler(), "/", token)
	if home.Code != 200 || strings.Contains(strings.Join(listedTitles(home.Body.String()), ","), "Trip") ||
		!strings.Contains(home.Body.String(), `id="vault-dialog"`) {
		t.Fatal("private creation must return to the locked homepage with its privacy dialog")
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM days WHERE owner_id = ? AND is_private = 1`, owner.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	privateURL := "/private" + dayPath(id)
	if rr := get(s.Handler(), privateURL, token); rr.Code != 303 || rr.Header().Get("Location") != "/private/unlock" {
		t.Fatal("creation bypassed password gate")
	}
	unlockVault(t, s, token)
	edit := get(s.Handler(), privateURL+"/edit", token)
	if edit.Code != 200 || !strings.Contains(edit.Body.String(), `name="private" value="1" checked`) {
		t.Fatal("edit lost privacy checkbox")
	}
	rr = sessionPost(t, s, privateURL+"/edit", token, dayValues())
	if rr.Code != 303 || strings.HasPrefix(rr.Header().Get("Location"), "/private") {
		t.Fatal("making public did not leave Vault")
	}
	d := onlyDay(t, s, owner.ID)
	if d.Private {
		t.Fatal("unchecked privacy was not saved")
	}
	rr = sessionPost(t, s, dayPath(d.ID)+"/edit", token, dayValues("private", "1"))
	if rr.Code != 303 || rr.Header().Get("Location") != privateURL {
		t.Fatal("making private did not move into Vault")
	}
	if rr := get(s.Handler(), privateURL, token); rr.Code != 200 {
		t.Fatal("normal mutation unexpectedly locked Vault")
	}
}

func TestVaultToggleIncludesPrivateAcrossViews(t *testing.T) {
	s, owner, token := homeServer(t)
	var logs strings.Builder
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	normal := mustDay(t, s, owner, days.DayInput{Title: "Normal", Date: "2027-01-01"})
	secret := mustDay(t, s, owner, days.DayInput{Title: "Secret", Comment: "confidential", Date: "2027-02-01", Private: true})
	other, _ := secondUser(t, s)
	shared := mustDay(t, s, other.ID, days.DayInput{Title: "Shared day", Date: "2027-02-02"})
	if _, err := s.shares.Create(context.Background(), other.ID, share.TargetDay, shared.ID, []string{"alice"}, false, homeNow); err != nil {
		t.Fatal(err)
	}
	acceptAllInvitations(t, s, token, owner)
	archived := mustDay(t, s, owner, days.DayInput{Title: "Archived secret", Date: "2027-03-01", Private: true})
	if err := s.days.SetArchived(days.WithPrivate(context.Background(), true), owner, archived.ID, true, homeNow); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/", "/?q=Secret", "/?q=confidential",
		"/collections/" + strconv.FormatInt(normal.CollectionID, 10),
		"/tags/" + strconv.FormatInt(normal.TagIDs[0], 10), "/archive"}
	for _, path := range paths {
		if body := get(s.Handler(), path, token).Body.String(); strings.Contains(body, `day-private`) {
			t.Fatalf("private row visible while locked: %s", path)
		}
	}
	body := get(s.Handler(), "/", token).Body.String()
	search, vault, theme := strings.Index(body, "header-search-menu"), strings.Index(body, `id="vault-toggle"`), strings.Index(body, `id="theme-toggle"`)
	if search < 0 || vault <= search || theme <= vault || !strings.Contains(body, `id="vault-dialog"`) ||
		strings.Contains(body, `<a href="/private/unlock">`) {
		t.Fatal("Vault toggle placement or dialog incorrect")
	}
	rr := sessionPost(t, s, "/vault/unlock", token, url.Values{"password": {"password123"}})
	if rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatalf("toggle unlock: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	for _, path := range paths {
		for _, partial := range []bool{false, true} {
			rr := get(s.Handler(), path, token)
			if partial {
				rr = htmxGet(s, path, token, nil)
			}
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), "day-private") {
				t.Fatalf("private row missing: %s partial=%v: %d", path, partial, rr.Code)
			}
		}
	}
	body = get(s.Handler(), "/", token).Body.String()
	for _, want := range []string{`href="` + dayPath(normal.ID) + `"`, `href="/private` + dayPath(secret.ID) + `"`,
		`badge-private`, `day-shared`, `Shared day`, `aria-pressed="true"`, `hx-history="false"`} {
		if !strings.Contains(body, want) {
			t.Errorf("unlocked home missing %s", want)
		}
	}
	for _, path := range []string{"/collections", "/tags"} {
		body := get(s.Handler(), path, token).Body.String()
		if !strings.Contains(body, ">3 days<") {
			t.Fatalf("unlocked group counts missing all days: %s", path)
		}
	}
	rr = sessionPost(t, s, "/vault/lock", token, url.Values{"return_to": {"/?when=past"}})
	if rr.Code != 303 || rr.Header().Get("Location") != "/?when=past" {
		t.Fatal("toggle off lost current view")
	}
	for _, path := range paths {
		if body := htmxGet(s, path, token, nil).Body.String(); strings.Contains(body, "day-private") {
			t.Fatalf("private row visible after toggle off: %s", path)
		}
	}
	if rr := get(s.Handler(), "/private"+dayPath(secret.ID), token); rr.Code != 303 {
		t.Fatal("toggle off did not revoke direct private access")
	}
}

func TestPrivateDayCreateHTMXReturnsHome(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	for _, base := range []string{"", "/private"} {
		if base != "" {
			unlockVault(t, s, token)
		}
		r := httptest.NewRequest(http.MethodPost, base+"/days", strings.NewReader(dayValues("private", "1").Encode()))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("HX-Request", "true")
		r.Header.Set("X-CSRF-Token", sessionCSRF(t, s, token))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != http.StatusOK || rr.Header().Get("HX-Redirect") != "/" {
			t.Fatalf("htmx create %s: %d %v", base, rr.Code, rr.Header())
		}
	}
}

func TestVaultDialogJSONConfirmationAndLockCSRF(t *testing.T) {
	s, _, token := homeServer(t)
	request := func(password, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/vault/unlock", strings.NewReader(url.Values{"password": {password}}.Encode()))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		return rr
	}
	csrf := sessionCSRF(t, s, token)
	if rr := request("password123", "invalid"); rr.Code != 403 {
		t.Fatalf("unlock CSRF bypass: %d", rr.Code)
	}
	rr := request("wrong", csrf)
	if rr.Code != 422 || !strings.Contains(rr.Body.String(), `"error":`) || strings.Contains(rr.Body.String(), "wrong") {
		t.Fatalf("dialog password error: %d %s", rr.Code, rr.Body.String())
	}
	later := homeNow.Add(2 * time.Second)
	s.now = func() time.Time { return later }
	rr = request("password123", csrf)
	if rr.Code != 200 || rr.Body.String() != "{}" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("JSON unlock: %d %s", rr.Code, rr.Body.String())
	}
	rr = postForm(s.Handler(), "/vault/lock", nil, &http.Cookie{Name: auth.SessionCookieName, Value: token})
	if rr.Code != 403 {
		t.Fatalf("lock CSRF bypass: %d", rr.Code)
	}
	if body := get(s.Handler(), "/", token).Body.String(); !strings.Contains(body, `aria-pressed="true"`) {
		t.Fatal("rejected lock changed session state")
	}
	for _, target := range []string{"//example.com", "/\\example.com", "/private/days/1", "https://example.com"} {
		rr := sessionPost(t, s, "/vault/lock", token, url.Values{"return_to": {target}})
		if rr.Code != 303 || rr.Header().Get("Location") != "/" {
			t.Fatalf("unsafe lock redirect %q: %d %s", target, rr.Code, rr.Header().Get("Location"))
		}
	}
}

func TestVaultUnlockRateLimitAndAdminRestriction(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	for i := 0; i < 5; i++ {
		sessionPost(t, s, "/private/unlock", token, url.Values{"password": {"incorrect"}})
	}
	rr := sessionPost(t, s, "/private/unlock", token, url.Values{"password": {"password123"}})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("password reentry not rate limited: %d", rr.Code)
	}
	if _, err := db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	r := sessionRequest("GET", "/private/", token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("admin entered Vault: %d", w.Code)
	}
}

func TestPrivateCreateWithNativeMultipartAndAttachmentIsolation(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	for i, base := range []string{"", "/private"} {
		if base != "" {
			unlockVault(t, s, token)
		}
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for key, value := range map[string]string{
			"title": "Private upload " + strconv.Itoa(i), "date": "2027-01-01", "private": "1",
			"csrf_token": sessionCSRF(t, s, token),
		} {
			if err := form.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		file, err := form.CreateFormFile("file", "private.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("Private attachment " + strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", base+"/days", &body)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.Header.Set("Content-Type", form.FormDataContentType())
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 303 || w.Header().Get("Location") != "/" {
			t.Fatalf("create multipart %s: %d %s", base, w.Code, w.Body.String())
		}
		home := get(s.Handler(), "/", token)
		if home.Code != 200 || strings.Contains(home.Body.String(), `id="vault-toggle" aria-pressed="true"`) != (base != "") {
			t.Fatal("private creation changed the Vault visibility state")
		}
	}
	private := days.WithPrivate(context.Background(), true)
	list, err := s.media.ListOwner(private, u.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("private media: %+v %v", list, err)
	}
	unlockVault(t, s, token)
	for _, a := range list {
		id := strconv.FormatInt(a.ID, 10)
		if rr := get(s.Handler(), "/private/attachments/"+id, token); rr.Code != 200 {
			t.Fatalf("private file: %d", rr.Code)
		}
		if rr := get(s.Handler(), "/attachments/"+id, token); rr.Code != 404 {
			t.Fatalf("normal file leaked: %d", rr.Code)
		}
	}
	settings := get(s.Handler(), "/private/settings", token)
	for _, want := range []string{`href="/private/settings/export"`, `action="/private/settings/import"`} {
		if settings.Code != 200 || !strings.Contains(settings.Body.String(), want) {
			t.Fatalf("Vault settings missing %s", want)
		}
	}
	for _, a := range list {
		id := strconv.FormatInt(a.ID, 10)
		if rr := sessionPost(t, s, "/attachments/"+id+"/rename", token, url.Values{"filename": {"changed.txt"}}); rr.Code != 404 {
			t.Fatalf("normal rename: %d", rr.Code)
		}
		if rr := sessionPost(t, s, "/attachments/"+id+"/delete", token, nil); rr.Code != 404 {
			t.Fatalf("normal delete: %d", rr.Code)
		}
	}
}
