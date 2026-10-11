package main

import (
	"log/slog"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseEnvDefaults(t *testing.T) {
	s, err := parseEnv(env(map[string]string{"SERVER_URL": "https://up.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config
	if c.ServerURL != "https://up.example.com" || c.DataDir != "/data" || c.BufferSize != 10000 ||
		c.MaxConcurrent != 50 || c.AllowInsecureHTTP || c.Token != "" {
		t.Fatalf("config = %+v", c)
	}
	if s.LogLevel != slog.LevelInfo || s.LogFormat != "json" {
		t.Fatalf("log = %v %q", s.LogLevel, s.LogFormat)
	}
}

func TestParseEnvValues(t *testing.T) {
	s, err := parseEnv(env(map[string]string{
		"SERVER_URL": "http://localhost:8080", "PROBE_TOKEN": "upr_x", "DATA_DIR": "/tmp/p",
		"BUFFER_SIZE": "5", "MAX_CONCURRENT_CHECKS": "3", "ALLOW_INSECURE_HTTP": "1",
		"LOG_LEVEL": "debug", "LOG_FORMAT": "text",
	}))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config
	if c.Token != "upr_x" || c.DataDir != "/tmp/p" || c.BufferSize != 5 || c.MaxConcurrent != 3 || !c.AllowInsecureHTTP {
		t.Fatalf("config = %+v", c)
	}
	if s.LogLevel != slog.LevelDebug || s.LogFormat != "text" {
		t.Fatalf("log = %v %q", s.LogLevel, s.LogFormat)
	}
}

func TestParseEnvErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"missing server url": {},
		"bad buffer":         {"SERVER_URL": "https://x", "BUFFER_SIZE": "0"},
		"junk buffer":        {"SERVER_URL": "https://x", "BUFFER_SIZE": "abc"},
		"bad concurrency":    {"SERVER_URL": "https://x", "MAX_CONCURRENT_CHECKS": "-1"},
		"bad log level":      {"SERVER_URL": "https://x", "LOG_LEVEL": "loud"},
		"bad log format":     {"SERVER_URL": "https://x", "LOG_FORMAT": "xml"},
		"http without flag":  {"SERVER_URL": "http://x"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEnv(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	// All problems reported together.
	_, err := parseEnv(env(map[string]string{"BUFFER_SIZE": "x", "LOG_LEVEL": "loud"}))
	if err == nil || !strings.Contains(err.Error(), "SERVER_URL") || !strings.Contains(err.Error(), "BUFFER_SIZE") || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("err = %v, want all three problems", err)
	}
}
