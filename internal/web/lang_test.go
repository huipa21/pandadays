package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pandadays/internal/db/dbtest"
)

func getPage(t *testing.T, s *Server, path, acceptLang string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s: status = %d", path, rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Vary"), "Accept-Language") {
		t.Errorf("%s: missing Vary: Accept-Language", path)
	}
	return rr.Body.String()
}

const (
	enTagline = "Your days, kept safe."
	zhTagline = "珍藏你的每一天。"
)

func assertLang(t *testing.T, body, lang string) {
	t.Helper()
	want, other := enTagline, zhTagline
	if lang == "zh" {
		want, other = zhTagline, enTagline
	}
	if !strings.Contains(body, `<html lang="`+lang+`">`) {
		t.Errorf("want html lang=%q", lang)
	}
	if !strings.Contains(body, want) || strings.Contains(body, other) {
		t.Errorf("want %s tagline; body:\n%s", lang, body)
	}
	if strings.Contains(body, "app.tagline") {
		t.Error("untranslated key rendered")
	}
}

func TestLayoutRendersInBothLanguages(t *testing.T) {
	s := newTestServer(t)
	assertLang(t, getPage(t, s, "/login?lang=en", ""), "en")
	assertLang(t, getPage(t, s, "/login?lang=zh", ""), "zh")
	assertLang(t, getPage(t, s, "/login?lang=zh-CN", "en"), "zh")
	assertLang(t, getPage(t, s, "/login?lang=fr", "zh"), "zh")
}

func TestLangResolution(t *testing.T) {
	noenv := func(string) string { return "" }
	zhEnv := func(k string) string {
		if k == "PANDA_LANG" {
			return "zh"
		}
		return ""
	}

	t.Run("default en", func(t *testing.T) {
		assertLang(t, getPage(t, newTestServer(t), "/login", ""), "en")
	})
	t.Run("accept-language when no site lang", func(t *testing.T) {
		assertLang(t, getPage(t, newTestServer(t), "/login", "zh-CN,zh;q=0.9,en;q=0.8"), "zh")
	})
	t.Run("explicit PANDA_LANG beats accept-language", func(t *testing.T) {
		assertLang(t, getPage(t, newTestServerWith(t, zhEnv, nil), "/login", "en-US"), "zh")
	})
	t.Run("site_lang setting beats PANDA_LANG and accept-language", func(t *testing.T) {
		database := dbtest.Open(t)
		if _, err := database.Exec(`INSERT INTO settings (key, value) VALUES ('site_lang', 'en')`); err != nil {
			t.Fatal(err)
		}
		assertLang(t, getPage(t, newTestServerWith(t, zhEnv, database), "/login", "zh"), "en")
	})
	t.Run("no site_lang row falls through to accept-language", func(t *testing.T) {
		assertLang(t, getPage(t, newTestServerWith(t, noenv, dbtest.Open(t)), "/login", "zh"), "zh")
	})
	t.Run("query override beats site setting", func(t *testing.T) {
		assertLang(t, getPage(t, newTestServerWith(t, zhEnv, nil), "/login?lang=en", "zh"), "en")
	})
}
