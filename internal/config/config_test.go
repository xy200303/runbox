package config

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaults(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"RUNBOX_API_KEY": "secret"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != ":8099" || cfg.Root != ".runbox" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.MaxConcurrency != 2 || cfg.MaxUploadBytes != 20<<20 || cfg.QuotaBytes != 1<<30 {
		t.Fatalf("unexpected limits: %+v", cfg)
	}
	if cfg.MaxCommandBytes != 64<<10 || cfg.MaxAttachments != 64 {
		t.Fatalf("unexpected command/input limits: %+v", cfg)
	}
	if cfg.AllowMissingCapabilities {
		t.Fatal("missing capabilities must fail closed by default")
	}
	if cfg.Retention != 7*24*time.Hour || cfg.DefaultTimeout != 60*time.Second || cfg.MaxTimeout != 300*time.Second {
		t.Fatalf("unexpected durations: %+v", cfg)
	}
}

func TestAPIKeyRequired(t *testing.T) {
	if _, err := FromEnv(env(nil)); err == nil || !strings.Contains(err.Error(), "RUNBOX_API_KEY") {
		t.Fatalf("expected API key error, got %v", err)
	}
	cfg, err := FromEnv(env(map[string]string{"RUNBOX_ALLOW_ANONYMOUS": "1"}))
	if err != nil {
		t.Fatalf("anonymous load: %v", err)
	}
	if !cfg.AllowAnonymous {
		t.Fatal("anonymous flag not set")
	}
}

func TestForeignEnvIsIgnored(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"RUNBOX_API_KEY":         "real-key",
		"RUNBOX_MAX_CONCURRENCY": "4",
		"SANDBOX_RUNNER_ADDR":    ":9999",
		"SANDBOX_RUNNER_KEY":     "foreign-key",
		"AGENT_SANDBOX_ROOT":     "/data",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != ":8099" || cfg.Root != ".runbox" || cfg.APIKey != "real-key" || cfg.MaxConcurrency != 4 {
		t.Fatalf("foreign environment was not ignored: %+v", cfg)
	}
	if _, err := FromEnv(env(map[string]string{"SANDBOX_RUNNER_KEY": "foreign-key"})); err == nil {
		t.Fatal("a foreign API key must not satisfy RUNBOX_API_KEY")
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"RUNBOX_API_KEY":            "k",
		"RUNBOX_MAX_UPLOAD_BYTES":   "1MiB",
		"RUNBOX_MAX_COMMAND_BYTES":  "1KiB",
		"RUNBOX_MAX_ATTACHMENTS":    "8",
		"RUNBOX_QUOTA_BYTES":        "2GiB",
		"RUNBOX_RETENTION":          "3d",
		"RUNBOX_MAX_TIMEOUT":        "10m",
		"RUNBOX_DEFAULT_TIMEOUT":    "30s",
		"RUNBOX_UID_MIN":            "1000",
		"RUNBOX_UID_MAX":            "1100",
		"RUNBOX_LOG_FORMAT":         "json",
		"RUNBOX_ALLOW_MISSING_CAPS": "1",
		"RUNBOX_PIP_INDEX":          "https://pypi.example/simple",
		"RUNBOX_NPM_REGISTRY":       "https://npm.example",
	}))
	if err != nil {
		t.Fatalf("load overrides: %v", err)
	}
	if cfg.MaxUploadBytes != 1<<20 || cfg.QuotaBytes != 2<<30 {
		t.Fatalf("unexpected sizes: %+v", cfg)
	}
	if cfg.MaxCommandBytes != 1<<10 || cfg.MaxAttachments != 8 {
		t.Fatalf("unexpected command/input limits: %+v", cfg)
	}
	if cfg.Retention != 72*time.Hour || cfg.MaxTimeout != 10*time.Minute {
		t.Fatalf("unexpected durations: %+v", cfg)
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("unexpected log format: %s", cfg.LogFormat)
	}
	if !cfg.AllowMissingCapabilities {
		t.Fatal("RUNBOX_ALLOW_MISSING_CAPS was not applied")
	}
	if cfg.PipIndex != "https://pypi.example/simple" || cfg.NpmRegistry != "https://npm.example" {
		t.Fatalf("unexpected package mirrors: %+v", cfg)
	}
}

func TestInvalidCombinations(t *testing.T) {
	cases := map[string]map[string]string{
		"small timeout": {
			"RUNBOX_API_KEY": "k", "RUNBOX_DEFAULT_TIMEOUT": "10m", "RUNBOX_MAX_TIMEOUT": "1m",
		},
		"bad pip mirror": {
			"RUNBOX_API_KEY": "k", "RUNBOX_PIP_INDEX": "pypi.example",
		},
		"bad uid range": {
			"RUNBOX_API_KEY": "k", "RUNBOX_UID_MIN": "200", "RUNBOX_UID_MAX": "100",
		},
		"uid zero": {
			"RUNBOX_API_KEY": "k", "RUNBOX_UID_MIN": "0", "RUNBOX_UID_MAX": "100",
		},
		"tls pair": {
			"RUNBOX_API_KEY": "k", "RUNBOX_TLS_CERT": "/tmp/cert",
		},
		"log format": {
			"RUNBOX_API_KEY": "k", "RUNBOX_LOG_FORMAT": "xml",
		},
		"request smaller than upload": {
			"RUNBOX_API_KEY": "k", "RUNBOX_MAX_UPLOAD_BYTES": "20MB", "RUNBOX_MAX_REQUEST_BYTES": "1MB",
		},
		"allow missing caps": {
			"RUNBOX_API_KEY": "k", "RUNBOX_ALLOW_MISSING_CAPS": "maybe",
		},
		"zero inputs": {
			"RUNBOX_API_KEY": "k", "RUNBOX_MAX_ATTACHMENTS": "0",
		},
	}
	for name, values := range cases {
		if _, err := FromEnv(env(values)); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

func TestParseBytes(t *testing.T) {
	valid := map[string]int64{
		"1024": 1024,
		"1B":   1,
		"1K":   1024,
		"1KiB": 1024,
		"1KB":  1000,
		"20MB": 20 * 1000 * 1000,
		"2MiB": 2 << 20,
		"1GiB": 1 << 30,
	}
	for input, want := range valid {
		got, err := ParseBytes(input)
		if err != nil || got != want {
			t.Fatalf("ParseBytes(%q)=%d,%v want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "abc", "-1", "1.5GB"} {
		if _, err := ParseBytes(input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
}

func TestParseDuration(t *testing.T) {
	valid := map[string]time.Duration{
		"30":  30 * time.Second,
		"90s": 90 * time.Second,
		"2h":  2 * time.Hour,
		"7d":  7 * 24 * time.Hour,
	}
	for input, want := range valid {
		got, err := ParseDuration(input)
		if err != nil || got != want {
			t.Fatalf("ParseDuration(%q)=%v,%v want %v", input, got, err, want)
		}
	}
	if _, err := ParseDuration("soon"); err == nil {
		t.Fatal("expected error")
	}
}
