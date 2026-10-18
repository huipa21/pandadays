package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.DataDir != "/data" || cfg.BaseURL != "" || !cfg.CookieSecure {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Location.String() != "UTC" {
		t.Fatalf("location = %s, want UTC", cfg.Location)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ADDR":          "127.0.0.1:9000",
		"DATA_DIR":      "./data",
		"BASE_URL":      "https://days.example.com/",
		"COOKIE_SECURE": "false",
		"TZ":            "Asia/Shanghai",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.DataDir != "./data" || cfg.BaseURL != "https://days.example.com" ||
		cfg.CookieSecure || cfg.Location.String() != "Asia/Shanghai" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"secure": {"COOKIE_SECURE": "maybe"},
		"tz":     {"TZ": "Not/AZone"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadIgnoresLegacyAndLanguageVariables(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PANDA_ADDR":          ":9000",
		"PANDA_DATA_DIR":      "./old",
		"PANDA_BASE_URL":      "https://old.example.com",
		"PANDA_COOKIE_SECURE": "false",
		"PANDA_LANG":          "zh",
		"LANG":                "zh_CN.UTF-8",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.DataDir != "/data" || cfg.BaseURL != "" || !cfg.CookieSecure {
		t.Fatalf("legacy variables changed config: %+v", cfg)
	}
}
