// Command upsera-probe is the Upsera remote probe: it checks the monitors
// assigned to its region from wherever it runs and reports the results to the
// Upsera server.
//
//	upsera-probe          run the probe (configured from the environment)
//	upsera-probe version  print the version
//
// Environment: SERVER_URL (required), PROBE_TOKEN (first start only),
// DATA_DIR (default /data), BUFFER_SIZE (10000), MAX_CONCURRENT_CHECKS (50),
// ALLOW_INSECURE_HTTP (1 to allow an http:// SERVER_URL), LOG_LEVEL, LOG_FORMAT.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	_ "time/tzdata" // keep time zone lookups working in minimal images

	"github.com/davidsugianto/upsera/internal/probe"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println(version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (commands: version)\n", os.Args[1])
			os.Exit(2)
		}
	}

	s, err := parseEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(1)
	}
	log := s.newLogger()
	slog.SetDefault(log)
	s.Config.Version = version
	s.Config.Logger = log

	agent, err := probe.New(s.Config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting upsera-probe", "version", version, "server", s.Config.ServerURL)
	if err := agent.Run(ctx); err != nil {
		var fe *probe.FatalError
		if errors.As(err, &fe) {
			fmt.Fprintf(os.Stderr, "upsera-probe: %s\n", fe.Msg)
		} else {
			log.Error("probe stopped", "err", err)
		}
		os.Exit(1)
	}
	log.Info("upsera-probe stopped")
}

// settings is the parsed environment.
type settings struct {
	Config    probe.Config
	LogLevel  slog.Level
	LogFormat string
}

func (s settings) newLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: s.LogLevel}
	if s.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// parseEnv reads the configuration from getenv and reports every problem at
// once.
func parseEnv(getenv func(string) string) (settings, error) {
	var errs []error
	var s settings

	s.Config.ServerURL = strings.TrimSpace(getenv("SERVER_URL"))
	if s.Config.ServerURL == "" {
		errs = append(errs, errors.New("SERVER_URL is required (the base URL of your Upsera server)"))
	}
	s.Config.Token = strings.TrimSpace(getenv("PROBE_TOKEN"))
	s.Config.DataDir = orDefault(getenv("DATA_DIR"), probe.DefaultDataDir)
	s.Config.AllowInsecureHTTP = getenv("ALLOW_INSECURE_HTTP") == "1"
	if s.Config.ServerURL != "" {
		if lower := strings.ToLower(s.Config.ServerURL); strings.HasPrefix(lower, "http://") && !s.Config.AllowInsecureHTTP {
			errs = append(errs, errors.New("SERVER_URL uses http://, which would send the probe credential in clear text; use https:// (or set ALLOW_INSECURE_HTTP=1 for local testing)"))
		}
	}

	var err error
	if s.Config.BufferSize, err = positiveInt(getenv, "BUFFER_SIZE", probe.DefaultBufferSize); err != nil {
		errs = append(errs, err)
	}
	if s.Config.MaxConcurrent, err = positiveInt(getenv, "MAX_CONCURRENT_CHECKS", probe.DefaultMaxConcurrent); err != nil {
		errs = append(errs, err)
	}

	if err := s.LogLevel.UnmarshalText([]byte(orDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	s.LogFormat = strings.ToLower(orDefault(getenv("LOG_FORMAT"), "json"))
	if s.LogFormat != "json" && s.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", s.LogFormat))
	}
	return s, errors.Join(errs...)
}

func positiveInt(getenv func(string) string, key string, def int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def, fmt.Errorf("%s must be a positive integer, got %q", key, raw)
	}
	return n, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
