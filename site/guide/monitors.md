# Monitors

A monitor is a check that runs on an interval. Each team has its own monitors.

![The Monitors page in the dark theme: six monitors in two groups with heartbeat bars, 24 hour and 30 day uptime and latency; one monitor is degraded.](/screenshots/dashboard-dark.png)

## Check types

| Type | What it checks |
|---|---|
| `http` | Sends a request and checks the status code. Settings: URL, method (`GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`), headers, body, accepted status ranges such as `200-299` or `301`, maximum redirects (default 10, `0` turns following off) and an option to ignore TLS errors. |
| `keyword` | Like `http`, and also looks for a keyword in the response. It can be inverted (up when the keyword is absent) and case sensitive. |
| `tcp` | Opens a TCP connection to a host and port. |
| `ping` | Sends ICMP pings to a host. The compose file sets `net.ipv4.ping_group_range` so the non-root container can do it. |
| `dns` | Looks up a record (`A`, `AAAA`, `CNAME`, `MX`, `TXT` or `NS`). It is up when at least one record comes back and, if you set an expected value, one record contains it (case-insensitive). You can choose a resolver as `host:port`. |
| `push` | A heartbeat. Your job calls `GET` or `POST /api/push/<token>` on the server. The monitor goes down when no push arrives within its interval. The push URL is shown on the monitor and is built from `BASE_URL`. |
| `docker` | Checks a container on a Docker host through the socket proxy. See below. |

HTTP and keyword monitors on HTTPS also record the TLS certificate expiry. Upsera sends a message to the
monitor's channels when the certificate has 14, 7 and 1 days left, once per threshold.

### Docker monitors

A Docker monitor names a Docker host (managed in the dashboard) and a container name or id.

- Running, with no health check or a healthy one: up.
- `starting`: counts as a failed check, so retries apply before the monitor goes pending or down.
- `unhealthy`, `exited (N)`, `restarting`, `paused`, container not found, or host unreachable: down, with that reason in the alert.

The compose file starts a Docker socket proxy that allows read-only container calls. A `local` host
is created from `DOCKER_HOST` at setup.

## Timing

| Setting | Default | Limits |
|---|---|---|
| `interval_s` | `60` | 20 to 86400 seconds |
| `timeout_s` | `10` | at least 1, and less than the interval |
| `retries` | `1` | 0 to 10 |
| `retry_interval_s` | `20` | 20 to 86400 seconds |

When a check fails, Upsera retries up to `retries` times, waiting `retry_interval_s` between tries.
The monitor shows `pending` meanwhile. It goes `down` only when all retries fail, and an alert opens then.

## Status values

| Status | Meaning |
|---|---|
| `up` | The last check passed. |
| `down` | Checks failed after the retries. |
| `pending` | A check failed and retries are running, or too few regions reported. |
| `maintenance` | Paused, or inside a [maintenance window](./alerting#maintenance-windows). |
| `degraded` | Up but much slower than usual. See [degraded state](./slos#degraded-state). |

## Regions

By default every check runs on the server, in the built-in `local` region. A monitor can select more
regions and a quorum. See [probes](./probes). Push and Docker monitors run only on the server.

## Organising monitors

- A monitor can have a group name and tags.
- A monitor can have a parent. When the parent is down, the alerts of its children are silenced.
- Pausing a monitor moves it to maintenance and resolves its open alert without notifying anyone.

Monitors can be created in the dashboard, through the REST API, or from a YAML file with [upsctl](./upsctl).
