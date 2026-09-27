// Package config loads server settings from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the server configuration. Every field comes from an environment
// variable; see Load for names and defaults.
type Config struct {
	DatabaseURL string // DATABASE_URL (required)
	AppSecret   string // APP_SECRET (required, at least 32 bytes)
	BaseURL     string // BASE_URL, used for push URLs and links
	Port        int    // PORT, default 3080
	LogLevel    slog.Level
	LogFormat   string // LOG_FORMAT: "json" (default) or "text"

	DBMaxConns          int32  // DB_MAX_CONNS, default 10
	HeartbeatBufferSize int    // HEARTBEAT_BUFFER_SIZE, default 100000
	RetentionDays       int    // HEARTBEAT_RETENTION_DAYS, default 14
	MaxConcurrentChecks int    // MAX_CONCURRENT_CHECKS, default 100
	DockerHost          string // DOCKER_HOST, e.g. tcp://docker-proxy:2375
	TimeZone            string // TZ (IANA name), default UTC; used for daily rollup days

	FlushInterval time.Duration // HEARTBEAT_FLUSH_INTERVAL, default 1s
	RollupEvery   time.Duration // ROLLUP_INTERVAL, default 1h
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	c := Config{
		DatabaseURL: getenv("DATABASE_URL"),
		AppSecret:   getenv("APP_SECRET"),
		BaseURL:     strings.TrimRight(getenv("BASE_URL"), "/"),
		LogFormat:   strings.ToLower(orDefault(getenv("LOG_FORMAT"), "json")),
		DockerHost:  getenv("DOCKER_HOST"),
	}
	var errs []error
	intVar := func(name string, def, min int) int {
		raw := getenv(name)
		if raw == "" {
			return def
		}
		v, err := strconv.Atoi(raw)
		if err != nil || v < min {
			errs = append(errs, fmt.Errorf("%s must be an integer >= %d, got %q", name, min, raw))
			return def
		}
		return v
	}
	durVar := func(name string, def, min time.Duration) time.Duration {
		raw := getenv(name)
		if raw == "" {
			return def
		}
		v, err := time.ParseDuration(raw)
		if err != nil || v < min {
			errs = append(errs, fmt.Errorf("%s must be a duration >= %s, got %q", name, min, raw))
			return def
		}
		return v
	}

	c.Port = intVar("PORT", 3080, 1)
	if c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT must be <= 65535, got %d", c.Port))
	}
	c.DBMaxConns = int32(intVar("DB_MAX_CONNS", 10, 2))
	c.HeartbeatBufferSize = intVar("HEARTBEAT_BUFFER_SIZE", 100000, 100)
	c.RetentionDays = intVar("HEARTBEAT_RETENTION_DAYS", 14, 3)
	c.MaxConcurrentChecks = intVar("MAX_CONCURRENT_CHECKS", 100, 1)
	c.FlushInterval = durVar("HEARTBEAT_FLUSH_INTERVAL", time.Second, 100*time.Millisecond)
	c.RollupEvery = durVar("ROLLUP_INTERVAL", time.Hour, time.Minute)
	c.TimeZone = orDefault(getenv("TZ"), "UTC")
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		errs = append(errs, fmt.Errorf("TZ must be an IANA time zone like Asia/Jakarta, got %q", c.TimeZone))
	}

	if err := c.LogLevel.UnmarshalText([]byte(orDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if len(c.AppSecret) < 32 {
		errs = append(errs, errors.New("APP_SECRET is required and must be at least 32 characters (openssl rand -base64 32)"))
	}
	if c.BaseURL == "" {
		c.BaseURL = fmt.Sprintf("http://localhost:%d", c.Port)
	} else if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("BASE_URL must be an absolute http(s) URL, got %q", c.BaseURL))
	}
	return c, errors.Join(errs...)
}

// SecureCookies reports whether session cookies should carry the Secure flag.
func (c Config) SecureCookies() bool { return strings.HasPrefix(c.BaseURL, "https://") }

// NewLogger builds the process logger from LOG_LEVEL and LOG_FORMAT.
func (c Config) NewLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
