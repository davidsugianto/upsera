# Configuration

The server reads its settings from environment variables. With Docker Compose they come from
`deploy/.env`. An invalid value stops the server at start with an error that names the variable.

Durations use Go syntax, for example `30s`, `5m`, `1h`.

## Server

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres URL. Tables live in the `upsera` schema. |
| `APP_SECRET` | required | At least 32 characters. Back it up with the database. It encrypts notification channel secrets: changing it makes existing channels unusable until you enter them again. Generate one with `openssl rand -base64 32`. |
| `BASE_URL` | `http://localhost:PORT` | Public URL. Used for push URLs, secure cookies, and the links in status page subscriber emails and RSS feeds. Must be an absolute `http` or `https` URL. Cookies get the `Secure` flag when it starts with `https://`. |
| `PORT` | `3080` | HTTP port (1 to 65535). |
| `TZ` | `UTC` | IANA time zone, for example `Asia/Jakarta`. Sets the boundaries of daily uptime buckets and recurring maintenance windows. |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn` or `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |

## Database and heartbeats

| Variable | Default | Meaning |
|---|---|---|
| `DB_MAX_CONNS` | `10` | Connection pool cap (minimum 2). |
| `HEARTBEAT_BUFFER_SIZE` | `100000` | Heartbeats held in memory while the database is down (minimum 100). |
| `HEARTBEAT_FLUSH_INTERVAL` | `1s` | How often buffered heartbeats are written (minimum `100ms`). |
| `HEARTBEAT_RETENTION_DAYS` | `14` | How long raw heartbeats are kept (minimum 3). Older ones are pruned after they are rolled up into daily uptime. |
| `ROLLUP_INTERVAL` | `1h` | How often the daily rollup and prune job runs (minimum `1m`). |

## Checks and incidents

| Variable | Default | Meaning |
|---|---|---|
| `MAX_CONCURRENT_CHECKS` | `100` | Checks running at once on the server (minimum 1). |
| `INCIDENT_DRAFT_AFTER` | `5m` | How long a monitor on a published status page must have an open alert before an incident is drafted for review (minimum `1s`). |
| `PROBE_SILENT_AFTER` | `2m` | How long a registered remote probe may go unseen before it counts as silent (minimum `30s`). See [probes](./probes). |
| `DOCKER_HOST` | empty | Address of the Docker socket proxy, for example `tcp://docker-proxy:2375`. The compose file sets it. Its address is always refused as a target for ordinary checks. |

## Proxies

| Variable | Default | Meaning |
|---|---|---|
| `TRUSTED_PROXIES` | empty | Comma separated addresses or CIDRs of reverse proxies, for example your Caddy. Only their `X-Forwarded-For` header is believed. It is used to tell visitors apart for the public sign-up rate limits and the probe registration limit (10 attempts a minute per client). Without it every visitor behind a proxy shares one limit. See [HTTPS](./https). |

## API endpoints for chat tools

| Variable | Default | Meaning |
|---|---|---|
| `TELEGRAM_API_URL` | `https://api.telegram.org` | Telegram Bot API base URL. Override it to point tests at a local fake. |
| `SLACK_API_URL` | `https://slack.com/api` | Slack Web API base URL. Override it to point tests at a local fake. |

## Compose-only variables

These are read by `deploy/docker-compose.yml` and the Caddyfile, not by the server.

| Variable | Default | Meaning |
|---|---|---|
| `POSTGRES_PASSWORD` | empty | Password of the bundled Postgres container. Must match the one in `DATABASE_URL`. |
| `UPSERA_PORT` | `3080` | Host port the server is published on. |
| `UPSERA_VERSION` | `dev` | Image tag and build version. |
| `UPSERA_DOMAIN` | `localhost` | Dashboard hostname for the `https` profile. See [HTTPS](./https). |

## Probe settings

`upsera-probe` has its own variables. See [probes](./probes#probe-settings).

## Commands

- `upsera healthcheck` exits 0 when the local server answers `/healthz`. The compose file uses it for the container health check.
- `upsera openapi` prints the OpenAPI document.
