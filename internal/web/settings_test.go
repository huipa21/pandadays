package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSettingsRequiresLoginAndShowsUsage(t *testing.T) {
	s, db, user, token := authenticatedServer(t)
	if rr := get(s.Handler(), "/settings", ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous settings: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}

	if _, err := db.Exec(`UPDATE users SET used_bytes = 512, quota_bytes = NULL, timezone = 'Asia/Shanghai' WHERE id = ?`, user.ID); err != nil {
		t.Fatal(err)
	}
	rr := get(s.Handler(), "/settings", token)
	body := rr.Body.String()
	for _, want := range []string{
		"User settings", `name="lang"`, `name="timezone"`,
		"512 B of 1.0 GiB used", `href="/password"`, `value="Asia/Shanghai"`,
	} {
		if rr.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("settings page status=%d missing %q", rr.Code, want)
		}
	}

	if _, err := db.Exec(`UPDATE users SET quota_bytes = 2048 WHERE id = ?`, user.ID); err != nil {
		t.Fatal(err)
	}
	rr = get(s.Handler(), "/settings", token)
	if !strings.Contains(rr.Body.String(), "512 B of 2.0 KiB used") {
		t.Fatalf("user quota override not shown: %s", rr.Body.String())
	}
}

func TestSaveSettings(t *testing.T) {
	s, db, user, token := authenticatedServer(t)
	csrf := sessionCSRF(t, s, token)
	cookie := &http.Cookie{Name: "panda_session", Value: token}
	form := url.Values{"csrf_token": {csrf}, "lang": {"zh"}, "timezone": {"Asia/Shanghai"}}
	rr := postForm(s.Handler(), "/settings", form, cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/settings" {
		t.Fatalf("save settings: status=%d location=%q body=%s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
	var lang, timezone string
	if err := db.QueryRow(`SELECT COALESCE(lang, ''), COALESCE(timezone, '') FROM users WHERE id = ?`, user.ID).Scan(&lang, &timezone); err != nil {
		t.Fatal(err)
	}
	if lang != "zh" || timezone != "Asia/Shanghai" {
		t.Fatalf("preferences = (%q, %q)", lang, timezone)
	}
	rr = get(s.Handler(), "/settings", token)
	if !strings.Contains(rr.Body.String(), `<html lang="zh">`) ||
		!strings.Contains(rr.Body.String(), `value="zh" lang="zh" selected`) ||
		!strings.Contains(rr.Body.String(), `value="Asia/Shanghai" selected`) {
		t.Fatalf("saved preferences not reflected: %s", rr.Body.String())
	}

	for _, tc := range []struct {
		name, lang, timezone, message string
	}{
		{"unsupported language", "fr", "Asia/Shanghai", "Choose a supported language."},
		{"invalid timezone", "en", "Mars/Olympus", "Choose a valid IANA timezone."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := url.Values{"csrf_token": {csrf}, "lang": {tc.lang}, "timezone": {tc.timezone}}
			rr := postForm(s.Handler(), "/settings?lang=en", invalid, cookie)
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), tc.message) {
				t.Fatalf("invalid preference: status=%d body=%s", rr.Code, rr.Body.String())
			}
			if err := db.QueryRow(`SELECT COALESCE(lang, ''), COALESCE(timezone, '') FROM users WHERE id = ?`, user.ID).Scan(&lang, &timezone); err != nil {
				t.Fatal(err)
			}
			if lang != "zh" || timezone != "Asia/Shanghai" {
				t.Fatalf("invalid preferences persisted: (%q, %q)", lang, timezone)
			}
		})
	}

	auto := url.Values{"csrf_token": {csrf}, "lang": {""}, "timezone": {""}}
	rr = postForm(s.Handler(), "/settings", auto, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("clear preferences: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := db.QueryRow(`SELECT COALESCE(lang, ''), COALESCE(timezone, '') FROM users WHERE id = ?`, user.ID).Scan(&lang, &timezone); err != nil {
		t.Fatal(err)
	}
	if lang != "" || timezone != "" {
		t.Fatalf("empty preferences should clear stored settings: (%q, %q)", lang, timezone)
	}
}
