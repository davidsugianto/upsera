// Package probe is the remote checker agent. It registers with an Upsera
// server (once), polls the monitors assigned to its region, runs the checks
// locally with the same checker the server uses, and uploads the raw results.
//
// Results are buffered in a bounded in-memory ring, so a server outage costs
// no results until the buffer overflows (oldest are dropped first).
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/probeproto"
)

// Defaults for zero Config fields.
const (
	DefaultDataDir       = "/data"
	DefaultBufferSize    = 10000
	DefaultMaxConcurrent = 50
	DefaultFlushInterval = 5 * time.Second

	// shutdownFlushTimeout bounds the final upload on shutdown.
	shutdownFlushTimeout = 5 * time.Second
	// maxFirstCheckJitterS spreads the first checks of a freshly assigned
	// monitor set over at most this many seconds.
	maxFirstCheckJitterS = 10
	httpTimeout          = 30 * time.Second
)

// FatalError is returned by Run when the agent cannot continue and retrying
// is pointless (bad registration token, revoked credential, unusable data
// dir). The process should exit non-zero and show the message.
type FatalError struct{ Msg string }

func (e *FatalError) Error() string { return e.Msg }

// Checker runs one check. *checker.Checker implements it.
type Checker interface {
	Check(ctx context.Context, m model.Monitor) checker.Result
}

// Config configures an Agent.
type Config struct {
	ServerURL string // required; https:// unless AllowInsecureHTTP
	Token     string // registration token; only needed on first start
	DataDir   string // holds the credential file; default /data

	BufferSize        int // max buffered results; default 10000
	MaxConcurrent     int // max simultaneous checks; default 50
	AllowInsecureHTTP bool
	Version           string // agent version, sent to the server
	Logger            *slog.Logger

	PollInterval  time.Duration // default probeproto.PollInterval
	FlushInterval time.Duration // default 5s

	HTTPClient *http.Client // optional; for the server connection only
	Checker    Checker      // optional; default checker.New(netpolicy)

	// Test knobs: zero means the production value.
	unit        time.Duration // length of one "second" of monitor interval
	backoffBase time.Duration // first upload/registration retry delay
	backoffMax  time.Duration // cap of the retry delay
}

// Agent is a remote probe.
type Agent struct {
	cfg    Config
	base   string
	log    *slog.Logger
	client *http.Client
	policy *netpolicy.Policy
	chk    Checker
	buf    *ring
	sem    chan struct{}
	wake   chan struct{}

	// unit is the duration of one "second" of monitor interval; tests shrink
	// it. backoffBase/backoffMax bound the upload retry backoff.
	unit        time.Duration
	backoffBase time.Duration
	backoffMax  time.Duration

	credential string
	etag       string
	workers    map[int64]*worker // owned by the Run goroutine
	wg         sync.WaitGroup

	fatalMu   sync.Mutex
	fatalErr  error
	cancelRun context.CancelFunc
}

// New validates cfg and builds an Agent.
func New(cfg Config) (*Agent, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.ServerURL))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("SERVER_URL %q is not a valid URL", cfg.ServerURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !cfg.AllowInsecureHTTP {
			return nil, errors.New("SERVER_URL uses http://, which would send the probe credential in clear text; use https:// (or set ALLOW_INSECURE_HTTP=1 for local testing)")
		}
	default:
		return nil, fmt.Errorf("SERVER_URL must be an http(s) URL, got scheme %q", u.Scheme)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultBufferSize
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = probeproto.PollInterval
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: httpTimeout,
			// Never follow redirects: the Authorization header must only go
			// to the configured server.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if cfg.unit <= 0 {
		cfg.unit = time.Second
	}
	if cfg.backoffBase <= 0 {
		cfg.backoffBase = time.Second
	}
	if cfg.backoffMax <= 0 {
		cfg.backoffMax = 60 * time.Second
	}
	policy := netpolicy.New("")
	var chk Checker = cfg.Checker
	if chk == nil {
		chk = checker.New(policy)
	}
	return &Agent{
		cfg:         cfg,
		base:        strings.TrimRight(u.String(), "/"),
		log:         log,
		client:      client,
		policy:      policy,
		chk:         chk,
		buf:         newRing(cfg.BufferSize),
		sem:         make(chan struct{}, cfg.MaxConcurrent),
		wake:        make(chan struct{}, 1),
		unit:        cfg.unit,
		backoffBase: cfg.backoffBase,
		backoffMax:  cfg.backoffMax,
		workers:     map[int64]*worker{},
	}, nil
}

// Run runs the agent until ctx is cancelled (returns nil) or a fatal
// condition occurs (returns *FatalError). On cancellation it stops the
// checks and makes one bounded attempt to upload what is still buffered.
func (a *Agent) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.cancelRun = cancel

	cred, err := a.ensureCredential(runCtx)
	if err != nil {
		if fe := a.fatal(); fe != nil {
			return fe
		}
		var fe *FatalError
		if errors.As(err, &fe) {
			return err
		}
		return nil // context cancelled while registering
	}
	a.credential = cred

	uploadCtx, uploadCancel := context.WithCancel(context.Background())
	defer uploadCancel()
	stopUpload := make(chan struct{})
	uploadDone := make(chan struct{})
	go func() {
		defer close(uploadDone)
		a.uploadLoop(uploadCtx, stopUpload)
	}()

	a.pollLoop(runCtx)

	// Stop the checks first so every finished result is already buffered.
	for id, w := range a.workers {
		w.cancel()
		delete(a.workers, id)
	}
	a.wg.Wait()

	if a.fatal() != nil {
		uploadCancel() // credential is dead; a final flush would only 401
	} else {
		close(stopUpload)
	}
	select {
	case <-uploadDone:
	case <-time.After(shutdownFlushTimeout + time.Second):
		uploadCancel()
		<-uploadDone
	}

	if fe := a.fatal(); fe != nil {
		return fe
	}
	return nil
}

func (a *Agent) setFatal(err error) {
	a.fatalMu.Lock()
	if a.fatalErr == nil {
		a.fatalErr = err
	}
	a.fatalMu.Unlock()
	if a.cancelRun != nil {
		a.cancelRun()
	}
}

func (a *Agent) fatal() error {
	a.fatalMu.Lock()
	defer a.fatalMu.Unlock()
	return a.fatalErr
}

// sleep waits d or until ctx is done; it reports whether the wait completed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
