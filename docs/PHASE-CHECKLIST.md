# Upsera phase checklist

Task list for every phase of the build plan ([`upsera-build-plan.html`](upsera-build-plan.html), "Build
phases"), checked against what is in the repo. Status as of 2026-10-19 (Phase 7 in the working tree on top of `b2cf403`).
Read [`HANDOVER.md`](HANDOVER.md) for how things work and what is still open; this file is only the
checklist. Update it in the same commit that finishes a task.

Legend: `[x]` done and tested · `[~]` partly done (note says what is missing) · `[ ]` not started.

| Phase | Name | Status | Commit |
|---|---|---|---|
| 1 | Foundation | done | `65f2ef8` |
| 2 | Alerting engine | done | `fddd257` |
| 3 | Dashboard | done | `1d16565` |
| 4 | Docker monitoring | done | `9b37dc9` |
| 5 | Status pages | done | `a0b8ea2` |
| 6 | Multi-location probes | done | `ffa41e3` |
| 7 | SLOs and monitors as code | done | see `git log` |
| 8 | Packaging and launch | partly done (Docker, Caddy, `/metrics`, backup notes) | `1d16565` + packaging slice |

---

## Phase 1: Foundation — done

- [x] Repo layout, config from env, Postgres via pgx + goose migrations, structured logging
- [x] Teams, users, roles, sessions, scoped API tokens, audit log
- [x] Scheduler with jittered start, worker cap, graceful shutdown
- [x] In-memory config cache and heartbeat write buffer, so checks survive a database outage
- [x] Daily rollup into `uptime_daily` and retention pruning of raw heartbeats
- [~] Integration tests against a real Postgres container (testcontainers), run in CI from the first commit
  — tests exist and pass locally; the CI workflow has never run on GitHub
- [x] HTTP, keyword, TCP, ping, DNS and push checkers, unit-tested against local test servers
- [x] TLS certificate expiry on HTTPS monitors (14 / 7 / 1 day warnings come from the alerting engine)
- [x] REST API with OpenAPI spec generated from the handlers

**Done when:** monitors are created via the API and store heartbeats — `TestPhase1DoneWhen`. ✔

## Phase 2: Alerting engine — done

- [x] State machine (UP / PENDING / DOWN / MAINTENANCE; DEGRADED added in Phase 7), event bus
- [x] Dispatcher with per-channel queues, retries, dedupe, log
- [x] Telegram, Discord, Slack, SMTP, generic webhook, plus test-send endpoint and cert-expiry events
- [x] Escalation policies
- [x] Acknowledgement from the web, Slack (Socket Mode) and Telegram (long polling) buttons
- [x] Dependency suppression (parent DOWN silences children)
- [x] Flap damping
- [x] Maintenance windows (none / daily / weekly)

**Done when:** an unacknowledged outage escalates from Telegram to email, and a DOWN parent silences its
children — `TestPhase2DoneWhen`. ✔

## Phase 3: Dashboard — done

- [x] Login, team switcher
- [x] Monitor list with heartbeat bars; add / edit forms per monitor type
- [x] Monitor detail: latency chart, uptime 24h / 7d / 30d, event history, open alerts with Acknowledge
- [x] Channels, escalation policies, maintenance windows, members and tokens
- [x] SSE live updates
- [x] Embedded in the server binary (`go:embed`), dark theme by default with the brand pink
- [ ] Not in the plan but still missing: team rename / delete, audit log and notification log UI, 7-day latency
  chart, code splitting (see `HANDOVER.md` §5)

**Done when:** everything above is manageable from the browser — `TestPhase3LiveDashboard` plus a browser
smoke test at the time. ✔

## Phase 4: Docker monitoring — done

- [x] Docker host config (socket proxy URL, or a remote host over TCP / TLS; unix socket for `DOCKER_HOST`)
- [x] Container picker in the UI; checks for running state and health status
- [x] Messages like `exited (137)` or `unhealthy` surfaced in alerts
- [x] `local` host seeded from `DOCKER_HOST` at setup and at startup
- [x] Docker checker allowed to reach its host while ordinary checks still cannot (netpolicy test)

**Done when:** `docker stop` on a watched container triggers an alert — `TestPhase4DoneWhen` and a real
`docker stop` through the socket proxy. ✔

## Phase 5: Status pages — done

- [x] Page builder: sections, monitor selection, public names, branding (title, description, https logo, theme)
- [x] Public page with overall banner, 90-day bars, incident timeline; responses cached 30s
- [x] Custom domains: page served when the request Host equals `custom_domain`; `GET /api/tls-allowed`
  returns 200 only for published pages (admin-only to set)
- [x] The Caddy profile that issues certificates with on-demand TLS (Phase 8 slice, `deploy/Caddyfile`)
- [x] Incidents auto-drafted when a listed monitor is down for `INCIDENT_DRAFT_AFTER` (default 5 min), published
  after review
- [x] Subscribers by email (instance SMTP, double opt-in), webhook (editor-added) and RSS
- [x] Instance SMTP settings in admin, with test send

**Done when:** two public pages render with different monitors, and a subscriber gets the incident email —
`TestPhase5DoneWhen`. ✔ (Not yet verified: a real browser, real SMTP, real Caddy TLS.)

## Phase 6: Multi-location probes — done

- [x] `upsera-probe` binary (`cmd/upsera-probe`) and `probe` Docker target (`make docker-build-probe`; the
  `upsera/upsera-probe` name is not published anywhere yet)
- [x] Registration with a one-time token (24h, single use), exchanged for a credential kept in `/data`
- [x] Probes pull assignments (every 15s, ETag) and push results over HTTPS, with an in-memory buffer if the
  server is unreachable
- [x] Per-monitor region selection and quorum (DOWN only when failing regions ≥ quorum; stale results
  older than 2× the interval excluded; PENDING + "probe silent" when too few fresh regions)
- [x] Per-region latency chart in the dashboard
- [x] Alert when a probe goes silent (and when it reports again)
- [x] Probe management UI (Administration → Probes, instance admin) and `probes` table use (`local` probe row
  and `heartbeats.probe_id` already existed)
- [ ] Not verified: a real multi-VPS deployment over public HTTPS; the orchestrator's `make check` and the
  phase 6 end-to-end test result are to be recorded here when run

**Done when:** firewalling the target from one probe's region fires no alert, from two regions fires DOWN,
and stopping one probe fires "probe silent", not DOWN.

## Phase 7: SLOs and monitors as code — done

- [x] SLO targets, error budget view (monitor form, error budget card with burndown, `GET/PUT/DELETE .../monitors/{id}/slo`,
  `GET .../slos`)
- [x] Multi-window burn-rate notifications: fast (1 h + 5 min) and slow (6 h + 30 min), notification only, state kept across restarts
- [x] Monthly SLA report (Reports page, CSV, print layout, `GET .../reports/sla?month=`)
- [x] DEGRADED detection from a rolling per-region p95 baseline (>3× the 7-day baseline over 15 minutes, back at ≤2×; `degraded` /
  `latency_recovered` notifications; status page, `/metrics` and web know the state)
- [x] `upsctl` CLI (`cmd/upsctl`, cobra): `export`, `diff`, `apply [--prune]` for monitors (with SLOs), channels, policies and status pages
- [x] `export` writes channel secrets as `${ENV_VAR}` placeholders; `apply` resolves them from the environment
- [x] Example GitLab CI job (`deploy/examples/upsctl.gitlab-ci.yml`, only YAML-linted; it cannot run here)
- [ ] Not verified: the new pages in a real browser, `upsctl` against a deployed instance, the DEGRADED baseline query on a large
  heartbeat table

**Done when:** `upsctl apply` from CI creates the same setup on a fresh instance — `TestPhase7UpsctlDoneWhen`. ✔
(also `TestPhase7DegradedDoneWhen` and `TestPhase7SLODoneWhen`)

## Phase 8: Packaging and launch — partly done (pulled forward in Phase 3)

- [x] Multi-stage Dockerfile (node build, go build, distroless runtime, non-root)
- [x] `time/tzdata` embedded so `TZ` works; ping uses unprivileged ICMP sockets (compose sets
  `net.ipv4.ping_group_range`)
- [x] `deploy/docker-compose.yml` with the `local-db` profile, `.env.example` (`make env` generates `.env`),
  container healthcheck (`/upsera healthcheck`)
- [x] Backup notes (database + `APP_SECRET`): `docs/BACKUP.md`
- [x] Optional Caddy profile for HTTPS (`deploy/Caddyfile`, `on_demand_tls ask → /api/tls-allowed`,
  `TRUSTED_PROXIES` on a fixed compose subnet). Smoke-tested locally with Caddy's local CA; a real
  Let's Encrypt certificate is not verified
- [x] `/metrics` Prometheus endpoint (bearer API token, any scope, only the token's team's monitors)
- [ ] Docs site
- [~] README with a comparison table and screenshots — table done (competitor claims written from memory,
  worth a check before launch); screenshots missing
- [ ] Fresh server to working dashboard in under 5 minutes, verified on a clean host

**Done when:** fresh server to working dashboard in under 5 minutes.

---

## Cross-cutting and release checklist

- [ ] Push and watch the first GitHub CI run (`test` and `web` jobs); fix anything it finds (the ping test
  depends on `net.ipv4.ping_group_range`)
- [ ] Decide the licence before accepting outside contributions: MIT (current) or AGPL-3.0
- [ ] Public v0.x release after Phase 5 (plan decision 6): the Phase 8 slice is in; still needs README
  screenshots and a real-host check of the Caddy certificate path
- [ ] Reserve the GitHub org, domain and Docker Hub namespace (plan decision 7)
- [ ] v1.0 after Phase 8
- [ ] Open review items to schedule: see `HANDOVER.md` §5 (auto-incident lifecycle, retention, claim batch,
  login limiter proxy trust, and the others)

## After v1 (from the plan, not scheduled)

- [ ] Kubernetes monitor (ready replicas, crash-looping pods, in-cluster service account)
- [ ] High availability: several replicas sharing monitors through Postgres advisory locks
- [ ] OIDC login (Google, GitHub), Terraform provider, more notifiers (ntfy, Gotify, Teams, PagerDuty native)
- [ ] More monitor types: gRPC health, MQTT, database connection checks
