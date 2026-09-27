// Command upsera is the Upsera server: REST API, scheduler and checkers in
// one binary.
//
//	upsera              run the server (configured from the environment)
//	upsera healthcheck  exit 0 if the local server's /healthz answers 200
//	upsera version      print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in distroless images without zoneinfo

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/jobs"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/scheduler"
	"github.com/davidsugianto/upsera/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck(os.Getenv("PORT")))
		case "version", "--version", "-v":
			fmt.Println(version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (commands: healthcheck, version)\n", os.Args[1])
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(1)
	}
	log := cfg.NewLogger()
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Error("listen", "port", cfg.Port, "err", err)
		os.Exit(1)
	}
	if err := run(ctx, cfg, log, ln); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run starts every component, serves HTTP on ln until ctx is cancelled, then
// shuts down in order: stop accepting requests, stop checks and flush
// buffered heartbeats, close the database.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ln net.Listener) error {
	log.Info("starting upsera", "version", version, "addr", ln.Addr().String(), "tz", cfg.TimeZone)

	st, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx, log); err != nil {
		return err
	}
	probeID, err := st.LocalProbeID(ctx)
	if err != nil {
		return fmt.Errorf("local probe: %w", err)
	}
	settings, err := st.GetInstanceSettings(ctx)
	if err != nil {
		return fmt.Errorf("instance settings: %w", err)
	}
	monitors, err := st.ListAllMonitors(ctx)
	if err != nil {
		return fmt.Errorf("load monitors: %w", err)
	}
	states, err := st.ListMonitorStates(ctx)
	if err != nil {
		return fmt.Errorf("load monitor state: %w", err)
	}

	policy := netpolicy.New(cfg.DockerHost)
	policy.SetBlockPrivate(settings.BlockPrivateTargets)

	sched := scheduler.New(st, checker.New(policy), scheduler.Options{
		ProbeID:       probeID,
		MaxConcurrent: cfg.MaxConcurrentChecks,
		BufferSize:    cfg.HeartbeatBufferSize,
		FlushInterval: cfg.FlushInterval,
		Logger:        log.With("component", "scheduler"),
	})
	// The scheduler outlives ctx: it is stopped explicitly below so buffered
	// heartbeats are flushed after the HTTP server has drained.
	schedCtx, cancelSched := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelSched()
	sched.Start(schedCtx, monitors, states)
	log.Info("scheduler started", "monitors", len(monitors))

	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		jobs.Run(ctx, st, jobs.Options{
			Every:                cfg.RollupEvery,
			DefaultRetentionDays: cfg.RetentionDays,
			TZ:                   cfg.TimeZone,
			Logger:               log.With("component", "jobs"),
			OnSettings:           func(s model.InstanceSettings) { policy.SetBlockPrivate(s.BlockPrivateTargets) },
		})
	}()

	srv := &http.Server{
		Handler: api.NewRouter(api.Deps{
			Store: st, Runner: sched, Policy: policy, Config: cfg,
			Logger: log.With("component", "api"), Version: version,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			cancelSched()
			return fmt.Errorf("http server: %w", err)
		}
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	if err := sched.Shutdown(shutdownCtx); err != nil {
		log.Warn("scheduler shutdown", "err", err)
	}
	<-jobsDone
	log.Info("stopped")
	return nil
}

// openStore connects to Postgres, retrying with backoff so the server
// survives starting before its database (or during a Supabase blip).
func openStore(ctx context.Context, cfg config.Config, log *slog.Logger) (*store.Store, error) {
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, err // malformed URL: retrying will not help
	}
	backoff := time.Second
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := st.Ping(pingCtx)
		cancel()
		if err == nil {
			return st, nil
		}
		log.Warn("database unreachable, retrying", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			st.Close()
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// healthcheck probes the local server; used as the container HEALTHCHECK
// because distroless images have no curl.
func healthcheck(port string) int {
	if port == "" {
		port = "3080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
