// Package i18n loads UI translations and resolves the language for a request.
package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"pandadays/locales"
)

// Fallback is the language used when a key or language is missing.
const Fallback = "en"

// Supported lists the languages the UI ships with (SPEC §4.5).
var Supported = []string{"en", "zh"}

// Bundle holds flat key → message maps per language. It is read-only after Load.
type Bundle struct {
	msgs map[string]map[string]string
}

// Load reads <lang>.json for every supported language from fsys.
func Load(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{msgs: make(map[string]map[string]string, len(Supported))}
	for _, lang := range Supported {
		data, err := fs.ReadFile(fsys, lang+".json")
		if err != nil {
			return nil, fmt.Errorf("i18n: read %s: %w", lang, err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("i18n: parse %s.json: %w", lang, err)
		}
		b.msgs[lang] = m
	}
	return b, nil
}

// LoadEmbedded loads the translations embedded in the binary.
func LoadEmbedded() (*Bundle, error) { return Load(locales.FS()) }

// Keys returns the sorted message keys defined for lang.
func (b *Bundle) Keys(lang string) []string {
	keys := make([]string, 0, len(b.msgs[lang]))
	for k := range b.msgs[lang] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// T translates key into lang, falling back to English and then to the key itself.
// When args are given the message is used as a fmt format string.
func (b *Bundle) T(lang, key string, args ...any) string {
	msg, ok := b.msgs[lang][key]
	if !ok || msg == "" {
		msg, ok = b.msgs[Fallback][key]
	}
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

// Func returns a translator bound to lang, suitable as the template func "t".
func (b *Bundle) Func(lang string) func(string, ...any) string {
	return func(key string, args ...any) string { return b.T(lang, key, args...) }
}

// IsSupported reports whether lang is exactly one of Supported.
func IsSupported(lang string) bool {
	for _, l := range Supported {
		if l == lang {
			return true
		}
	}
	return false
}

// Normalize maps a language tag such as "zh-CN" or "EN_us" to a supported
// language, or returns "" if it is not supported.
func Normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	if IsSupported(tag) {
		return tag
	}
	return ""
}

// FromAcceptLanguage returns the highest-weighted supported language in an
// Accept-Language header value, or "" if none match.
func FromAcceptLanguage(header string) string {
	type entry struct {
		lang string
		q    float64
	}
	var entries []entry
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					f = 0
				}
				q = f
			}
		}
		if lang := Normalize(tag); lang != "" && q > 0 {
			entries = append(entries, entry{lang, q})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].q > entries[j].q })
	if len(entries) == 0 {
		return ""
	}
	return entries[0].lang
}

// Resolve picks the UI language: user setting → site setting →
// Accept-Language → English. Empty or unsupported values are skipped.
func Resolve(userLang, siteLang, acceptLanguage string) string {
	for _, l := range []string{userLang, siteLang} {
		if l = Normalize(l); l != "" {
			return l
		}
	}
	if l := FromAcceptLanguage(acceptLanguage); l != "" {
		return l
	}
	return Fallback
}

type ctxKey struct{}

// WithLang stores the resolved language in ctx.
func WithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// LangFrom returns the language stored by WithLang, or Fallback.
func LangFrom(ctx context.Context) string {
	if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
		return l
	}
	return Fallback
}
