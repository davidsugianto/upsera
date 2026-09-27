# Handover: Phase 3 (dashboard) → review, then Phase 4 (Docker monitoring)

Written 2026-09-27 at the end of the Phase 3 session. The product plan is
[`docs/upsera-build-plan.html`](upsera-build-plan.html); phase numbers below are the plan's.

## 1. Where things stand

| Phase | State |
|---|---|
| 1. Foundation | done, committed (`65f2ef8`) |
| 2. Alerting engine | done, committed (`fddd257`) |
| 3. Dashboard | **done, NOT committed**: everything is in the working tree on `master` |
| Local Docker (Dockerfile, compose, Makefile) | done, NOT committed (pulled forward from phase 8) |
| 4. Docker monitoring | not started |

First action next session: review (section 4), then commit Phase 3 as one commit
(`Phase 3: dashboard (...)`, same style as the earlier ones) before starting Phase 4.

CI has never run: its push trigger said `main` but the only branch is `master`. That is
fixed now, so the first push after committing is also the **first CI run** of both the `test`
and the new `web` job. Watch it.

## 2. What Phase 3 added

### Backend (Go)

- **`internal/events`**: an in-memory per-team hub for live events. `Publish` never blocks.
  A subscriber that falls 64 events behind is dropped, and its browser reconnects and refetches.
  `Publish` sends under the hub mutex with non-blocking `select`s, deliberately not the plan's
  "copy the set, then send", which could send on a closed channel.
- **Feeding the hub:**
  - `model.Transition.TeamID` is set at both scheduler emit sites (`scheduler/worker.go`).
  - `alerting.Options.OnAlert` is called from `markDirtyLocked`, the single choke point for
    alert changes, under the engine lock.
  - Monitor create, update and delete in `api/monitors.go` publish `events.MonitorsChanged`.
  - `cmd/upsera/main.go` wires all of these, and registers `srv.RegisterOnShutdown(hub.Close)`
    so open streams don't stall shutdown.
- **New endpoints:**
  - `internal/api/dashboard.go`:
    - `GET /api/teams/{t}/monitor-overview?beats=` (default 50, max 100) returns the last N
      heartbeats, oldest first, plus 24h and 30d uptime for every monitor;
    - `GET .../monitors/{id}/uptime-summary` returns 24h (raw heartbeats, exact) and 7d/30d
      (from `uptime_daily`, which lags by up to one rollup);
    - `GET .../monitors/{id}/events` returns status changes, newest first.
  - Store queries are in `internal/store/dashboard.go`.
  - The `/heartbeats` limit maximum was raised to 5000.
- **Live stream:** `internal/api/events.go` serves `GET /api/teams/{t}/events` as SSE, with
  events `monitor`, `alert` and `monitors_changed`.
  - It uses a plain `huma.Register` returning a `StreamResponse`, not `huma/sse`, so auth
    failures are real 401/403/404 responses.
  - A comment line is sent every 25s, and each stream is closed after 15 minutes so the browser
    reconnects and re-authenticates.
  - Only transitions are streamed, never steady-state checks (plan line 231).
- **Router:**
  - `NewRouter` now calls `newAPI`.
  - `api.OpenAPISpec(version)` backs the new `upsera openapi` subcommand.
  - The dashboard is mounted via `r.NotFound(d.UI.ServeHTTP)`, so every API route, `/healthz`
    and `/api/docs` win over it.
- **`web/web.go`:** embeds `web/dist` with `go:embed all:dist`.
  - Serves the SPA with history-mode fallback.
  - Sets CSP, nosniff, frame-deny and referrer headers on every response.
  - `/api/*` paths it doesn't know get a JSON 404; non-GET/HEAD methods get 405.
  - Without a build it serves a "not built" page. The committed `web/dist/README.md` keeps the
    embed pattern valid without Node.
- **`go.mod`:** has `ignore ./web/node_modules`, so npm's stray `.go` files stay out of `./...`
  (`go list ./... | grep node_modules` prints nothing).

### Frontend (`web/`)

**Stack:** React 19, Vite 8, TypeScript **5.9**, Tailwind 4, react-router 8, TanStack Query 5,
openapi-fetch and uPlot, managed with npm. TS 5.9 rather than 7 is forced by
`openapi-typescript` (peer `^5`) and `typescript-eslint` (`<6.1`).

- **`src/api/`:**
  - `schema.d.ts` is generated (`npm run gen:api`; CI fails if it is stale).
  - `client.ts` holds `ApiError`, the CSRF middleware, the 401 → signed-out handling and `unwrap`.
  - `queries.ts` holds every query key and hook.
  - `types.ts` holds aliases plus the SSE payload types. `Monitor.state` is overridden to be
    nullable there, because huma panics on `nullable` for struct pointers.
- **`src/lib/`:** pure logic with Vitest tests (23 tests):
  - `beats` (padding and uptime formatting);
  - `live` (applies SSE events to the cache);
  - `monitorForm` (emits only the config keys each type allows; the server rejects unknown
    fields);
  - `channelForm` (the `********` secret handling).
- **`src/hooks/useTeamEvents.ts`:** the single EventSource per team layout.
  - Patches caches on events and does a delayed 2s refetch of the overview and alert lists.
  - After a reconnect it refetches every query for the team.
  - On an HTTP error it re-checks `me` and reopens after 5s.
- **`src/components/`:** Layout (sidebar, team switcher, theme and logout), HeartbeatBar,
  StatusBadge, LatencyChart (uPlot), ConfirmDialog (native `<dialog>`), form Fields, and
  ErrorBanner. ErrorBanner shows on any 503 and retries the failed queries every 10s until the
  database is back.
- **`src/pages/`:** every route in plan section 9, including admin.
  - Write actions appear only for editors, and members/tokens management only for owners, via
    `useRole().can()`.
  - The pages were written by parallel sub-agents from the foundations. They are covered by the
    browser smoke test and lint/tsc, but have no component tests.

### Tooling / deploy (new this session)

- **`Dockerfile`:** node:24-alpine builds the web app, golang:1.26-alpine builds a static binary,
  and the runtime is `distroless/static:nonroot`. It has a `HEALTHCHECK` via
  `/upsera healthcheck`, and the version comes from `--build-arg VERSION`.
- **`deploy/docker-compose.yml`:** follows the plan's draft.
  - Services: `upsera`, `postgres` (profile `local-db`) and `docker-proxy`
    (tecnativa, read-only: `CONTAINERS=1`, `POST=0`).
  - It sets `DOCKER_HOST=tcp://docker-proxy:2375` and
    `sysctls: net.ipv4.ping_group_range` so ping works as non-root.
  - Caddy (`https` profile) is left for phase 8.
- **`deploy/.env.example`**, and `make env` generates `deploy/.env` with random secrets.
  `deploy/.env` is gitignored.
- **`Makefile`:** run `make help`. It auto-uses the colima socket for docker and testcontainers
  commands only; the server's own `DOCKER_HOST` is not touched. Written for GNU Make 3.81
  (macOS default).
- **CI:**
  - The push trigger branch is now `master`.
  - New `web` job: `npm ci`, check the API types are current, lint, test, build, then
    `go build` and `go test ./web/`.

## 3. How to run and verify

```sh
make up            # http://localhost:3080: the stack is running now with an empty DB (setup page)
make down          # stop; add `-v` via docker compose to wipe the DB volume

make test          # go test -race ./... (testcontainers; colima handled by the Makefile)
make lint test-web # gofmt/vet/eslint/tsc + vitest
make gen-api       # after any API change, then commit web/src/api/schema.d.ts
make dev           # Vite on :5173 proxying /api to a server on :3080
```

Verified at the end of this session:

- **Go:**
  - `go vet` and `go test -race -count=1 ./...` are green;
  - the new tests are `internal/events/hub_test.go`, `internal/store/dashboard_test.go`,
    `web/web_test.go` and `cmd/upsera/phase3_test.go`;
  - `TestPhase3LiveDashboard` covers the SSE events, overview, history, SPA fallback, and
    shutdown with an open stream.
- **Web:** `tsc`, eslint, vitest and `vite build` are green. The bundle is about 486 KB
  (154 KB gzipped) as a single chunk.
- **Browser smoke** (Playwright, throwaway, not committed), covering plan steps 1–8 in full:
  - setup;
  - live dot flip on a push `down` without reload;
  - open alert, then Acknowledge;
  - chart and uptime cards;
  - editing a webhook channel keeps its secret (Test showed the real send error, not a 422);
  - a two-step policy;
  - a maintenance window;
  - team create and switch;
  - viewer with hidden write actions and nav;
  - token shown once, then revoked;
  - Postgres stopped: live transitions continue and the banner shows; Postgres started again:
    the page recovers without a reload.
- **Compose:** `make up` gives all three containers `Up (healthy)`, `/healthz` reports db up,
  `/` serves the SPA, and a ping monitor to 127.0.0.1 is UP inside the non-root container.
  After that check the DB volume was wiped, so you get a fresh setup page.

## 4. Phase 3 review checklist (for the next session)

Worth a careful look, roughly in risk order:

1. **Sub-agent pages** (`web/src/pages/**`, apart from `HomePage`, `TeamLayout` and
   `NotFoundPage`): consistency, error handling on every mutation, empty states, and form
   validation that matches the server. Small bugs already found and fixed: the policy delay
   field's `step` attribute, missing card padding, the Revoke button label, and set-state in an
   effect in the monitor form.
2. **Alert list vs engine lag:** `GET /alerts` reads Postgres, but the engine flushes dirty
   alerts about 1s later. The client papers over this with a 2s trailing refetch after any
   `alert` event. A server-side fix (serve open alerts from the engine, or flush before reading)
   would be cleaner.
3. **SSE session revocation:** a stream authorises once, then lives up to 15 minutes. A logged-out
   or removed member keeps getting that team's events until then. Acceptable for now; revisit if
   status pages or public SSE appear.
4. **Schema accuracy:**
   - `AlertBody.ack_source` is `""` when unacked, but the enum lacks `""` (predates Phase 3).
   - `MonitorBody.state` is nullable in practice; see the note on `types.ts` above.
5. **Uptime semantics:** 7d/30d come from `uptime_daily` (hourly rollup, in the server's TZ) and
   24h from raw heartbeats, so they can disagree slightly for up to one hour.
6. **Hub delivery:** a slow client is dropped after 64 buffered events. Check that the reconnect
   and refetch path feels fine under a flapping storm.
7. **Security headers and CSP** in `web/web.go`: `style-src 'unsafe-inline'` is needed by uPlot
   and inline style attributes.
8. **The Docker bits added this session:**
   - image size;
   - that the `docker-proxy` socket mount works on your Docker host (it does on colima);
   - whether you want `deploy/` (plan layout) or compose at the repo root.
9. **Still open from Phase 2**, deliberately out of scope here: the cross-team check on
   Telegram/Slack acks, and trusted-proxy handling of `X-Forwarded-For` (currently ignored).
10. **Out of scope for Phase 3, per the plan:** team rename/delete, audit log UI, notification log
    UI, a 7d latency chart and code splitting.

## 5. Phase 4: Docker monitoring

Plan text:

- Docker host config (socket proxy URL, or a remote host over TCP / TLS);
- container picker in the UI, with checks for running state and health status;
- messages like "exited (137)" or "unhealthy" surfaced in alerts.

**Done when:** `docker stop` on a watched container triggers an alert.

Data-model note from the plan (line 281): `docker_hosts` has team, name, endpoint (socket proxy
URL, or TCP with TLS) and encrypted TLS material. The compose `DOCKER_HOST` is seeded as the
first team's host at setup.

### Suggested design (to confirm at the start of the session)

- **Migration `00003_docker.sql`:**
  - `upsera.docker_hosts`: `id`, `team_id`, `name`, `endpoint`, `tls_ca`/`tls_cert`/`tls_key`
    (ciphertext via the existing `secret` box, `v1:` prefix), and timestamps; unique
    `(team_id, name)`.
  - Allow monitor type `docker` in whatever CHECK or enum the monitors table uses. Check
    migration 00001.
- **Model:**
  - add `model.TypeDocker = "docker"` to `MonitorTypes`;
  - add a `DockerHost` struct;
  - monitor config becomes `{docker_host_id, container}` (name or id), parsed in
    `checker/config.go` with strict decoding like the other types.
- **Checker** (`internal/checker/docker.go`):
  - Talk to the Docker Engine API with plain `net/http`, no SDK: `GET /v1.43/containers/{c}/json`.
  - Mapping:
    - running and (no healthcheck, or `healthy`) → UP;
    - `starting` → PENDING, or stay UP; decide;
    - `unhealthy` → DOWN with message "unhealthy";
    - not running → DOWN with message `exited (<ExitCode>)` or the state name;
    - 404 → DOWN "container not found".
  - The checker needs the host's endpoint and TLS config: inject a `DockerHosts` registry (like
    `maintenance.Registry`), loaded at startup and kept in sync by the API.
  - TCP+TLS uses `crypto/tls` with the decrypted CA and client cert/key.
- **Target policy:** `netpolicy` always refuses the `DOCKER_HOST` address for ordinary checks.
  The Docker checker must bypass that on purpose, only for configured Docker hosts. Keep a
  test proving an HTTP monitor still can't reach `docker-proxy:2375`.
- **Seeding:** at `POST /api/setup`, if `cfg.DockerHost` is set, create a host named "local"
  for the first team. Decide whether existing instances (already set up) get it on upgrade.
- **API:**
  - CRUD `/api/teams/{t}/docker-hosts`: editor to write, viewer to read; secrets redacted
    exactly like channels.
  - `POST .../docker-hosts/{id}/test` pings `/_ping`.
  - `GET .../docker-hosts/{id}/containers` (`/containers/json?all=1`: name, image, state,
    status) feeds the picker. Audit-log the writes.
- **Web:**
  - a "Docker hosts" page and form (endpoint plus optional TLS PEM textareas, secrets as
    `********`);
  - `docker` in `lib/monitorForm.ts` (`configFor`, `monitorToForm`, `targetSummary`) plus tests;
  - a container picker in `MonitorFormPage` (host select, then a searchable container list);
  - a nav entry;
  - run `make gen-api`.
- **Tests:**
  - checker unit tests against an `httptest` fake Engine API (running, exited 137, unhealthy,
    404, TLS);
  - store tests for `docker_hosts`;
  - API tests for redaction and the picker;
  - a `cmd/upsera/phase4_test.go` done-when test: a fake Engine API whose container flips to
    exited produces an `alert` event with "exited (137)".
  - Then smoke it for real: `make up`, run `docker run -d --name demo nginx`, watch it via the
    `local` host, `docker stop demo`, and see the alert in the dashboard.
- **Proxy:** tecnativa's `CONTAINERS=1` allows `GET /containers/*`; `/_ping` and `/version` are
  allowed by default. Keep `POST=0`.

### Open questions for Phase 4

- Should a container whose health is `starting` be PENDING or UP?
- Remote hosts over plain TCP without TLS: allow them (homelab), or require TLS unless the host
  is private?
- Who may manage Docker hosts: editors or owners? Channels use editors.
- Seed the "local" host on upgrade for existing instances, or only at first setup?

## 6. Session hygiene notes

- **Colima:** set `DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock` and
  `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock`. The Makefile sets both for
  `make test` and the compose targets.
- **Outbound HTTPS here:** `example.com` answered HTTP 403 from this machine during smoke tests,
  so don't read that as an app bug.
- **Browser automation:** the harness browser tool is in relay mode with no extension, so the
  smoke test used a throwaway Playwright install in `/tmp` (now deleted). Reinstall with
  `npm i playwright && npx playwright install chromium` in a temp dir if needed.
- **IPython `eval` hazard:** don't write source files with the IPython `eval` tool. It rewrites
  lines like `x = !y` as shell escapes. Use the write/edit tools instead.
