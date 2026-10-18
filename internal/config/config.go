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
	CookieSecure bool
	Location     *time.Location
}

// Load reads configuration using getenv (typically os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:    get("ADDR", ":8080"),
		DataDir: get("DATA_DIR", "/data"),
		BaseURL: strings.TrimRight(get("BASE_URL", ""), "/"),
	}

	secure, err := strconv.ParseBool(get("COOKIE_SECURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("COOKIE_SECURE: %w", err)
	}
	cfg.CookieSecure = secure

	loc, err := time.LoadLocation(get("TZ", "UTC"))
	if err != nil {
		return Config{}, fmt.Errorf("TZ: %w", err)
	}
	cfg.Location = loc

	return cfg, nil
}
