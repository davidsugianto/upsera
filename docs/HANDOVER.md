# Upsera handover: Phase 1 done, Phase 2 next

Written 2026-09-27 for the next session. Read this first, then `docs/upsera-build-plan.html`
(v0.5, the product plan; its "Build phases" section is the roadmap).

## 1. State of the repo

- Phase 1 (Foundation) is complete and meets its "done when": monitors created via the REST
  API are checked and their heartbeats are stored.
- **Nothing is committed yet.** Everything except `LICENSE`, `.gitignore` and the initial
  README is untracked. Commit Phase 1 before starting Phase 2.
- ~7.5k lines of Go. `gofmt`, `go vet ./...` and `go test -race ./...` all pass.
- Module `github.com/davidsugianto/upsera`, `go 1.26` (built with go1.26.6). Go 1.26 rule:
  use `new(expr)` instead of pointer helpers like `intPtr`.

## 2. Running things

```sh
# Docker for integration tests (colima on this Mac; `colima start` if stopped)
export DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock

go vet ./... && go test -race -count=1 ./...   # ~25s, starts Postgres 17 containers
go test -short ./...                           # skips Docker-backed tests
```

Local server: see README "Run locally" (Postgres container, `DATABASE_URL`, `APP_SECRET`,
`go run ./cmd/upsera`, port 3080, API docs at `/api/docs`, first run via `POST /api/setup`).
CI: `.github/workflows/ci.yml` (gofmt, vet, build, `go test -race`), never run on GitHub yet.

## 3. Layout

| Path | What |
|---|---|
| `cmd/upsera/main.go` | Wiring: config → store (retrying connect) → migrate → load monitors/states → netpolicy → scheduler → jobs → HTTP. Subcommands `healthcheck`, `version`. Shutdown order: HTTP drain → scheduler stop + final flush → DB close. |
| `cmd/upsera/main_test.go` | End-to-end "done when" test through the real `run()`. |
| `internal/config` | Env config (all vars listed in README). `TZ` drives rollup days. |
| `internal/model` | Shared types. `Status` is int16 (0 down, 1 up, 2 pending, 3 maintenance) and JSON-encodes as `"up"` etc. `Role.AtLeast`. |
| `internal/store` | pgx store. **Every table is in schema `upsera` and every query is schema-qualified** (works through any Supabase pooler). Migrations embedded in `migrations/`, goose with advisory lock, version table `upsera.goose_db_version`. Sentinels: `ErrNotFound`, `ErrConflict`, `ErrSetupDone`, `ErrLastOwner`. |
| `internal/checker` | `ValidateConfig` (per-type config, defaults) + `Checker.Check`. http/keyword/tcp/ping (unprivileged ICMP via pro-bing)/dns. With a custom `resolver`, DNS sends one raw query over the policy dialer (no `/etc/hosts`). Push isn't dialed out to; a watchdog records DOWN when no push arrives within `interval_s`. |
| `internal/netpolicy` | Outbound target policy: always refuses link-local/metadata/unspecified and the `DOCKER_HOST` IPs; optionally private ranges. IP checked at dial time (anti DNS-rebinding). |
| `internal/scheduler` | In-memory monitor cache, one goroutine per monitor, semaphore cap, startup jitter (not on Upsert), bounded heartbeat buffer (drop-oldest), flusher (1s, 15s timeout, requeue on failure), push handling, `Health()`. |
| `internal/jobs` | Hourly: load settings → rollup → prune (only if rollup ok) → delete expired sessions. |
| `internal/api` | chi + huma v2. Session cookie + CSRF header, or `Bearer ups_...` team token. `/healthz` outside huma. |
| `internal/testutil` | `Store(t)` / `PostgresURL(t)` testcontainers helpers. |

### Contracts to keep stable

- `api.Runner` (implemented by `*scheduler.Scheduler`): `Upsert`, `Remove`, `Push`, `State`, `Health`.
  The API must call `Upsert`/`Remove` after every monitor write.
- `scheduler.Checker` / `scheduler.Store` interfaces; `jobs.Store`.
- Nothing in the check path may touch the DB (that's what keeps checks alive in a DB outage).

## 4. Phase 1 decisions worth knowing

- **Phase 1 `monitor_state.status` is the raw last check result.** No PENDING/retry state
  machine yet; retries only affect scheduling (retry interval while `0 < failures <= retries`).
  Phase 2 replaces this with the real state machine.
- Columns created early but not used yet: `monitor_state.flap_count`, `monitor_state.last_dedupe_key`,
  `probes.token_hash/last_seen_at/version`.
- Heartbeats status/latency: `latency_ms` is always set (measured time, even on failure);
  rollup averages/p95 use only UP rows; maintenance rows are excluded from `checks`; pending counts as up.
- HTTP `max_redirects` defaults to 10 (`*int`, so explicit 0 = don't follow).
- Push token: 24 random bytes base64url; push URL `BASE_URL/api/push/{token}`; 404 unknown, 409 paused.
- API tokens: read = viewer ceiling, write = editor ceiling; tokens can never manage members/tokens
  (even list) or use admin/auth/team-list endpoints. Non-member → 404, low role → 403.
- Login limiter: in-memory, 10 failures per (RemoteAddr IP, email) per 15 min → 429. Does not trust
  `X-Forwarded-For`.
- Creates return **201** (huma default). Responses include a `$schema` field (huma default).
- `interval_s` min 20, `timeout_s` must be `< interval_s`, retries 0–10.
- API tokens are soft-deleted (`api_tokens.revoked_at`) so audit entries keep `actor_token_id`.
- `PruneHeartbeats` rolls up every day it is about to delete (same tx), so late-flushed heartbeats
  below the rollup watermark are never lost. Prune cutoff is TZ-local midnight minus retention.
- Heartbeat messages are sanitized (invalid UTF-8 → U+FFFD, NUL stripped) in `model.TruncateMessage`
  and again in `store.InsertHeartbeats`.
- Enum-like model types (`Status`, `MonitorType`, `Role`, `TokenScope`) implement `huma.SchemaProvider`,
  so the spec lists their values. New Phase 2 enum types must do the same.
- Auth: a DB error while looking up a session/token → 503 (logged), not 401.

## 5. Bugs found and fixed at the end of Phase 1 (all have regression tests)

1. Checks aborted by Upsert/Remove/Shutdown were recorded as false DOWN heartbeats
   (`scheduler/worker.go`; test `TestSchedulerDiscardsChecksAbortedByUpsertOrShutdown`).
2. Periodic flush had no timeout during a DB outage (`scheduler/flusher.go`, `flushTimeout`).
3. `max_redirects` defaulted to 0 (redirects never followed).
4. Deleting a monitor while its heartbeats were buffered made COPY fail on the FK and the batch
   requeue forever, blocking all heartbeats (`store.InsertHeartbeats` now falls back to an
   insert that skips deleted monitors; store test "heartbeats for a deleted monitor…").

## 6. Phase 1 review (done 2026-09-27)

Confirmed and fixed, each with a regression test: auth DB error → 401 (now 503); login limiter map
never pruned; NUL/invalid UTF-8 heartbeat message blocked the flusher forever; Remove racing Upsert
leaked a check loop; rollup gap + prune lost days; URL (with query secrets) in HTTP error messages;
blocked-policy reason lost; ping DOWN when count × 1s > timeout; CNAME check UP without a CNAME;
custom DNS resolver bypassed by `/etc/hosts`; custom `Host` header ignored; DOCKER_HOST IP cache
cleared on lookup failure; token delete erased audit actor; OpenAPI enums missing; push `ping` < 0.

Verified correct: push/checks during a DB outage (real binary, zero loss, `/healthz` flips back),
no stale `monitor_state` after delete, name-only edit keeps `consecutive_failures`, cookie `Secure`
behind Caddy, CSRF/role/scope checks, audit coverage.

Still open:
- [ ] `clientIP` ignores `X-Forwarded-For`; behind Caddy all visitors share one limiter IP
      (needs a trusted-proxy setting).
- [ ] `block_private_targets` defaults to `false` (checks may reach loopback/private ranges).
- [ ] Rollup counts PENDING as up; revisit when Phase 2 adds a real PENDING state.
- [ ] Run CI on GitHub once (ping test depends on `net.ipv4.ping_group_range` on the runner).

## 7. Phase 2 (Alerting engine): scope from the plan

Plan text: state machine (UP / PENDING / DOWN / MAINTENANCE, DEGRADED reserved for phase 7),
event bus; dispatcher with per-channel queues, retries, dedupe, log; Telegram, Discord, Slack,
SMTP, generic webhook, test-send endpoint, cert-expiry events; escalation policies,
acknowledgement (web, Slack via Socket Mode, Telegram via long polling), dependency
suppression, flap damping.
**Done when:** an unacknowledged outage escalates from Telegram to email, and a DOWN parent
silences its children.

Constraints carried over from the plan:
- Alerts keep firing during a DB outage from cached channels; escalation timers, acks and
  dedupe keys live in memory and are written to `alerts` / `notification_log` when the DB is back.
- Channel secrets encrypted with AES-256-GCM, key from `APP_SECRET` via HKDF-SHA256, ciphertexts
  prefixed `v1:`.
- Webhook notifications go through `netpolicy` (same outbound policy as checks).
- Notifier interface (plan): `Type() string; Validate(cfg json.RawMessage) error; Send(ctx, cfg, ev Event) error`.
- Each channel: own queue, 3 attempts with backoff, every attempt logged.
- Only state *transitions* go on the event bus.

Suggested order:
1. Migration `00002_alerting.sql`: `notification_channels`, `escalation_policies` (+steps),
   `alerts`, `notification_log`; monitor columns `parent_id`, `escalation_policy_id`, and a
   monitor↔channel link table.
2. `internal/secret` (HKDF + AES-GCM, `v1:` prefix) with tests.
3. State machine in the scheduler: PENDING after first failure, DOWN after `retries`
   consecutive failures, recovery emits UP with downtime; MAINTENANCE for paused/windows;
   flap damping; persist via existing `monitor_state` columns. Emit transitions to an event bus.
4. `internal/notify`: Notifier interface, dispatcher (per-channel queue, retries, dedupe key per
   monitor+event, log), providers: telegram, discord, slack (webhook + app/Socket Mode), smtp, webhook.
5. `internal/alerting`: escalation timers, acks (API + Slack Socket Mode + Telegram getUpdates),
   dependency suppression via `parent_id`, cert-expiry events at 14/7/1 days from `tls_expires_at`.
6. API: channels CRUD + test-send, escalation policies CRUD, alerts list + acknowledge,
   monitor fields for parent / policy / channels. Audit all writes.
7. Tests: state machine table tests; dispatcher with fake notifiers; an e2e test for the
   done-when (fake Telegram/SMTP endpoints, fast escalation delay, parent DOWN silences child).

Open decisions to raise with the user before/while building Phase 2:
- Decision 5 in the plan (license: MIT is committed; AGPL recommended if a hosted version is likely).
- Whether Slack plain incoming webhooks (no ack buttons) and the Slack app mode both ship in Phase 2.
