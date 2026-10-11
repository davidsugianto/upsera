# Upsera handover: Phases 1–5 done, Phases 6–8 next

Updated 2026-10-11 for the next session. Read this first, then `docs/upsera-build-plan.html`
(v0.5, the product plan; its "Build phases" section is the roadmap). `docs/PHASE-CHECKLIST.md` lists
every task of every phase with its done / partial / not started status. `docs/handover-phase3.md` is the
detailed snapshot written at the end of the Phase 3 session (dashboard internals, review checklist);
it is historical and not kept up to date.

## 1. State of the repo

All on `master`, nothing pushed, **CI has never run on GitHub** (its push trigger is `master`;
the first push is the first run of the `test` and `web` jobs, and the ping test depends on
`net.ipv4.ping_group_range`).

| Phase | Commit | Done when (test) |
|---|---|---|
| 1 Foundation | `65f2ef8` | monitors created via the REST API are checked and their heartbeats stored (`cmd/upsera` `TestPhase1DoneWhen`) |
| 2 Alerting engine | `fddd257` | an unacknowledged outage escalates Telegram → email; a DOWN parent silences its children (`TestPhase2DoneWhen`) |
| 3 Dashboard (+ Dockerfile, compose, Makefile) | `1d16565` | everything manageable from the browser; live SSE updates (`TestPhase3LiveDashboard`) |
| 4 Docker monitoring | `9b37dc9` | a watched container that stops raises an alert saying why (`TestPhase4DoneWhen`) |
| 5 Status pages | `a0b8ea2` | two public pages with different monitors render, and a subscriber gets the incident email (`TestPhase5DoneWhen`) |

- `make check` (gofmt, vet, eslint, tsc, `go test -race ./...`, vitest, web build) exits 0: 17 Go
  packages, 60 web tests. Takes about 3 minutes (testcontainers start Postgres 17 containers).
- Module `github.com/davidsugianto/upsera`, `go 1.26`. Go 1.26 rule: use `new(expr)` instead of
  pointer helpers like `intPtr`.
- Frontend in `web/`: React 19, Vite 8, TypeScript 5.9 (forced by `openapi-typescript` and
  `typescript-eslint`), Tailwind 4, react-router 8, TanStack Query 5, openapi-fetch, uPlot. It is
  embedded into the binary with `go:embed` (`web/web.go`).
- Not done yet: Phase 6 (multi-location probes), 7 (SLOs, `upsctl`), 8 (packaging: Caddy profile,
  docs site). The Dockerfile, `deploy/docker-compose.yml` and `Makefile` came forward from Phase 8.

## 2. Running things

```sh
# Docker for integration tests (colima on this Mac; `colima start` if stopped)
export DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock

make check                     # everything CI runs
make test / test-short / test-web / lint
make gen-api                   # after ANY API change, then commit web/src/api/schema.d.ts (CI fails if stale)
make dev                       # Vite on :5173 proxying /api to a server on :3080
make up / down                 # compose stack on http://localhost:3080 (uses the upsera_pg_data volume)
```

Smoke tests that must not touch your data volume: use a throwaway compose project,
`UPSERA_PORT=3081 docker compose -p upsera-smoke -f deploy/docker-compose.yml --profile local-db up -d --build`
and `... down -v` afterwards (push URLs embed `BASE_URL`, so push through the smoke port).
Local server without Docker: README "Run locally". Point `TELEGRAM_API_URL` / `SLACK_API_URL` at a fake
for local alerting tests. Machine gotchas: `rm` and `cp` are interactive aliases (use `command rm -f`),
zsh does not word-split unquoted variables, BSD `sed -i` needs `''`.

## 3. Layout

| Path | What |
|---|---|
| `cmd/upsera/main.go` | Wiring: config → store (retrying connect) → migrate → load monitors/states/channels/policies/windows/Docker hosts/open alerts/cert keys → secret box → netpolicy → registries → alerting engine → scheduler → status page service → jobs → HTTP. **Start order: `sched.Start` then `engine.Start`** (the engine reconciles open alerts against live states). Shutdown: HTTP drain → scheduler stop + flush → engine stop → status page worker → jobs → DB close. Startup also seeds the `local` Docker host (see §5). |
| `cmd/upsera/*_test.go` | End-to-end "done when" tests through the real `run()` for phases 1–5; helpers `startServer`, `setupAdmin`, fake Telegram, `testutil.FakeSMTP`. |
| `internal/config` | Env config (all vars in README). `TZ` drives rollup days and recurring windows. Phase 5 added `INCIDENT_DRAFT_AFTER`, `TRUSTED_PROXIES`. |
| `internal/model` | Shared types. `Status` int16 (0 down, 1 up, 2 pending, 3 maintenance). `alerting.go`, `docker.go` (DockerHost, DockerTLS), `status.go` (StatusPage, Incident, Subscriber, Delivery, SMTPSettings, enums). |
| `internal/secret` | `Box`: AES-256-GCM, key = HKDF-SHA256(`APP_SECRET`), ciphertext `v1:` + base64url. Encrypts channel configs, Docker host TLS material and the instance SMTP settings. |
| `internal/store` | pgx store. **Every table in schema `upsera`, every query schema-qualified.** Migrations `00001`–`00004` (committed — never edit; add a new one). Files: `monitors`, `alerting`, `dashboard`, `docker`, `status`, `tokens`, `accounts`. Sentinels: `ErrNotFound`, `ErrConflict`, `ErrInUse`, `ErrSetupDone`, `ErrLastOwner`. `SetSecretBox` must be called before encrypted reads/writes (testutil does it). |
| `internal/checker` | Per-type config validation + checks: http/keyword/tcp/ping/dns/docker; push has a watchdog. |
| `internal/dockerapi` | Minimal Docker Engine API client over `net/http` (tcp/http(s)/unix, optional TLS; ping, container inspect/list) and the per-host `Registry` the checker reads (no DB on the check path). |
| `internal/netpolicy` | Outbound target policy checked at dial time; used by checks **and** every notifier and the webhook/SMTP deliveries. `DialDockerHost`/`CheckDockerHost` refuse only unspecified/link-local/metadata (the Docker host itself and private ranges are allowed). Notifier HTTP clients never use `HTTP(S)_PROXY`. |
| `internal/scheduler` | In-memory monitor cache, goroutine per monitor, heartbeat buffer + flusher, `nextState` (UP / PENDING / DOWN), flap damping, maintenance and pause → MAINTENANCE. Emits `model.Transition` via `OnTransition` **under its lock, in order** (callback must not block). |
| `internal/maintenance` | `ActiveAt` and the `Registry` cache the scheduler consults. |
| `internal/notify` | `Notifier` per channel type, `Dispatcher`, `AckListeners`, and `SendMail` (shared SMTP sender used by the smtp channel and by subscriber email). |
| `internal/alerting` | `Engine`: transitions → alerts, escalation, acks, dependency suppression, flap handling, cert warnings, outbox for alerts and notification log (survives DB outage). |
| `internal/events` | In-memory per-team hub for the dashboard's SSE stream; `Publish` never blocks, slow subscribers are dropped. |
| `internal/statuspage` | The public side of status pages: server-rendered page and RSS (`handler`, `render`, `templates/`, `status.css`), 30s `cache`, custom-domain `domains` (Host middleware, `TLSAllowed`), `subscribe`/`token`/`clientip` (double opt-in, signed links, rate limits), and the background `worker`/`mail` (auto-drafting, outbox delivery). `service.go` is the contract; `fakestore_test.go` the shared test fake. |
| `internal/jobs` | Hourly: settings → rollup → prune heartbeats → expired sessions → notification log (30 days) → (optional interface) unconfirmed subscribers (7 days) and finished deliveries (30 days). |
| `internal/api` | chi + huma v2. Session cookie + CSRF header, or `Bearer ups_...` team token. Routes by file: `monitors`, `channels`, `policies`, `alerts`, `maintenance`, `dashboard` (overview, uptime summary, events), `events` (SSE), `docker_hosts`, `status_pages` (pages, incidents, subscribers), `admin` (users, settings incl. SMTP), `teams`, `auth`, `audit`. |
| `internal/testutil` | `Store(t)` / `PostgresURL(t)` testcontainers helpers; `FakeSMTP(t)`. |
| `web/` | React app; `web/web.go` embeds `web/dist` (SPA fallback, security headers). `src/api/schema.d.ts` is generated (`make gen-api`). |
| `deploy/`, `Dockerfile`, `Makefile` | distroless runtime image, compose (`upsera`, `postgres` under profile `local-db`, `docker-proxy`). The Caddy `https` profile is not written yet (Phase 8). |

### Contracts to keep stable

- `api.Runner` (`*scheduler.Scheduler`): `Upsert`, `Remove`, `Push`, `State`, `Health`.
- `api.Alerting` (`*alerting.Engine`): `UpsertMonitor`/`RemoveMonitor`, channels, policies, `Acknowledge`,
  `TestSend`. Every write to monitors, channels or policies must call the matching method.
- `api.MaintenanceRegistry`, `api.DockerRegistry` (`*dockerapi.Registry`: `Upsert`, `Remove`, `Client`),
  `api.StatusPageCache` (`InvalidatePage`, `InvalidateAll`, `ReloadDomains`; call after every page or
  incident write) and `api.PublicPages` (`Handler`, `HostMiddleware`, `TLSAllowed`) — the last two are
  implemented by `*statuspage.Service`.
- `statuspage.Store` (subset of `*store.Store`) and `statuspage.States` (`*scheduler.Scheduler`);
  `scheduler.Checker` / `scheduler.Store` / `scheduler.Maintenance`; `alerting.Store` / `alerting.StateSource`;
  `jobs.Store`; `notify.Notifier`.
- Nothing in the check or alert path may touch the DB synchronously (both keep working during a DB
  outage). The public status page may query the store (it is cached 30s) but its banner uses live state.

## 4. Decisions worth knowing

API / general (Phase 1):
- API tokens: read = viewer ceiling, write = editor ceiling; tokens can never manage members/tokens or
  use admin/auth/team-list endpoints. Non-member → 404, low role → 403. Creates return **201**.
- Login limiter in-memory, 10 failures per (RemoteAddr IP, email) per 15 min; ignores `X-Forwarded-For`.
- A request carrying a session cookie hits the DB for the session lookup, so it gets 503 during a DB outage.
- `interval_s` min 20, `timeout_s < interval_s`, retries 0–10 (default 1). Enum-like model types implement
  `huma.SchemaProvider`. Every write is audited via `writeAudit`.

Alerting (Phase 2):
- `heartbeats.status` and `monitor_state.status` store the resulting **state**. Rollup counts PENDING as up.
- Channel configs are encrypted at rest; changing `APP_SECRET` makes them undecryptable (API shows
  `config_error`). Responses redact secrets as `********`; a PUT sending `********` (or omitting it) keeps
  the stored secret. Routing: monitor's policy, else its `channel_ids`, else the team's defaults.
- Pausing a monitor moves it to MAINTENANCE and resolves its open alert silently. License stays MIT.

Dashboard (Phase 3): details in `docs/handover-phase3.md` (SSE hub, overview endpoint, embed, CSP).

Docker monitoring (Phase 4):
- `docker_hosts` rows hold name, endpoint (`tcp://`, `http(s)://`, `unix://`) and TLS material encrypted as
  one JSON blob. Monitor config is `{docker_host_id, container}`; the host must belong to the monitor's team.
- Check mapping: running and (no healthcheck or healthy) → UP; `starting` → fails (PENDING via retries);
  `unhealthy`, `exited (N)`, `restarting`, `paused`, `container not found`, host unreachable → DOWN with that
  message. Plain-TCP hosts are allowed (the UI warns); editors manage hosts.
- `unix://` endpoints can only be created through the API when they equal the server's `DOCKER_HOST`.
- The `local` host is seeded from `DOCKER_HOST` at `POST /api/setup` and, at startup, for the first team if it
  has no hosts (deleting all hosts re-seeds it on the next restart).
- The Docker checker bypasses the `DOCKER_HOST` block deliberately; a test proves ordinary HTTP monitors
  still cannot reach `docker-proxy:2375`.

Status pages (Phase 5):
- Public pages are server-rendered Go templates (no JS), strict CSP, `Referrer-Policy: same-origin` (a
  `no-referrer` page makes browsers send `Origin: null`, which the subscribe form would reject). Nothing
  internal is public: only the page title/description, section names, per-item `display_name` (defaults to the
  monitor name — the builder warns), incident titles/updates. Heartbeat messages and targets never appear.
- Custom domains: served when the request Host matches a published page's `custom_domain`
  (`HostMiddleware` wraps the whole router; only `/`, `/feed.xml`, `/subscribe`, `/confirm`, `/unsubscribe`
  exist there, everything else 404). `GET /api/tls-allowed` is for Caddy's on-demand TLS `ask`. **Only an
  instance admin session may set, change or remove a custom domain**; for editors/tokens an omitted field
  keeps the current value. Emails link to `https://<domain>/...` for such pages.
- Incidents: manual or auto-drafted when a monitor on a published page has had an open alert for
  `INCIDENT_DRAFT_AFTER` (default 5m); drafts are private until published (publishing queues email/webhook
  notices for confirmed subscribers). Auto incidents resolve when their alert recovers. Deleting an auto
  incident whose alert is still open only dismisses it (`incidents.dismissed`) so it is not drafted again.
- Subscribers: email (public, double opt-in via signed links, GET shows a button and only POST acts,
  rate limits per client key and per page+email, caps on confirmation mails 60/page/h and 300/instance/h,
  honeypot, Origin check) and webhook (editor-added only, URLs masked in the API). RSS is public.
  `TRUSTED_PROXIES` makes `X-Forwarded-For` count for the subscribe limiter; IPv6 clients are bucketed per /64.
- Delivery outbox (`subscriber_deliveries`): 10 attempts, backoff 30s doubling to 2h; `DeferDelivery`
  reschedules without using attempts (no SMTP configured → every 10 min; email circuit breaker after 3
  consecutive failures); `ExpireDeliveries` drops anything pending older than 24h keeping the last reason.
  Draft/resolve and delivery are separate loops (10s tick). Delivery is at-least-once (single instance only).
- Instance SMTP (admin only) is stored encrypted in `instance_settings.smtp`; `GET` shows the password as
  `********`, `PUT` omitted = keep, `null` = clear, object = set; `POST /api/admin/settings/smtp/test`.

## 5. Still open

From Phase 5 reviews (none blocking, all accepted):
- Auto-incident lifecycle: deleting a monitor leaves its published auto incident "investigating" forever
  (`auto_alert_id ON DELETE SET NULL`); alerts closed by maintenance are announced as "recovered"; reopening an
  auto incident by hand gets re-resolved and re-announced. Needs an `auto` marker or monitor id.
- No retention for resolved never-published auto drafts or dismissed rows; the admin incident list is capped at
  100 with no pagination. `last_error` of deliveries is not shown anywhere.
- Confirmation-mail caps are constants, not config. The email breaker also counts per-recipient rejections.
- `ClaimDeliveries` aborts the whole claimed batch if a row vanishes between claim and load.
- Web: SMTP password field append/autofill trap, "Remove SMTP" also saves pending general settings, status page
  lists go stale after a monitor delete, no confirm when creating a published incident.
- Server: CR-only smuggling in `buildMessage`, RSS text escaped once, custom-domain requests skip
  RequestID/Recoverer/access log, `ReloadDomains` ordering and context, settings save not atomic, loose `smtp`
  OpenAPI schema (two casts in `web/src/api/queries.ts`), `ReloadDomains` runs after scheduler start in `main.go`.

From earlier phases:
- [ ] `clientIP` for the login limiter ignores `X-Forwarded-For` (`TRUSTED_PROXIES` covers only subscribe).
- [ ] `block_private_targets` defaults to `false` (Docker hosts ignore it on purpose).
- [ ] Run CI on GitHub once.
- [ ] No automated test for the `HTTP(S)_PROXY` fix (Go never proxies loopback).
- [ ] Telegram/Slack acks could target another team's alert if its UUID were known (low risk).
- [ ] Phase 3 out-of-scope items: team rename/delete, audit/notification log UI, 7d latency chart, code splitting.

Not verified: Chrome/Firefox really send a real Origin from the `same-origin` page; real SMTP and Caddy TLS;
upgrading an existing database to migration 4.

## 6. Next phases (from the plan)

- **Phase 8 slice (suggested before more features):** `deploy/Caddyfile` + compose `https` profile
  (`on_demand_tls { ask http://upsera:3080/api/tls-allowed }`, set `TRUSTED_PROXIES` to the Caddy address),
  `/metrics`, README comparison table; then smoke a real certificate path.
- **Phase 6 Multi-location probes:** `upsera-probe` binary/image registering with a one-time token, pulling
  assignments and pushing results over HTTPS with local buffering; per-monitor regions + quorum; per-region
  latency chart; "probe silent" alert. `probe_id` already exists on heartbeats (the built-in checker is probe
  `local`).
- **Phase 7 SLOs and monitors as code:** SLO targets, error budget, burn-rate alerts, DEGRADED detection,
  `upsctl export|diff|apply`.
- Plan for each with `/plan` (ask about material trade-offs), and keep the review → fix → test → commit gates.
