package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/media"
)

func makeAdmin(t *testing.T, s *Server, id int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func temporaryPassword(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`<code id="generated-password">([^<]+)</code>`).FindStringSubmatch(body)
	if len(match) != 2 || len(match[1]) != 16 {
		t.Fatal("missing random 16-character password")
	}
	return match[1]
}

func TestAdminRoutesAuthorizationAndCSRF(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	for _, path := range []string{"/admin/users", "/admin/settings"} {
		if rr := get(s.Handler(), path, ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Fatalf("anonymous %s: %d", path, rr.Code)
		}
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
			t.Fatalf("non-admin %s: %d", path, rr.Code)
		}
	}
	paths := []string{"/admin/users", "/admin/settings"}
	for _, action := range []string{"reset-password", "activate", "deactivate", "delete", "quotas"} {
		paths = append(paths, "/admin/users/"+strconv.FormatInt(user.ID, 10)+"/"+action)
	}
	for _, path := range paths {
		if rr := sessionPost(t, s, path, token, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("non-admin POST %s: %d", path, rr.Code)
		}
	}
	if rr := get(s.Handler(), "/", token); strings.Contains(rr.Body.String(), `href="/admin/users"`) {
		t.Fatal("non-admin navigation contains admin links")
	}
	makeAdmin(t, s, user.ID)
	for _, path := range paths {
		rr := postForm(s.Handler(), path, url.Values{}, &http.Cookie{Name: auth.SessionCookieName, Value: token})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF POST %s: %d", path, rr.Code)
		}
	}
	for _, path := range []string{"/admin/users", "/admin/settings"} {
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusOK {
			t.Fatalf("admin GET %s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAdminUserLifecycle(t *testing.T) {
	ctx := context.Background()
	s, db, actor, token := authenticatedServer(t)
	makeAdmin(t, s, actor.ID)
	s.cfg.DataDir = t.TempDir()
	s.media = media.New(db, filepath.Join(s.cfg.DataDir, "media"))
	createForm := url.Values{"username": {"Bad_Name"}, "email": {"bad@example.com"}}
	rr := sessionPost(t, s, "/admin/users", token, createForm)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "Use 4–32 lowercase letters") {
		t.Fatalf("invalid username: %d %s", rr.Code, rr.Body.String())
	}
	rr = sessionPost(t, s, "/admin/users", token, url.Values{"username": {"bobby"}, "email": {"bobby@example.com"}})
	if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	password := temporaryPassword(t, rr.Body.String())
	u, err := s.auth.Authenticate(ctx, "ip", "bobby", password, time.Now())
	if err != nil || !u.MustChangePassword {
		t.Fatalf("created login: %+v %v", u, err)
	}
	rr = get(s.Handler(), "/admin/users", token)
	if strings.Contains(rr.Body.String(), password) {
		t.Fatal("temporary password persisted on GET")
	}
	rr = sessionPost(t, s, "/admin/users", token, url.Values{"username": {"bobby"}, "email": {"new@example.com"}})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate user: %d", rr.Code)
	}
	userToken, _, err := s.auth.CreateSession(ctx, u.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rr := get(s.Handler(), "/admin/users", userToken); rr.Header().Get("Location") != "/password" {
		t.Fatalf("created user not forced to change password: %d", rr.Code)
	}
	prefix := "/admin/users/" + strconv.FormatInt(u.ID, 10)
	rr = sessionPost(t, s, prefix+"/reset-password", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}
	reset := temporaryPassword(t, rr.Body.String())
	if reset == password {
		t.Fatal("reset reused password")
	}
	if !strings.Contains(rr.Body.String(), "bobby: <code") {
		t.Fatal("reset banner does not name the account")
	}
	if _, err := s.auth.LookupSession(ctx, userToken, time.Now()); !errors.Is(err, auth.ErrSession) {
		t.Fatalf("reset retained old session: %v", err)
	}
	if rr := get(s.Handler(), "/admin/users", token); strings.Contains(rr.Body.String(), reset) {
		t.Fatal("reset password persisted on GET")
	}
	rr = sessionPost(t, s, prefix+"/deactivate", token, nil)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("deactivate: %d", rr.Code)
	}
	if _, err := s.auth.Authenticate(ctx, "another-ip", "Bob", reset, time.Now()); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("inactive login: %v", err)
	}
	if rr := sessionPost(t, s, prefix+"/activate", token, nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rr.Code)
	}
	d, err := s.days.Create(ctx, u.ID, days.DayInput{Title: "Delete with user", Date: "2026-10-18"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.media.Upload(ctx, u.ID, d.ID, "file.txt", strings.NewReader("contents"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.shares.Create(ctx, u.ID, "day", d.ID, []string{actor.Username}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	self := "/admin/users/" + strconv.FormatInt(actor.ID, 10)
	for _, action := range []string{"delete", "deactivate"} {
		rr := sessionPost(t, s, self+"/"+action, token, url.Values{"confirm": {"1"}})
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("self %s: %d", action, rr.Code)
		}
	}
	if rr := sessionPost(t, s, prefix+"/delete", token, nil); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed delete: %d", rr.Code)
	}
	if _, err := s.days.Get(ctx, u.ID, d.ID); err != nil {
		t.Fatalf("unconfirmed delete changed data: %v", err)
	}
	rr = sessionPost(t, s, prefix+"/delete", token, url.Values{"confirm": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	for _, table := range []string{"users", "sessions", "collections", "tags", "days", "attachments", "shares"} {
		column := "owner_id"
		if table == "users" {
			column = "id"
		} else if table == "sessions" {
			column = "user_id"
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, u.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained rows=%d err=%v", table, count, err)
		}
	}
	path := filepath.Join(s.cfg.DataDir, "media", strconv.FormatInt(u.ID, 10), a.UUID)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user media retained: %v", err)
	}
	if rr := sessionPost(t, s, prefix+"/quotas", token, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("missing user quotas: %d", rr.Code)
	}
}

func siteSettingsForm() url.Values {
	return url.Values{
		"site_lang": {"en"}, "session_days": {"7"}, "default_quota_days": {"2"},
		"default_quota_bytes": {"10"}, "default_max_card_bytes": {"6"}, "backup_retention": {"3"},
	}
}

func TestAdminQuotaOverrideResolutionAndEnforcement(t *testing.T) {
	ctx := context.Background()
	s, db, actor, token := authenticatedServer(t)
	makeAdmin(t, s, actor.ID)
	s.cfg.DataDir = t.TempDir()
	s.media = media.New(db, filepath.Join(s.cfg.DataDir, "media"))
	target, _ := secondUser(t, s)
	if rr := sessionPost(t, s, "/admin/settings", token, siteSettingsForm()); rr.Code != http.StatusSeeOther {
		t.Fatalf("site settings: %d %s", rr.Code, rr.Body.String())
	}
	d, err := s.days.Create(ctx, target.ID, days.DayInput{Title: "Day", Date: "2026-10-18"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertLimits := func(dayLimit int, byteLimit, cardLimit int64) {
		t.Helper()
		q, err := s.days.QuotaDays(ctx, target.ID)
		if err != nil || q != dayLimit {
			t.Fatalf("day limit=%d err=%v want=%d", q, err, dayLimit)
		}
		usage, err := s.media.Usage(ctx, target.ID, d.ID)
		if err != nil || usage.UserLimit != byteLimit || usage.CardLimit != cardLimit {
			t.Fatalf("media limits=%+v err=%v", usage, err)
		}
	}
	assertLimits(2, 10, 6)
	prefix := "/admin/users/" + strconv.FormatInt(target.ID, 10) + "/quotas"
	for _, value := range []string{"-1", "1.5", "abc", "9223372036854775808"} {
		for _, key := range []string{"quota_days", "quota_bytes", "max_card_bytes"} {
			if rr := sessionPost(t, s, prefix, token, url.Values{key: {value}}); rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid %s=%s: %d", key, value, rr.Code)
			}
			assertLimits(2, 10, 6)
		}
	}
	override := url.Values{"quota_days": {"1"}, "quota_bytes": {"4"}, "max_card_bytes": {"3"}}
	if rr := sessionPost(t, s, prefix, token, override); rr.Code != http.StatusSeeOther {
		t.Fatalf("set overrides: %d", rr.Code)
	}
	assertLimits(1, 4, 3)
	if _, err := s.days.Create(ctx, target.ID, days.DayInput{Title: "Blocked", Date: "2026-10-19"}, time.Now()); !errors.Is(err, days.ErrQuotaDays) {
		t.Fatalf("day override not enforced: %v", err)
	}
	if _, err := s.media.Upload(ctx, target.ID, d.ID, "too-big", strings.NewReader("1234")); !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("card override not enforced: %v", err)
	}
	if _, err := s.media.Upload(ctx, target.ID, d.ID, "fits", strings.NewReader("123")); err != nil {
		t.Fatal(err)
	}
	override.Set("max_card_bytes", "10")
	if rr := sessionPost(t, s, prefix, token, override); rr.Code != http.StatusSeeOther {
		t.Fatalf("change card override: %d", rr.Code)
	}
	if _, err := s.media.Upload(ctx, target.ID, d.ID, "too-big", strings.NewReader("12")); !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("storage override not enforced: %v", err)
	}
	changed := siteSettingsForm()
	changed.Set("default_quota_days", "3")
	changed.Set("default_quota_bytes", "20")
	changed.Set("default_max_card_bytes", "12")
	if rr := sessionPost(t, s, "/admin/settings", token, changed); rr.Code != http.StatusSeeOther {
		t.Fatalf("change site defaults: %d", rr.Code)
	}
	assertLimits(1, 4, 10)
	// Zero is an actual override, not inheritance; lowering limits retains existing data.
	zero := url.Values{"quota_days": {"0"}, "quota_bytes": {"0"}, "max_card_bytes": {"0"}}
	if rr := sessionPost(t, s, prefix, token, zero); rr.Code != http.StatusSeeOther {
		t.Fatalf("zero overrides: %d", rr.Code)
	}
	assertLimits(0, 0, 0)
	if _, err := s.media.Upload(ctx, target.ID, d.ID, "blocked", strings.NewReader("1")); !errors.Is(err, media.ErrQuota) {
		t.Fatalf("zero quota not enforced: %v", err)
	}
	if rr := sessionPost(t, s, prefix, token, url.Values{}); rr.Code != http.StatusSeeOther {
		t.Fatalf("clear overrides: %d", rr.Code)
	}
	assertLimits(3, 20, 12)
	if err := s.days.SetArchived(ctx, target.ID, d.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	rr := get(s.Handler(), "/admin/users/"+strconv.FormatInt(target.ID, 10), token)
	for _, text := range []string{"1 of 3 days used (including archived)",
		formatMegabytes(3) + " M of " + formatMegabytes(20) + " M used",
		"Attachment limit per day: " + formatMegabytes(12) + " M"} {
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), text) {
			t.Errorf("usage page missing %q status=%d", text, rr.Code)
		}
	}
}

func TestAdminSettingsHTTPValidationAndLanguage(t *testing.T) {
	s, _, actor, token := authenticatedServer(t)
	makeAdmin(t, s, actor.ID)
	for _, key := range []string{"session_days", "default_quota_days", "default_quota_bytes", "default_max_card_bytes", "backup_retention"} {
		for _, value := range []string{"", "-1", "1.5", "overflow"} {
			v := siteSettingsForm()
			v.Set(key, value)
			if rr := sessionPost(t, s, "/admin/settings", token, v); rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid %s=%s: %d", key, value, rr.Code)
			}
		}
	}
	for _, v := range []url.Values{
		{"site_lang": {"fr"}, "session_days": {"7"}, "default_quota_days": {"2"}, "default_quota_bytes": {"10"}, "default_max_card_bytes": {"6"}, "backup_retention": {"3"}},
		{"site_lang": {"en"}, "session_days": {"0"}, "default_quota_days": {"2"}, "default_quota_bytes": {"10"}, "default_max_card_bytes": {"6"}, "backup_retention": {"3"}},
	} {
		if rr := sessionPost(t, s, "/admin/settings", token, v); rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid settings: %d", rr.Code)
		}
	}
	v := siteSettingsForm()
	v.Set("site_lang", "zh")
	if rr := sessionPost(t, s, "/admin/settings", token, v); rr.Code != http.StatusSeeOther {
		t.Fatalf("save settings: %d", rr.Code)
	}
	for _, path := range []string{"/admin/users", "/admin/settings"} {
		rr := get(s.Handler(), path, token)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `<html lang="zh">`) || strings.Contains(rr.Body.String(), "admin.") {
			t.Fatalf("localized admin page %s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	v.Set("site_lang", "")
	if rr := sessionPost(t, s, "/admin/settings", token, v); rr.Code != http.StatusSeeOther {
		t.Fatalf("clear site language: %d", rr.Code)
	}
	if rr := get(s.Handler(), "/admin/settings", token); !strings.Contains(rr.Body.String(), `<html lang="en">`) {
		t.Fatal("site language did not return to fallback")
	}
}
