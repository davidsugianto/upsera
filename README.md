# upsera
Self-hosted uptime monitoring and status pages for teams. Multi-region checks, fewer false alarms.

The product plan lives in [`docs/upsera-build-plan.html`](docs/upsera-build-plan.html).

## Status

Phase 1 (foundation) is done: teams, users and roles, sessions, scoped API tokens, audit log,
HTTP / keyword / TCP / ping / DNS / push checks with TLS expiry, a scheduler that keeps
checking through a database outage, daily uptime rollup and retention pruning, and a REST API
with a generated OpenAPI spec. Phase 2 (alerting) added notification channels, escalation
policies, acknowledgements, monitor dependencies, flap damping and maintenance windows.
Phase 3 (dashboard) adds a browser dashboard embedded in the server binary, with live updates
over Server-Sent Events. Phase 4 adds Docker container monitoring through a socket proxy.
Phase 5 adds public status pages at `/status/<slug>` (optionally on a custom domain), incidents
auto-drafted from outages, and email, webhook and RSS subscribers. Subscriber email needs the
instance SMTP server (Administration → settings). Phase 6 adds [multi-location probes](#multi-location-probes):
remote `upsera-probe` agents, per-monitor regions with a quorum, and a "probe silent" alert. Packaging adds
an optional Caddy HTTPS profile, Prometheus `/metrics` and backup notes.

## Run with Docker Compose

Needs Docker with Compose ≥ 2.20 (colima works).

```sh
make up      # writes deploy/.env with random secrets if missing, builds the image, starts the stack
make logs    # follow the server logs
make down    # stop (the Postgres volume is kept)
```

Then open <http://localhost:3080/>. The stack (`deploy/docker-compose.yml`) runs the server, a
Postgres 17 container (the `local-db` profile) and a read-only Docker socket proxy. To use
Supabase instead, set its `DATABASE_URL` in `deploy/.env` (see `deploy/.env.example`) and run
`docker compose -f deploy/docker-compose.yml up -d --build` without `--profile local-db`.
To start over with an empty database: `docker compose -f deploy/docker-compose.yml --profile local-db down -v`.

`make help` lists every task (build, test, lint, gen-api, ...).

### HTTPS with Caddy

The optional `https` profile puts Caddy in front of Upsera on ports 80 and 443 and gets certificates
automatically. Point your domain's DNS at the server, then in `deploy/.env`:

```sh
UPSERA_DOMAIN=status.example.com
BASE_URL=https://status.example.com
TRUSTED_PROXIES=172.29.0.0/24     # the compose network Caddy runs in
```

```sh
docker compose -f deploy/docker-compose.yml --profile local-db --profile https up -d --build
```

`deploy/Caddyfile` serves the dashboard on `UPSERA_DOMAIN` and, for
[status pages on custom domains](#status-pages-on-a-custom-domain), requests a certificate on demand
only for domains of published pages (Caddy asks `/api/tls-allowed` over the internal network; the
public site returns 404 for that path). Without `UPSERA_DOMAIN` the profile serves `localhost` with
Caddy's local CA, which is handy to try it. Certificates live in the `caddy_data` volume.

### Backups

Back up the database **and** `APP_SECRET` together; see [`docs/BACKUP.md`](docs/BACKUP.md).

## Run locally without Docker

Needs Go 1.26+ and Postgres 17 (Supabase works too; see the plan for the pooler URL).

```sh
docker run -d --name upsera-pg -e POSTGRES_USER=upsera -e POSTGRES_PASSWORD=upsera \
  -e POSTGRES_DB=upsera -p 5432:5432 postgres:17-alpine

export DATABASE_URL='postgres://upsera:upsera@localhost:5432/upsera?sslmode=disable'
export APP_SECRET="$(openssl rand -base64 32)"
go run ./cmd/upsera
```

The server listens on `:3080`. Open <http://localhost:3080/> for the dashboard (the first visit
walks you through creating the instance admin and first team). API docs:
<http://localhost:3080/api/docs>.

The dashboard is built into the binary with `go:embed`. Build it first, or the server shows a
"not built" page at `/`:

```sh
npm --prefix web ci && npm --prefix web run build   # needs Node 24+
```

First run without the dashboard: `POST /api/setup` with `{"email","name","password","team_name"}`
creates the instance admin and the first team, and logs you in.

```sh
curl -c jar -X POST localhost:3080/api/setup -H 'content-type: application/json' \
  -d '{"email":"you@example.com","name":"You","password":"a long password","team_name":"Ops"}'
```

Cookie sessions must send the returned `csrf_token` as `X-CSRF-Token` on writes. For scripts,
create a team token (`POST /api/teams/{id}/tokens`, scope `read` or `write`) and send
`Authorization: Bearer ups_...`.

## Prometheus metrics

`GET /metrics` exposes the monitors of one team in the Prometheus text format. Authenticate with a team
API token (`read` scope is enough; cookies are not accepted), so each team scrapes only its own monitors:

```yaml
scrape_configs:
  - job_name: upsera
    scheme: https
    authorization: { credentials: ups_xxxxxxxx }   # or credentials_file
    static_configs: [{ targets: ["status.example.com"] }]
```

| Metric (gauge) | Meaning |
|---|---|
| `upsera_monitor_up` | `1` up or pending, `0` down. Absent while paused or not yet checked. |
| `upsera_monitor_status` | `0` down, `1` up, `2` pending, `3` maintenance. |
| `upsera_monitor_response_ms` | Latency of the most recent check. |
| `upsera_monitor_paused` | `1` when paused. |
| `upsera_monitor_cert_expiry_seconds` | Seconds until the TLS certificate expires, for HTTPS monitors. |

Labels: `monitor_id`, `monitor`, `type`, `group`.

## Multi-location probes

By default every check runs on the server itself (the built-in `local` region). To check from other
places, run `upsera-probe` on a VPS in each location. A probe needs no open ports: it polls the server
over HTTPS for the monitors assigned to its region, runs the checks, and pushes the results back. A
monitor can then select several regions and a quorum, so one probe's bad network does not page you.

**Add a probe**

1. In the dashboard open Administration → Probes (instance admin only) and create a probe with a
   name and a region (lowercase letters, digits and `-`, for example `sg-1`; `local` is reserved).
2. Copy the registration token (`upr_...`). It is shown once, is valid for 24 hours and works once.
3. On the remote host:

```sh
docker run -d --restart unless-stopped --name upsera-probe \
  -v upsera-probe:/data \
  -e SERVER_URL=https://status.example.com \
  -e PROBE_TOKEN=upr_xxxxxxxx \
  upsera/upsera-probe:latest
```

On first start the probe exchanges the token for a long-lived credential (`upp_...`) and keeps it in
`/data/credential` (mode 0600), so keep `/data` on a volume; after that `PROBE_TOKEN` is ignored.
The region comes from the probe row on the server, so there is no `REGION` variable. Build the image
yourself with `make docker-build-probe` (`docker build --target probe`) or the binary with `make probe`.
To rotate or revoke a probe's credential, issue a new registration token for it (the old credential
stops working at once) or delete the probe. A region still used by a monitor cannot be deleted. Deleting a
probe also deletes the heartbeats it reported, so that region's per-region latency history is gone (daily
uptime figures that were already rolled up stay).

**Probe settings** (`upsera-probe`):

| Variable | Default | Purpose |
|---|---|---|
| `SERVER_URL` | required | Base URL of the Upsera server. |
| `PROBE_TOKEN` | empty | One-time registration token; only needed on the first start. |
| `DATA_DIR` | `/data` | Where the credential is stored (the image sets `/data`). |
| `BUFFER_SIZE` | `10000` | Results kept in memory while the server is unreachable; the oldest are dropped first. |
| `MAX_CONCURRENT_CHECKS` | `50` | Checks running at once. |
| `ALLOW_INSECURE_HTTP` | unset | Must be `1` to use an `http://` `SERVER_URL`. Without it only HTTPS is accepted, because the credential and monitor configs travel over this connection. |
| `LOG_LEVEL` | `info` | Logging. |

`upsera-probe version` prints the version. If the server rejects the credential (revoked or deleted),
the probe logs it and exits non-zero.

**Server setting:** `PROBE_SILENT_AFTER` (default `2m`, min `30s`) is how long a registered probe may go
unseen before it counts as silent.

**Regions and quorum.** In a monitor's form pick one or more regions and a quorum (default: a majority,
so 2 of 3). The built-in `local` region is always available. Each region applies the monitor's retries
on its own results. The monitor is DOWN when at least `quorum` fresh regions are down. A region's result
is fresh for twice the monitor interval; if fewer than `quorum` fresh regions are reporting, the monitor
goes PENDING with a `probe silent: 1 of 3 regions reporting (sg-1 silent)` message instead of DOWN, so a
dead probe never opens a down alert by itself. Latency is charted per region on the monitor page.

**Probe silent alert.** When a registered probe is not heard from for `PROBE_SILENT_AFTER`, each team
that has an unpaused monitor in that region gets a message on its default channels, and another one when
the probe reports again. There is no acknowledgement or escalation for it.

**Trust model.** Probes receive the full config of the monitors in their region, including credentials
placed in headers or basic auth, so run probes only on hosts you trust. A probe credential is separate
from API tokens and cannot be used for anything but the probe endpoints. To revoke a compromised probe,
delete it or issue a new token, and rotate the credentials in those monitors if the host was exposed.
The `block_private_targets` instance setting also applies to probes.

**Things to know.** Each check from each region writes its own heartbeat row, so a monitor with three
regions stores three times the heartbeats (see `HEARTBEAT_RETENTION_DAYS`). Push and Docker monitors
run only on the server, and remote regions are limited to HTTP, keyword, TCP, ping and DNS monitors.

## How it compares

| | Upsera | Uptime Kuma | Gatus | Upptime |
|---|---|---|---|---|
| Runs as | one server + Postgres | one server (SQLite / MariaDB) | one binary, YAML config | GitHub Actions + GitHub Pages |
| Teams, roles, API tokens | yes | no | no | no |
| Add and edit monitors in a UI | yes | yes | no (edit the YAML) | no (edit the repo) |
| Docker container checks | yes | yes | no | no |
| Public status pages, incidents, subscribers | yes | status pages, incidents | status page | status site |
| Prometheus `/metrics` | yes, per team | yes | yes | no |
| Multi-location probes with quorum | yes | no | no | no |
| SLOs, monitors as code | planned (Phase 7) | no | config is code | config is code |

Based on each project's documentation at the time of writing; check theirs for the current state.
Uptime Kuma, Gatus and Upptime are good projects and a better fit when you want something smaller.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | required | Postgres URL. Tables live in the `upsera` schema. |
| `APP_SECRET` | required | ≥ 32 chars; back it up with the database. It encrypts notification channel secrets: changing it makes existing channels unusable until re-entered. |
| `BASE_URL` | `http://localhost:PORT` | Public URL, used for push URLs, secure cookies, and the links in status page subscriber emails and RSS feeds. |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | Telegram Bot API base URL (override for a local fake in tests). |
| `SLACK_API_URL` | `https://slack.com/api` | Slack Web API base URL (override for a local fake in tests). |
| `PORT` | `3080` | HTTP port. |
| `TZ` | `UTC` | Time zone for daily uptime buckets. |
| `DB_MAX_CONNS` | `10` | Connection pool cap (min 2). |
| `HEARTBEAT_BUFFER_SIZE` | `100000` | Heartbeats held in memory while the database is down (min 100). |
| `HEARTBEAT_FLUSH_INTERVAL` | `1s` | How often buffered heartbeats are written (min 100ms). |
| `HEARTBEAT_RETENTION_DAYS` | `14` | Raw heartbeat retention (min 3). |
| `ROLLUP_INTERVAL` | `1h` | Daily rollup / prune job interval (min 1m). |
| `MAX_CONCURRENT_CHECKS` | `100` | Checks running at once (min 1). |
| `INCIDENT_DRAFT_AFTER` | `5m` | How long a monitor on a published status page must stay down before an incident is drafted for review (min 1s). |
| `TRUSTED_PROXIES` | empty | Comma separated addresses or CIDRs of reverse proxies (for example your Caddy). Only their `X-Forwarded-For` is believed, to tell visitors apart for the public sign-up rate limits and the probe registration limit (10 attempts a minute per client); without it every visitor behind a proxy shares one limit. |
| `PROBE_SILENT_AFTER` | `2m` | How long a remote probe may go unseen before it is reported silent (min 30s). See [multi-location probes](#multi-location-probes). |
| `DOCKER_HOST` | empty | Docker socket proxy; its address is always refused as a check target. |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | Logging. |

`upsera healthcheck` exits 0 when the local server answers `/healthz` (for container health checks).
`upsera openapi` prints the OpenAPI document.

### Status pages on a custom domain

Only an instance admin can set a page's custom domain (it is claimed for the whole instance). Point
the domain's DNS at the server and put a TLS-terminating proxy in front of it, forwarding the
original `Host` header and listing the proxy in `TRUSTED_PROXIES`. With Caddy, `on_demand_tls { ask
http://upsera:3080/api/tls-allowed }` makes it request certificates only for published pages'
domains. Subscriber emails link to `https://<domain>/...` for such pages, which therefore needs TLS to
work. The [`https` compose profile](#https-with-caddy) does this.

## Development

```sh
go test ./...        # integration tests start Postgres with testcontainers (needs Docker)
go test -short ./... # unit tests only
```

With colima, export `DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock` and
`TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock` first.

Dashboard (`web/`: React, Vite, TypeScript, Tailwind):

```sh
cd web
npm ci
npm run dev       # Vite on :5173, proxying /api to a server on :3080
npm test          # Vitest
npm run lint
npm run gen:api   # regenerate src/api/schema.d.ts after API changes (CI checks it is current)
```
