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
	if cfg.Addr != ":8080" || cfg.DataDir != "/data" || cfg.BaseURL != "" || cfg.Lang != "en" || !cfg.CookieSecure {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Location.String() != "UTC" {
		t.Fatalf("location = %s, want UTC", cfg.Location)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PANDA_ADDR":          "127.0.0.1:9000",
		"PANDA_DATA_DIR":      "./data",
		"PANDA_BASE_URL":      "https://days.example.com/",
		"PANDA_LANG":          "ZH",
		"PANDA_COOKIE_SECURE": "false",
		"TZ":                  "Asia/Shanghai",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.DataDir != "./data" || cfg.BaseURL != "https://days.example.com" ||
		cfg.Lang != "zh" || cfg.CookieSecure || cfg.Location.String() != "Asia/Shanghai" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"lang":   {"PANDA_LANG": "fr"},
		"secure": {"PANDA_COOKIE_SECURE": "maybe"},
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
