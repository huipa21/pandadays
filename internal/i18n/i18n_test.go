package i18n

import (
	"context"
	"slices"
	"testing"
	"testing/fstest"
)

func TestEmbeddedLocalesHaveIdenticalKeys(t *testing.T) {
	b, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	en := b.Keys("en")
	if len(en) == 0 {
		t.Fatal("en.json has no keys")
	}
	for _, lang := range Supported {
		keys := b.Keys(lang)
		for _, k := range en {
			if !slices.Contains(keys, k) {
				t.Errorf("%s.json missing key %q", lang, k)
			}
		}
		for _, k := range keys {
			if !slices.Contains(en, k) {
				t.Errorf("%s.json has extra key %q not in en.json", lang, k)
			}
		}
		for _, k := range keys {
			if b.msgs[lang][k] == "" {
				t.Errorf("%s.json: empty value for %q", lang, k)
			}
		}
	}
}

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load(fstest.MapFS{
		"en.json": {Data: []byte(`{"hello":"Hello","only.en":"English only","count":"%d days","blank":"Blank"}`)},
		"zh.json": {Data: []byte(`{"hello":"你好","count":"%d 天","blank":""}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTranslate(t *testing.T) {
	b := testBundle(t)
	cases := []struct {
		lang, key string
		args      []any
		want      string
	}{
		{"en", "hello", nil, "Hello"},
		{"zh", "hello", nil, "你好"},
		{"zh", "only.en", nil, "English only"},
		{"zh", "blank", nil, "Blank"},
		{"zh", "count", []any{3}, "3 天"},
		{"en", "count", []any{3}, "3 days"},
		{"zh", "missing.key", nil, "missing.key"},
		{"fr", "hello", nil, "Hello"},
	}
	for _, c := range cases {
		if got := b.T(c.lang, c.key, c.args...); got != c.want {
			t.Errorf("T(%q,%q) = %q, want %q", c.lang, c.key, got, c.want)
		}
	}
	if got := b.Func("zh")("hello"); got != "你好" {
		t.Errorf("Func(zh)(hello) = %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(fstest.MapFS{"en.json": {Data: []byte(`{}`)}}); err == nil {
		t.Error("expected error for missing zh.json")
	}
	if _, err := Load(fstest.MapFS{
		"en.json": {Data: []byte(`{}`)},
		"zh.json": {Data: []byte(`{nope`)},
	}); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"en": "en", "EN-us": "en", "zh-CN": "zh", "zh_Hant_TW": "zh", " zh ": "zh",
		"fr": "", "": "", "*": "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		"zh-CN,zh;q=0.9,en;q=0.8":            "zh",
		"en-US,en;q=0.9,zh;q=0.8":            "en",
		"fr-FR,fr;q=0.9,zh-TW;q=0.5":         "zh",
		"en;q=0.3, zh;q=0.7":                 "zh",
		"zh;q=0, en;q=0.1":                   "en",
		"fr, de":                             "",
		"*":                                  "",
		"zh;q=bogus, en;q=0.2":               "en",
		"de;q=1.0,zh-Hans-CN;q=0.8,en;q=0.8": "zh",
	} {
		if got := FromAcceptLanguage(in); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveOrder(t *testing.T) {
	cases := []struct {
		name, user, site, accept, want string
	}{
		{"user wins", "zh", "en", "en", "zh"},
		{"site when no user", "", "zh", "en", "zh"},
		{"invalid user skipped", "fr", "zh", "en", "zh"},
		{"accept-language when no user/site", "", "", "zh-CN,zh;q=0.9", "zh"},
		{"default en", "", "", "fr", "en"},
		{"all empty", "", "", "", "en"},
	}
	for _, c := range cases {
		if got := Resolve(c.user, c.site, c.accept); got != c.want {
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLangContext(t *testing.T) {
	if got := LangFrom(context.Background()); got != Fallback {
		t.Errorf("empty ctx = %q", got)
	}
	if got := LangFrom(WithLang(context.Background(), "zh")); got != "zh" {
		t.Errorf("got %q", got)
	}
}
