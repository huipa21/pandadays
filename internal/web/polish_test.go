package web

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"pandadays/internal/i18n"
	assets "pandadays/web"
)

// TestTemplateKeysAreTranslated guards against untranslated UI text: every
// literal key passed to "t" in a template must exist in every locale.
func TestTemplateKeysAreTranslated(t *testing.T) {
	bundle, err := i18n.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	keyPattern := regexp.MustCompile(`\bt "([a-z0-9_.]+)"`)
	names, err := fs.Glob(assets.Templates(), "*.html")
	if err != nil || len(names) == 0 {
		t.Fatalf("templates: %v %v", names, err)
	}
	seen := 0
	for _, name := range names {
		data, err := fs.ReadFile(assets.Templates(), name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyPattern.FindAllStringSubmatch(string(data), -1) {
			seen++
			for _, lang := range i18n.Supported {
				if !slices.Contains(bundle.Keys(lang), m[1]) {
					t.Errorf("%s: key %q missing from %s.json", name, m[1], lang)
				}
			}
		}
	}
	if seen < 100 {
		t.Fatalf("only %d translated keys found; pattern may be stale", seen)
	}
}

func TestStaticVersionTracksContent(t *testing.T) {
	a, err := staticVersion(fstest.MapFS{"app.css": {Data: []byte("a{}")}, "app.js": {Data: []byte("1")}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := staticVersion(fstest.MapFS{"app.css": {Data: []byte("b{}")}, "app.js": {Data: []byte("1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 12 || a == b {
		t.Fatalf("versions %q and %q should differ and be 12 hex chars", a, b)
	}
	s, _, _, token := authenticatedServer(t)
	body := get(s.Handler(), "/", token).Body.String()
	if !strings.Contains(body, `href="/static/app.css?v=`+s.assetVersion+`"`) {
		t.Fatalf("layout does not version app.css with %q", s.assetVersion)
	}
	if !strings.Contains(body, `<link rel="icon" type="image/svg+xml" href="/static/favicon.svg?v=`+s.assetVersion+`">`) {
		t.Fatal("authenticated layout is missing its versioned favicon")
	}
	if rr := get(s.Handler(), "/static/app.css?v="+s.assetVersion, ""); rr.Code != 200 {
		t.Fatalf("versioned asset: %d", rr.Code)
	}
}

func TestLayoutAccessibilityLandmarks(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	for _, lang := range []string{"en", "zh"} {
		body := get(s.Handler(), "/settings?lang="+lang, token).Body.String()
		want := map[string][]string{
			"en": {`<a class="skip-link" href="#main">Skip to main content</a>`, `<nav class="nav" aria-label="Main">`},
			"zh": {`<a class="skip-link" href="#main">跳到主要内容</a>`, `<nav class="nav" aria-label="主导航">`},
		}[lang]
		want = append(want, `<main class="container" id="main" tabindex="-1">`, `id="flash" role="alert" hidden`,
			`<script src="/static/app.js?v=`, `action="/settings/language"`, `id="theme-toggle"`)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s layout missing %q", lang, w)
			}
		}
	}
}
