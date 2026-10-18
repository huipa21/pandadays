// Package config loads runtime configuration from environment variables (SPEC §1).
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr         string
	DataDir      string
	BaseURL      string
	Lang         string
	CookieSecure bool
	Location     *time.Location
}

var SupportedLangs = []string{"en", "zh"}

// Load reads configuration using getenv (typically os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:    get("PANDA_ADDR", ":8080"),
		DataDir: get("PANDA_DATA_DIR", "/data"),
		BaseURL: strings.TrimRight(get("PANDA_BASE_URL", ""), "/"),
		Lang:    strings.ToLower(get("PANDA_LANG", "en")),
	}

	if !isSupportedLang(cfg.Lang) {
		return Config{}, fmt.Errorf("PANDA_LANG: unsupported language %q (want en or zh)", cfg.Lang)
	}

	secure, err := strconv.ParseBool(get("PANDA_COOKIE_SECURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("PANDA_COOKIE_SECURE: %w", err)
	}
	cfg.CookieSecure = secure

	loc, err := time.LoadLocation(get("TZ", "UTC"))
	if err != nil {
		return Config{}, fmt.Errorf("TZ: %w", err)
	}
	cfg.Location = loc

	return cfg, nil
}

func isSupportedLang(lang string) bool {
	for _, l := range SupportedLangs {
		if l == lang {
			return true
		}
	}
	return false
}
