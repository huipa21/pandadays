package web

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

func TestAdminSiteOnlyNavigationAndAccess(t *testing.T) {
	s, _, actor, token := authenticatedServer(t)
	day, err := s.days.Create(context.Background(), actor.ID, days.DayInput{
		Title: "Preserved personal data", Date: "2026-10-18",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	makeAdmin(t, s, actor.ID)
	if rr := get(s.Handler(), "/", token); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/admin/users" {
		t.Fatalf("admin landing: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	for _, path := range []string{"/archive", "/days/new", "/days/1", "/collections", "/collections/1",
		"/tags", "/tags/1", "/shared", "/messages", "/attachments/1", "/settings/export", "/settings/import"} {
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
			t.Errorf("admin GET %s: %d", path, rr.Code)
		}
	}
	for _, path := range []string{"/days", "/days/1/edit", "/days/1/archive", "/collections", "/tags",
		"/days/1/attachments", "/days/1/shares", "/collections/1/shares",
		"/shares/1/revoke", "/shared/1/hide", "/messages/1/accept", "/messages/1/reject", "/settings/import", "/settings/import/confirm"} {
		if rr := sessionPost(t, s, path, token, nil); rr.Code != http.StatusNotFound {
			t.Errorf("admin POST %s: %d", path, rr.Code)
		}
	}
	if _, err := s.days.Get(context.Background(), actor.ID, day.ID); err != nil {
		t.Fatalf("role change removed existing data: %v", err)
	}
	for _, lang := range []string{"en", "zh"} {
		body := get(s.Handler(), "/admin/users?lang="+lang, token).Body.String()
		for _, link := range []string{"/archive", "/shared", "/collections", "/tags"} {
			if strings.Contains(body, `href="`+link+`"`) {
				t.Errorf("admin navigation exposes %s", link)
			}
		}
		for _, want := range []string{`href="/admin/users"`, `href="/admin/backup"`, `href="/admin/settings"`,
			`class="avatar"`, actor.Username, `href="/settings"`, `action="/logout"`} {
			if !strings.Contains(body, want) {
				t.Errorf("admin navigation missing %s", want)
			}
		}
		body = get(s.Handler(), "/settings?lang="+lang, token).Body.String()
		if !strings.Contains(body, `href="/password"`) || strings.Contains(body, `action="/settings/import"`) ||
			strings.Contains(body, `href="/settings/export"`) || strings.Contains(body, "<progress") {
			t.Error("admin profile missing password change or exposes personal storage")
		}
	}
	_, regularToken := secondUser(t, s)
	for _, path := range []string{"/", "/collections", "/tags", "/archive", "/shared", "/settings"} {
		if rr := get(s.Handler(), path, regularToken); rr.Code != http.StatusOK {
			t.Errorf("regular user %s: %d", path, rr.Code)
		}
	}
}

func TestAdminUsersListSearchAndSeparateForms(t *testing.T) {
	s, _, actor, token := authenticatedServer(t)
	target, regularToken := secondUser(t, s)
	makeAdmin(t, s, actor.ID)
	targetPath := "/admin/users/" + strconv.FormatInt(target.ID, 10)
	for _, path := range []string{"/admin/users/new", targetPath} {
		if rr := get(s.Handler(), path, ""); rr.Header().Get("Location") != "/login" {
			t.Errorf("anonymous %s not redirected", path)
		}
		if rr := get(s.Handler(), path, regularToken); rr.Code != http.StatusNotFound {
			t.Errorf("regular user %s: %d", path, rr.Code)
		}
	}
	body := get(s.Handler(), "/admin/users", token).Body.String()
	for _, want := range []string{`type="search"`, `name="q"`, `href="/admin/users/new"`, `href="` + targetPath + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %s", want)
		}
	}
	for _, field := range []string{`name="username"`, `name="quota_days"`} {
		if strings.Contains(body, field) {
			t.Errorf("list still includes management form %s", field)
		}
	}
	for _, query := range []string{strings.ToUpper(target.Username), strings.ToUpper(target.Email)} {
		body := get(s.Handler(), "/admin/users?q="+url.QueryEscape(query), token).Body.String()
		if !strings.Contains(body, `href="`+targetPath+`"`) ||
			strings.Contains(body, `href="/admin/users/`+strconv.FormatInt(actor.ID, 10)+`"`) {
			t.Errorf("search %q did not filter users", query)
		}
	}
	body = get(s.Handler(), "/admin/users?q=nobody-matches", token).Body.String()
	if !strings.Contains(body, "No users found.") {
		t.Fatal("missing empty search state")
	}
	body = get(s.Handler(), "/admin/users/new", token).Body.String()
	if !strings.Contains(body, `name="username"`) || !strings.Contains(body, `name="email"`) ||
		strings.Contains(body, `name="quota_days"`) {
		t.Fatal("create form not separated")
	}
	body = get(s.Handler(), targetPath, token).Body.String()
	for _, want := range []string{`name="quota_mb"`, `name="max_card_mb"`, `/reset-password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("management page missing %s", want)
		}
	}
	for _, path := range []string{"/admin/users/0", "/admin/users/invalid", "/admin/users/999999"} {
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
			t.Errorf("invalid detail %s: %d", path, rr.Code)
		}
	}
}

func TestStorageQuotaMegabytesExactConversion(t *testing.T) {
	for value, want := range map[string]int64{"1.5": 3 * megabyte / 2, "0010": 10 * megabyte, "010.5": 21 * megabyte / 2} {
		got, err := parseStorageQuota(url.Values{"quota_mb": {value}}, "quota_bytes")
		if err != nil || !got.Valid || got.Int64 != want {
			t.Errorf("decimal %s -> %+v %v; want %d", value, got, err, want)
		}
	}
	for _, n := range []int64{0, 1, 3, megabyte / 2, megabyte, 1024 * megabyte, 1<<53 + 1, math.MaxInt64} {
		value := formatMegabytes(n)
		got, err := parseStorageQuota(url.Values{"quota_mb": {value}}, "quota_bytes")
		if err != nil || !got.Valid || got.Int64 != n {
			t.Errorf("round trip %d -> %s -> %+v: %v", n, value, got, err)
		}
	}
	for _, value := range []string{"-1", "NaN", "Inf", "1/2", "1e6", ".", "1.2.3", "0.0000001", "8796093022208", strings.Repeat("0", 65)} {
		if _, err := parseStorageQuota(url.Values{"quota_mb": {value}}, "quota_bytes"); err == nil {
			t.Errorf("accepted invalid quota %q", value)
		}
	}
	for _, value := range []string{"", "  "} {
		got, err := parseStorageQuota(url.Values{"quota_mb": {value}, "quota_bytes": {"999"}}, "quota_bytes")
		if err != nil || got.Valid {
			t.Errorf("blank M should inherit rather than use legacy bytes: %+v %v", got, err)
		}
	}
	s, _, actor, token := authenticatedServer(t)
	makeAdmin(t, s, actor.ID)
	s.cfg.DataDir = t.TempDir()
	target, _ := secondUser(t, s)
	form := siteSettingsForm()
	form.Del("default_quota_bytes")
	form.Del("default_max_card_bytes")
	form.Set("default_quota_mb", "1024")
	form.Set("default_max_card_mb", "0.5")
	if rr := sessionPost(t, s, "/admin/settings", token, form); rr.Code != http.StatusSeeOther {
		t.Fatalf("save M defaults: %d %s", rr.Code, rr.Body.String())
	}
	settings, err := s.auth.SiteSettings(context.Background())
	if err != nil || settings.QuotaBytes != 1024*megabyte || settings.MaxCardBytes != megabyte/2 {
		t.Fatalf("stored byte defaults: %+v %v", settings, err)
	}
	body := get(s.Handler(), "/admin/settings", token).Body.String()
	for _, want := range []string{`name="default_quota_mb"`, `value="1024"`, `name="default_max_card_mb"`, `value="0.5"`, "Storage quota (M)"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %s", want)
		}
	}
	prefix := "/admin/users/" + strconv.FormatInt(target.ID, 10) + "/quotas"
	if rr := sessionPost(t, s, prefix, token, url.Values{"quota_mb": {"1.5"}, "max_card_mb": {"0"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("save M overrides: %d %s", rr.Code, rr.Body.String())
	}
	var quota, card sql.NullInt64
	if err := s.db.QueryRow(`SELECT quota_bytes, max_card_bytes FROM users WHERE id=?`, target.ID).Scan(&quota, &card); err != nil ||
		!quota.Valid || quota.Int64 != 3*megabyte/2 || !card.Valid || card.Int64 != 0 {
		t.Fatalf("M overrides: %+v %+v %v", quota, card, err)
	}
	form.Set("default_quota_mb", "0.0000001")
	if rr := sessionPost(t, s, "/admin/settings", token, form); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fractional byte default accepted: %d", rr.Code)
	}
}

func TestAdminHeaderTimesDaysUsedAndDeleteDialog(t *testing.T) {
	s, _, actor, token := authenticatedServer(t)
	makeAdmin(t, s, actor.ID)
	target, regularToken := secondUser(t, s)
	if _, err := s.days.Create(context.Background(), target.ID, days.DayInput{Title: "Kept", Date: "2026-10-18"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET timezone = 'Asia/Shanghai', comment = 'Secret note' WHERE id IN (?, ?)`, actor.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/users", "/"} {
		tok := token
		if path == "/" {
			tok = regularToken
		}
		body := get(s.Handler(), path, tok).Body.String()
		toggle, nav := strings.Index(body, `id="theme-toggle"`), strings.Index(body, `<nav class="nav"`)
		if toggle < 0 || nav < 0 || toggle > nav {
			t.Errorf("%s: header toggles not before navigation", path)
		}
		if strings.Contains(body, "中/EN") || !strings.Contains(body, `language-toggle" type="submit" name="lang" value="zh" aria-label="Switch to Chinese" title="Switch to Chinese"><svg class="icon"`) {
			t.Errorf("%s: language toggle is not an icon", path)
		}
	}

	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	created := target.CreatedAt.In(shanghai)
	body := get(s.Handler(), "/admin/users", token).Body.String()
	want := `<time datetime="` + created.Format(time.RFC3339) + `">` + created.Format("2006-01-02 15:04 MST") + `</time>`
	if !strings.Contains(body, want) || strings.Contains(body, " UTC</time>") {
		t.Errorf("list time not in admin timezone; want %s", want)
	}
	if !strings.Contains(body, "1 days used") || !strings.Contains(body, "0 days used") {
		t.Error("list missing days used")
	}
	if strings.Contains(body, "Secret note") || strings.Contains(body, "bob@example.com") {
		t.Error("list embeds private details")
	}
	if _, err := s.db.Exec(`UPDATE users SET timezone = '' WHERE id = ?`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if body := get(s.Handler(), "/admin/users", token).Body.String(); !strings.Contains(body, "data-local-time>") {
		t.Error("times not marked for browser timezone")
	}

	path := "/admin/users/" + strconv.FormatInt(target.ID, 10) + "/delete"
	r := sessionRequest("GET", path, token)
	r.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	frag := rr.Body.String()
	if rr.Code != http.StatusOK || strings.Contains(frag, "<html") {
		t.Fatalf("dialog fragment: %d", rr.Code)
	}
	for _, want := range []string{`id="dialog-title"`, "Bob", "bob@example.com", "Secret note", "Asia/Shanghai",
		"1 days used", `action="` + path + `"`, `name="confirm" value="1"`, `name="csrf_token"`, "data-close-dialog", "data-local-time"} {
		if !strings.Contains(frag, want) {
			t.Errorf("dialog missing %s", want)
		}
	}
	if page := get(s.Handler(), path, token); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<html") ||
		!strings.Contains(page.Body.String(), "bob@example.com") {
		t.Errorf("no-JS delete page: %d", page.Code)
	}
	for _, tc := range []struct{ path, token string }{
		{"/admin/users/" + strconv.FormatInt(actor.ID, 10) + "/delete", token},
		{"/admin/users/999999/delete", token},
		{path, regularToken},
	} {
		if rr := get(s.Handler(), tc.path, tc.token); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", tc.path, rr.Code)
		}
	}
	if rr := sessionPost(t, s, path, token, url.Values{"confirm": {"1"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("confirmed delete: %d", rr.Code)
	}
	if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
		t.Errorf("deleted user dialog: %d", rr.Code)
	}
}
