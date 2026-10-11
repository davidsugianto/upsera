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
instance SMTP server (Administration → settings).

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
| `TRUSTED_PROXIES` | empty | Comma separated addresses or CIDRs of reverse proxies (for example your Caddy). Only their `X-Forwarded-For` is believed, to tell visitors apart for the public sign-up rate limits; without it every visitor behind a proxy shares one limit. |
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
work. The packaged Caddy profile comes in a later phase.

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
