// Package config loads Runbox server configuration from environment
// variables. Every setting has a safe default; the API key does not.
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xy200303/runbox/sdk/go"
)

// Config is the resolved server configuration.
type Config struct {
	Addr                     string
	APIKey                   string
	AllowAnonymous           bool
	AllowMissingCapabilities bool
	Root                     string
	MaxRequestBytes          int64
	MaxUploadBytes           int64
	MaxCommandBytes          int64
	MaxAttachments           int
	MaxStdoutBytes           int
	MaxOutputFiles           int
	MaxOutputBytes           int64
	MaxBatchFiles            int
	MaxBatchBytes            int64
	QuotaBytes               int64
	Retention                time.Duration
	SweepInterval            time.Duration
	MaxConcurrency           int
	UIDMin                   int
	UIDMax                   int
	DefaultTimeout           time.Duration
	MaxTimeout               time.Duration
	ShutdownTimeout          time.Duration
	TLSCert                  string
	TLSKey                   string
	LogFormat                string
	Version                  string
}

// Load reads configuration from the process environment.
func Load() (Config, error) { return FromEnv(os.Getenv) }

// FromEnv reads configuration through get, which makes the loader testable.
// Only RUNBOX_* names are read; there are no compatibility aliases.
func FromEnv(get func(string) string) (Config, error) {
	cfg := Config{
		Addr:            stringOr(get("RUNBOX_ADDR"), ":8099"),
		APIKey:          strings.TrimSpace(get("RUNBOX_API_KEY")),
		Root:            stringOr(get("RUNBOX_ROOT"), ".runbox"),
		MaxRequestBytes: 200 << 20,
		MaxUploadBytes:  sandbox.DefaultMaxUploadBytes,
		MaxCommandBytes: 64 << 10,
		MaxAttachments:  64,
		MaxStdoutBytes:  sandbox.DefaultMaxStdoutBytes,
		MaxOutputFiles:  sandbox.DefaultMaxOutputFiles,
		MaxOutputBytes:  sandbox.DefaultMaxOutputBytes,
		MaxBatchFiles:   1000,
		MaxBatchBytes:   1 << 30,
		QuotaBytes:      sandbox.DefaultQuotaBytes,
		Retention:       sandbox.DefaultRetention,
		SweepInterval:   time.Hour,
		MaxConcurrency:  2,
		UIDMin:          20000,
		UIDMax:          20100,
		DefaultTimeout:  sandbox.DefaultTimeout,
		MaxTimeout:      sandbox.DefaultMaxTimeout,
		ShutdownTimeout: 30 * time.Second,
		LogFormat:       "text",
		Version:         "dev",
	}

	boolValue, err := parseBool(get("RUNBOX_ALLOW_ANONYMOUS"), false)
	if err != nil {
		return Config{}, fmt.Errorf("RUNBOX_ALLOW_ANONYMOUS: %w", err)
	}
	cfg.AllowAnonymous = boolValue

	allowMissing, err := parseBool(get("RUNBOX_ALLOW_MISSING_CAPS"), false)
	if err != nil {
		return Config{}, fmt.Errorf("RUNBOX_ALLOW_MISSING_CAPS: %w", err)
	}
	cfg.AllowMissingCapabilities = allowMissing

	if value := get("RUNBOX_MAX_REQUEST_BYTES"); value != "" {
		if cfg.MaxRequestBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_REQUEST_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_UPLOAD_BYTES"); value != "" {
		if cfg.MaxUploadBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_UPLOAD_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_COMMAND_BYTES"); value != "" {
		if cfg.MaxCommandBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_COMMAND_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_ATTACHMENTS"); value != "" {
		if cfg.MaxAttachments, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_ATTACHMENTS: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_STDOUT_BYTES"); value != "" {
		if cfg.MaxStdoutBytes, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_STDOUT_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_OUTPUT_FILES"); value != "" {
		if cfg.MaxOutputFiles, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_OUTPUT_FILES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_OUTPUT_BYTES"); value != "" {
		if cfg.MaxOutputBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_OUTPUT_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_BATCH_FILES"); value != "" {
		if cfg.MaxBatchFiles, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_BATCH_FILES: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_BATCH_BYTES"); value != "" {
		if cfg.MaxBatchBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_BATCH_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_QUOTA_BYTES"); value != "" {
		if cfg.QuotaBytes, err = ParseBytes(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_QUOTA_BYTES: %w", err)
		}
	}
	if value := get("RUNBOX_RETENTION"); value != "" {
		if cfg.Retention, err = ParseDuration(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_RETENTION: %w", err)
		}
	}
	if value := get("RUNBOX_SWEEP_INTERVAL"); value != "" {
		if cfg.SweepInterval, err = ParseDuration(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_SWEEP_INTERVAL: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_CONCURRENCY"); value != "" {
		if cfg.MaxConcurrency, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_CONCURRENCY: %w", err)
		}
	}
	if value := get("RUNBOX_UID_MIN"); value != "" {
		if cfg.UIDMin, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_UID_MIN: %w", err)
		}
	}
	if value := get("RUNBOX_UID_MAX"); value != "" {
		if cfg.UIDMax, err = parseInt(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_UID_MAX: %w", err)
		}
	}
	if value := get("RUNBOX_DEFAULT_TIMEOUT"); value != "" {
		if cfg.DefaultTimeout, err = ParseDuration(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_DEFAULT_TIMEOUT: %w", err)
		}
	}
	if value := get("RUNBOX_MAX_TIMEOUT"); value != "" {
		if cfg.MaxTimeout, err = ParseDuration(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_MAX_TIMEOUT: %w", err)
		}
	}
	if value := get("RUNBOX_SHUTDOWN_TIMEOUT"); value != "" {
		if cfg.ShutdownTimeout, err = ParseDuration(value); err != nil {
			return Config{}, fmt.Errorf("RUNBOX_SHUTDOWN_TIMEOUT: %w", err)
		}
	}
	if value := get("RUNBOX_TLS_CERT"); value != "" {
		cfg.TLSCert = value
	}
	if value := get("RUNBOX_TLS_KEY"); value != "" {
		cfg.TLSKey = value
	}
	if value := strings.ToLower(strings.TrimSpace(get("RUNBOX_LOG_FORMAT"))); value != "" {
		cfg.LogFormat = value
	}
	if value := get("RUNBOX_VERSION"); value != "" {
		cfg.Version = value
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects combinations that would be unsafe or nonsensical.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("RUNBOX_ADDR must not be empty")
	}
	if c.APIKey == "" && !c.AllowAnonymous {
		return fmt.Errorf("RUNBOX_API_KEY is required (set RUNBOX_ALLOW_ANONYMOUS=1 only for local development)")
	}
	if strings.TrimSpace(c.Root) == "" {
		return fmt.Errorf("RUNBOX_ROOT must not be empty")
	}
	if c.MaxConcurrency <= 0 || c.MaxConcurrency > 4096 {
		return fmt.Errorf("RUNBOX_MAX_CONCURRENCY must be between 1 and 4096")
	}
	if c.UIDMin < 1 || c.UIDMax <= c.UIDMin || c.UIDMax > 1<<31 || c.UIDMax-c.UIDMin > 100000 {
		return fmt.Errorf("RUNBOX_UID_MIN/RUNBOX_UID_MAX must form a range of 1..100000 below 2^31")
	}
	if c.MaxUploadBytes <= 0 {
		return fmt.Errorf("RUNBOX_MAX_UPLOAD_BYTES must be positive")
	}
	if c.MaxCommandBytes <= 0 {
		return fmt.Errorf("RUNBOX_MAX_COMMAND_BYTES must be positive")
	}
	if c.MaxAttachments <= 0 {
		return fmt.Errorf("RUNBOX_MAX_ATTACHMENTS must be positive")
	}
	if c.MaxRequestBytes < c.MaxUploadBytes {
		return fmt.Errorf("RUNBOX_MAX_REQUEST_BYTES must be >= RUNBOX_MAX_UPLOAD_BYTES")
	}
	if c.MaxStdoutBytes < 1024 {
		return fmt.Errorf("RUNBOX_MAX_STDOUT_BYTES must be at least 1024")
	}
	if c.MaxOutputFiles < 0 || c.MaxOutputBytes < 0 || c.QuotaBytes < 0 {
		return fmt.Errorf("limits must not be negative")
	}
	if c.MaxBatchFiles <= 0 || c.MaxBatchBytes <= 0 {
		return fmt.Errorf("RUNBOX_MAX_BATCH_FILES and RUNBOX_MAX_BATCH_BYTES must be positive")
	}
	if c.Retention <= 0 || c.SweepInterval <= 0 {
		return fmt.Errorf("RUNBOX_RETENTION and RUNBOX_SWEEP_INTERVAL must be positive")
	}
	if c.DefaultTimeout <= 0 || c.MaxTimeout < c.DefaultTimeout {
		return fmt.Errorf("RUNBOX_MAX_TIMEOUT must be >= RUNBOX_DEFAULT_TIMEOUT > 0")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("RUNBOX_SHUTDOWN_TIMEOUT must be positive")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return fmt.Errorf("RUNBOX_TLS_CERT and RUNBOX_TLS_KEY must be set together")
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return fmt.Errorf("RUNBOX_LOG_FORMAT must be text or json")
	}
	return nil
}

// Limits returns the advertised resource envelope.
func (c Config) Limits() sandbox.Limits {
	return sandbox.Limits{
		MaxUploadBytes:   c.MaxUploadBytes,
		MaxCommandBytes:  c.MaxCommandBytes,
		MaxAttachments:   c.MaxAttachments,
		MaxStdoutBytes:   c.MaxStdoutBytes,
		MaxOutputFiles:   c.MaxOutputFiles,
		MaxOutputBytes:   c.MaxOutputBytes,
		MaxBatchFiles:    c.MaxBatchFiles,
		MaxBatchBytes:    c.MaxBatchBytes,
		QuotaBytes:       c.QuotaBytes,
		RetentionSeconds: int64(c.Retention / time.Second),
		DefaultTimeout:   int64(c.DefaultTimeout / time.Second),
		MaxTimeout:       int64(c.MaxTimeout / time.Second),
	}
}

// Platform describes the runtime for /v1/capabilities.
func Platform() (string, string) { return runtime.GOOS, runtime.GOARCH }

func stringOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func parseInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("expected an integer, got %q", value)
	}
	return parsed, nil
}

func parseBool(value string, fallback bool) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("expected a boolean, got %q", value)
	}
	return parsed, nil
}

// ParseDuration accepts Go duration strings, bare seconds and a day suffix.
func ParseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		if err == nil && days >= 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("expected a duration such as 7d or 24h, got %q", value)
	}
	return parsed, nil
}

// ParseBytes accepts plain bytes and binary or decimal suffixes
// (20MiB, 20MB, 1GiB, 512K).
func ParseBytes(value string) (int64, error) {
	text := strings.ToUpper(strings.TrimSpace(value))
	if text == "" {
		return 0, fmt.Errorf("empty byte size")
	}
	units := []struct {
		suffix     string
		multiplier int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1},
	}
	for _, unit := range units {
		if strings.HasSuffix(text, unit.suffix) {
			number := strings.TrimSpace(strings.TrimSuffix(text, unit.suffix))
			parsed, err := strconv.ParseInt(number, 10, 64)
			if err != nil || parsed < 0 {
				return 0, fmt.Errorf("invalid byte size %q", value)
			}
			if parsed > (1<<63-1)/unit.multiplier {
				return 0, fmt.Errorf("byte size %q is too large", value)
			}
			return parsed * unit.multiplier, nil
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid byte size %q", value)
	}
	return parsed, nil
}
