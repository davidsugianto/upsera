# Upsera handover: Phases 1–2 done, Phase 3 (Dashboard) next

Updated 2026-09-27 for the next session. Read this first, then `docs/upsera-build-plan.html`
(v0.5, the product plan; its "Build phases" section is the roadmap).

## 1. State of the repo

- **Phase 1 (Foundation)** is committed (`65f2ef8`). Done when: monitors created via the REST
  API are checked and their heartbeats are stored (`cmd/upsera` `TestPhase1DoneWhen`).
- **Phase 2 (Alerting engine)** is complete but **not committed yet** — commit it before starting
  Phase 3. Done when: an unacknowledged outage escalates from Telegram to email, and a DOWN
  parent silences its children (`cmd/upsera` `TestPhase2DoneWhen`, real `run()` with fake
  Telegram + SMTP). A full bug review was done afterwards (§6).
- ~16k lines of Go. `gofmt -l .` empty, `go vet ./...` clean, `go test -race -count=1 ./...` passes
  (13 packages, ~40s).
- Module `github.com/davidsugianto/upsera`, `go 1.26` (built with go1.26.6). Go 1.26 rule:
  use `new(expr)` instead of pointer helpers like `intPtr`.
- No frontend yet: no `web/` directory, no Node toolchain in the repo.

## 2. Running things

```sh
# Docker for integration tests (colima on this Mac; `colima start` if stopped)
export DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock

go vet ./... && go test -race -count=1 ./...   # ~40s, starts Postgres 17 containers
go test -short ./...                           # skips Docker-backed tests
```

Local server: README "Run locally" (Postgres container, `DATABASE_URL`, `APP_SECRET`,
`go run ./cmd/upsera`, port 3080, API docs at `/api/docs`, first run via `POST /api/setup`).
For local alerting tests point `TELEGRAM_API_URL` / `SLACK_API_URL` at a fake (a tiny python
`http.server` that logs POST bodies is enough). CI: `.github/workflows/ci.yml` (gofmt, vet, build,
`go test -race`), never run on GitHub yet.

## 3. Layout

| Path | What |
|---|---|
| `cmd/upsera/main.go` | Wiring: config → store (retrying connect) → migrate → load monitors/states/channels/policies/windows/open alerts/cert keys → secret box → netpolicy → maintenance registry → alerting engine → scheduler → jobs → HTTP. **Start order: `sched.Start` then `engine.Start`** (the engine reconciles open alerts against the scheduler's live states). Shutdown: HTTP drain → scheduler stop + flush → engine stop (drain transitions, deliver queued sends, final flush with its own 15s) → jobs → DB close. |
| `cmd/upsera/main_test.go`, `phase2_test.go` | End-to-end "done when" tests through the real `run()`; helpers `startServer`, `setupAdmin`, fake Telegram. |
| `internal/config` | Env config (all vars in README). `TZ` drives rollup days and recurring maintenance windows. `TELEGRAM_API_URL`, `SLACK_API_URL` overridable. |
| `internal/model` | Shared types. `Status` int16 (0 down, 1 up, 2 pending, 3 maintenance), JSON `"up"` etc. `alerting.go`: Channel, EscalationPolicy, Alert, AlertEvent, AckSource, Recurrence, MaintenanceWindow, Transition. |
| `internal/secret` | `Box`: AES-256-GCM, key = HKDF-SHA256(`APP_SECRET`), ciphertext `v1:` + base64url. Encrypts channel configs. |
| `internal/store` | pgx store. **Every table in schema `upsera`, every query schema-qualified.** Migrations `00001_foundation.sql` (committed — never edit), `00002_alerting.sql`. Sentinels: `ErrNotFound`, `ErrConflict`, `ErrInUse` (FK restrict on delete), `ErrSetupDone`, `ErrLastOwner`. `SetSecretBox` must be called before channel reads/writes (testutil does it). |
| `internal/checker` | Per-type config validation + checks: http/keyword/tcp/ping/dns; push has a watchdog. |
| `internal/netpolicy` | Outbound target policy checked at dial time; used by checks **and** every notifier (Telegram/Slack API, webhooks, SMTP, Slack websocket). Notifier HTTP clients never use `HTTP(S)_PROXY`. |
| `internal/scheduler` | In-memory monitor cache, goroutine per monitor, heartbeat buffer + flusher. `state.go`: `nextState` (UP / PENDING after a failure / DOWN after `retries` failures, Since of DOWN = first failure). Flap damping (≥5 DOWN/UP changes in 1h). Maintenance windows and pause → MAINTENANCE (no checks). Emits `model.Transition` via `Options.OnTransition` **under its lock, in order** (callback must not block). |
| `internal/maintenance` | `ActiveAt` (none/daily/weekly, DST-safe wall-clock recurrence) and `Registry` cache consulted by the scheduler. |
| `internal/notify` | `Notifier` per channel type (telegram, discord, slack webhook, slack_app, smtp, webhook), `Title`/`Body` formatting, `Dispatcher` (per-channel queue of 1000, 3 attempts, backoff 1s/5s, every attempt reported; idle workers retire), `AckListeners` (Telegram getUpdates long-poll, Slack Socket Mode) with chat/channel allow-lists. `SecretFields`, `Redacted`. |
| `internal/alerting` | `Engine`: transitions → alerts (one open per monitor, UUIDv7), escalation steps via policy or monitor channels or team defaults, acks (web/Telegram/Slack), dependency suppression via `parent_id` (reconciled on parent maintenance/delete/re-parent), flap handling, cert warnings at 14/7/1 days, outbox writing alerts then notification log (survives DB outage). |
| `internal/jobs` | Hourly: settings → rollup → prune heartbeats → delete expired sessions → prune notification log (30 days). |
| `internal/api` | chi + huma v2. Session cookie + CSRF header, or `Bearer ups_...` team token. Phase 2 routes: `/channels` (+`/test`), `/escalation-policies`, `/alerts` (+`/acknowledge`), `/notification-log`, `/maintenance-windows`; monitor fields `parent_id`, `escalation_policy_id`, `channel_ids`, state `flap_count`. |
| `internal/testutil` | `Store(t)` / `PostgresURL(t)` testcontainers helpers; `FakeSMTP(t)`. |

### Contracts to keep stable

- `api.Runner` (`*scheduler.Scheduler`): `Upsert`, `Remove`, `Push`, `State`, `Health`. The API
  calls `Upsert`/`Remove` after every monitor write.
- `api.Alerting` (`*alerting.Engine`): `UpsertMonitor`/`RemoveMonitor`, `UpsertChannel`/`RemoveChannel`,
  `UpsertPolicy`/`RemovePolicy`, `Acknowledge`, `TestSend`. Every write to monitors, channels or
  policies must call the matching method, or the engine's cache goes stale.
- `api.MaintenanceRegistry` (`*maintenance.Registry`): `Upsert`/`Remove` after window writes.
- `scheduler.Checker` / `scheduler.Store` / `scheduler.Maintenance`; `alerting.Store` /
  `alerting.StateSource`; `jobs.Store`; `notify.Notifier`.
- Nothing in the check or alert path may touch the DB synchronously (both keep working during
  a DB outage; verified on the real binary).

## 4. Decisions worth knowing

API / general (Phase 1):
- API tokens: read = viewer ceiling, write = editor ceiling; tokens can never manage members/tokens
  or use admin/auth/team-list endpoints. Non-member → 404, low role → 403. Soft-deleted.
- Creates return **201**; responses include `$schema` (huma default). Auth DB error → 503.
- Login limiter in-memory, 10 failures per (RemoteAddr IP, email) per 15 min; ignores `X-Forwarded-For`.
- A request carrying a session cookie hits the DB for the session lookup, so during a DB outage it
  gets 503 even on `/api/push/...`; cookie-less pushes keep working. Keep this in mind for the
  dashboard (SSE / polling will 503 while the DB is down).
- `interval_s` min 20, `timeout_s < interval_s`, retries 0–10 (default 1). HTTP `max_redirects`
  default 10. Push URL `BASE_URL/api/push/{token}`: 404 unknown, 409 paused.
- Enum-like model types implement `huma.SchemaProvider` (spec lists values; `TestOpenAPIEnums`).
- Every write is audited via `writeAudit`; channel audit details never include config.

Alerting (Phase 2):
- `heartbeats.status` and `monitor_state.status` store the resulting **state**, not the raw check
  result. Rollup still counts PENDING as up (decision: unchanged).
- Channel configs are encrypted at rest; changing `APP_SECRET` makes them undecryptable (API shows
  `config_error`, sends fail with "channel config unavailable"). API responses redact secret fields
  as `********`; a PUT sending `********` (or omitting the field) keeps the stored secret.
- Channel types: `telegram`, `discord`, `slack` (incoming webhook, no buttons), `slack_app`
  (bot + app token, Socket Mode, Acknowledge button), `smtp`, `webhook`.
- Routing: monitor's escalation policy if set, else its `channel_ids`, else the team's
  `is_default` channels. Policy: 1–10 steps, `delay_s` 10–86400 (default 600).
- Hard-coded: flapping = 5 changes/hour; cert warnings 14/7/1 days (scan every 10 min, dedupe keys
  persisted in `notification_log`); notification log kept 30 days.
- Telegram/Slack acks are accepted from anyone who can press the button in a configured
  chat/channel (by id, or `@username` / `#name`); no mapping to Upsera users.
- Pausing a monitor moves it to MAINTENANCE and resolves its open alert silently
  (`resolution = "maintenance"`). Acknowledging a resolved alert → 409 "alert is resolved".
- `block_private_targets` stays default `false`. License stays MIT.

## 5. Phase 1 bugs and review (summary)

Fixed with regression tests: false DOWN from aborted checks; flush without timeout; `max_redirects`
0; deleted monitor blocking COPY forever; auth DB error → 401; limiter map growth; NUL/invalid UTF-8
blocking the flusher; Remove racing Upsert; rollup gap + prune losing days; secrets in HTTP error
URLs; DNS resolver bypassed by `/etc/hosts`; custom `Host` ignored; token delete erasing audit actor.

## 6. Phase 2 review (done 2026-09-27)

Fixed, most with regression tests: engine started before scheduler states were seeded (startup
reconciliation never ran — verified fixed on the real binary); children suppressed forever when a
DOWN parent entered maintenance / was paused / deleted / detached; flapping ending on PENDING
silenced the alert; flap end re-notified acked/suppressed alerts; transitions emitted out of order
under concurrent pushes; a push racing a pause left a paused monitor DOWN; deleted channel left
monitors without fallback to defaults; `HTTP(S)_PROXY` bypassed netpolicy; Telegram poller wedged on
>64 KiB batches (now `limit=20`); name-based chat/channel acks dropped; shutdown flush losing alerts;
SMTP ignoring ctx; dispatcher workers never reclaimed; resolved alerts leaking when the log insert
failed; concurrent channel edits applying a stale ack-listener sync.

Still open:
- [ ] `clientIP` ignores `X-Forwarded-For` (needs a trusted-proxy setting).
- [ ] `block_private_targets` defaults to `false`.
- [ ] Run CI on GitHub once (ping test depends on `net.ipv4.ping_group_range`).
- [ ] No automated test for the proxy fix (Go never proxies loopback, so a local test can't show it).
- [ ] Acks from Telegram/Slack could target another team's alert if its UUID were known (buttons
      only carry ids we sent, so low risk); consider checking the channel's team.

## 7. Phase 3 (Dashboard): scope from the plan

Plan text:
- Login, team switcher, monitor list with heartbeat bars, add / edit forms per monitor type.
- Monitor detail: latency chart, uptime 24h / 7d / 30d, event history, open alerts with Acknowledge.
- Channels, escalation policies, maintenance windows, members and tokens, SSE live updates.
**Done when:** everything above is manageable from the browser.

Stack fixed by the plan: **React + Vite + TypeScript + Tailwind** in `web/`, built in a Docker
stage and **embedded into the binary via `go:embed`** (no separate web container); live updates via
**Server-Sent Events**. Packaging (Dockerfile, compose) is Phase 8, but the embed must work now.

Things to decide / build:
1. `web/` scaffold and how `go build` works without Node (e.g. commit a placeholder `web/dist`
   or a build tag); serve the SPA with history fallback outside `/api`, keep `/healthz`.
2. Existing API covers most forms: `GET/POST /api/setup`, `POST /api/auth/login|logout`,
   `GET /api/auth/me` (returns `csrf_token`), `/api/teams`, and per team `members`, `tokens`,
   `audit`, `monitors` (+`/heartbeats`, `/uptime`), `channels` (+`/test`), `escalation-policies`,
   `alerts` (+`/acknowledge`), `notification-log`, `maintenance-windows`; admin `users`/`settings`.
   There is no team rename/delete endpoint. Check what's missing: e.g. heartbeat bars for many monitors in one call, latency series
   for the chart, event history (state changes — may need an endpoint over heartbeats/alerts).
3. SSE endpoint (`/api/teams/{id}/events`?) fed by the scheduler transitions / heartbeats and
   alert changes (the engine's `Publish` path and a fan-out hub; must not block the scheduler, which
   calls `OnTransition` under its lock). Session auth, per-team filtering.
4. Secret fields in channel forms: show `********`, send it back unchanged to keep the secret.
5. Tests: API additions as usual; a browser smoke test of the done-when (`browser` tool) at the end.
