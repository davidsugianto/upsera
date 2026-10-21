# upsera
Self-hosted uptime monitoring and status pages for teams. Multi-region checks, fewer false alarms.

![Upsera dashboard](site/public/screenshots/dashboard-light.png)

<table>
<tr>
<td><a href="site/public/screenshots/monitor-detail.png"><img src="site/public/screenshots/monitor-detail.png" alt="A degraded monitor with its error budget card"></a></td>
<td><a href="site/public/screenshots/reports.png"><img src="site/public/screenshots/reports.png" alt="The monthly SLA report with met and missed SLOs"></a></td>
<td><a href="site/public/screenshots/status-page.png"><img src="site/public/screenshots/status-page.png" alt="A public status page"></a></td>
</tr>
</table>

Documentation: the [docs site](https://davidsugianto.github.io/upsera/) (source in [`site/`](site/); it is published from `master` once GitHub Pages is switched on for the repository). Contributing: [`CONTRIBUTING.md`](CONTRIBUTING.md). Security reports: [`SECURITY.md`](SECURITY.md). Licence: [MIT](LICENSE).

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
remote `upsera-probe` agents, per-monitor regions with a quorum, and a "probe silent" alert. Phase 7 adds a
[DEGRADED state and SLOs](#degraded-state-and-slos) (error budgets, burn-rate notifications, a monthly SLA
report) and [monitors as code](#monitors-as-code-with-upsctl) with the `upsctl` CLI. Packaging adds
an optional Caddy HTTPS profile, Prometheus `/metrics`, backup notes, a docs site, `make screenshots`
and a [release workflow](#run-a-released-version). No release has been tagged yet; `v0.1.0` is planned as a
pre-release, because a clean-host install, a real Let's Encrypt certificate and a multi-VPS probe setup are
not verified yet.

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

### Run a released version

Tagged releases publish multi-arch images to GHCR (`ghcr.io/davidsugianto/upsera`, `upsera-probe` and
`upsctl`) and archives with `SHA256SUMS` to GitHub Releases (linux and macOS, amd64 and arm64). None is
published yet, so until the first tag use `make up`. After a release, pull instead of building:

```sh
make env                                   # writes deploy/.env if missing
export UPSERA_VERSION=0.1.0                # a published version, without the leading v
docker compose -f deploy/docker-compose.yml --profile local-db pull upsera
docker compose -f deploy/docker-compose.yml --profile local-db up -d
deploy/smoke.sh                            # waits for the server, creates the first admin, prints the elapsed time
```

`deploy/smoke.sh` takes the URL as its first argument (default `http://localhost:3080`) and reads
`SMOKE_EMAIL` and `SMOKE_PASSWORD`; it generates and prints a password only when it has to create the
first admin.

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
| `upsera_monitor_up` | `1` up, degraded or pending, `0` down. Absent while paused or not yet checked. |
| `upsera_monitor_status` | `0` down, `1` up, `2` pending, `3` maintenance, `4` degraded. |
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
  ghcr.io/davidsugianto/upsera-probe:latest
```

On first start the probe exchanges the token for a long-lived credential (`upp_...`) and keeps it in
`/data/credential` (mode 0600), so keep `/data` on a volume; after that `PROBE_TOKEN` is ignored.
The region comes from the probe row on the server, so there is no `REGION` variable. The image name above
is published by the release workflow (none is published yet); until then build it yourself with
`make docker-build-probe` (`docker build --target probe`, tagged `upsera-probe:dev`) or the binary with
`make probe`.
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

## Degraded state and SLOs

**DEGRADED.** A monitor that answers, but far slower than usual, is shown as `degraded` instead of `up`. A
region is degraded when its p95 latency over the last 15 minutes is more than 3× its own 7-day p95 baseline
(and at least 100 ms above it); it recovers at 2× or lower. It needs 100 baseline samples first, applies to
HTTP, keyword, TCP, ping and DNS monitors, and counts as up for uptime. It sends one `degraded` message and one
`latency_recovered` message to the monitor's first escalation step; it never opens an alert. The baseline is
read from the database once an hour, so a database outage freezes the baseline but never stops your checks.

**SLOs.** Give a monitor a target (for example 99.9 %) over 7 to 90 days. The monitor page shows the error
budget and a burndown chart. Burn-rate notifications (`slo_burn`, `slo_burn_recovered`, notification only)
fire on the usual two-window rule: **fast** when the 1 h and 5 min burn rates are both at least 14.4×,
**slow** when the 6 h and 30 min burn rates are both at least 6× (both thresholds are editable). The
evaluator reads heartbeats from the database once a minute, so it skips a tick while Postgres is down;
DOWN alerts are not affected. A check is good when the monitor is up, pending or degraded; maintenance is
not counted. **Reports** (team menu) lists, per month, uptime, SLO met or missed, downtime, alerts and p95
for every monitor, with a CSV download and a print layout (`GET /api/teams/{id}/reports/sla?month=YYYY-MM`).
The report's p95 is the check-weighted average of the daily p95 values.

Rolling back to a build older than Phase 7 needs `UPDATE upsera.heartbeats SET status = 1 WHERE status = 4`
and the same on `upsera.monitor_state` first, because the older code does not know status `4`. The older
build starts fine against a database that already has migration `00006` (it applies nothing; a test covers
this), and the extra `upsera.slos` table and the wider `notification_log` event check do no harm. To remove
them as well, run the `-- +goose Down` statements of `internal/store/migrations/00006_slos.sql` **and**
`DELETE FROM upsera.goose_db_version WHERE version_id = 6` together (one without the other leaves the
migration state wrong). That also deletes the log rows of the new event kinds.

## Monitors as code with upsctl

`upsctl` exports, diffs and applies a team's channels, escalation policies, monitors (with their SLO) and
status pages as YAML through the REST API, for example from CI (see `deploy/examples/`).

```sh
export UPSERA_URL=https://status.example.com UPSERA_TOKEN=ups_xxxxxxxx   # a team token; apply needs `write`
upsctl export -o upsera.yaml          # live team -> YAML, secrets as ${UPSERA_<CHANNEL>_<FIELD>} placeholders
upsctl diff -f upsera.yaml            # exit 0 = nothing to do, 2 = changes, 1 = error
upsctl apply -f upsera.yaml --yes     # create and update; add --prune to delete what the file no longer lists
```

Resources are matched by name inside the team (status pages by slug) and refer to each other by name.
`upsctl` refuses to run when a team has duplicate names. Secrets are never written by `export`; `apply`
reads them from the environment (`${UPSERA_...}` inside string values, `$$` for a literal `$`; other variables, and `UPSERA_TOKEN` and
`UPSERA_URL`, cannot be referenced) and the server
ignores a channel update that changes nothing, so repeated applies stay quiet. Push URLs are generated per
instance, and a status page's `custom_domain` can only be set by an instance admin session in the browser (apply skips it with
a warning). Regions and Docker hosts named by a monitor must already exist. Quote YAML names such as
`n`, `no` or `on` (YAML 1.1 reads them as booleans). Build it with `make upsctl` or `docker build --target upsctl .`; releases will also publish the
`ghcr.io/davidsugianto/upsctl` image and `upsctl` in the release archives.

## How it compares

| | Upsera | Uptime Kuma | Gatus | Upptime |
|---|---|---|---|---|
| Runs as | one server + Postgres | one server (SQLite / MariaDB) | one binary, YAML config | GitHub Actions + GitHub Pages |
| Teams, roles, API tokens | yes | several admin users, no roles or teams; API keys for `/metrics` only | no teams or roles; basic auth or OIDC | no (access is GitHub's) |
| Add and edit monitors in a UI | yes | yes | no (edit the YAML) | no (edit the repo) |
| Docker container checks | yes | yes | no | not documented |
| Public status pages, incidents, subscribers | yes | status pages, incidents | status page, announcements | status site, incidents as GitHub issues |
| Prometheus `/metrics` | yes, per team | yes | yes (off by default) | not documented |
| Multi-location probes with quorum | yes | not documented | no quorum (external endpoints push results; remote instances are experimental) | not documented |
| SLOs, monitors as code | yes (`upsctl` diff / apply) | no SLOs, no built-in monitors as code | config is code, no SLOs | config is code |

Checked against each project's README, wiki and issue tracker on 2026-10-21; "not documented" means we
did not find it there, not that it cannot be done. Parts of Gatus's README and Uptime Kuma 2.x release notes
were not reviewed. Check their documentation for the current state. Uptime Kuma, Gatus and Upptime are good
projects and a better fit when you want something smaller.

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
