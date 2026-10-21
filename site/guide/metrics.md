# Prometheus metrics

`GET /metrics` exposes the monitors of one team in the Prometheus text format.

Authenticate with a team API token. The `read` scope is enough. Cookies are not accepted. Each team
scrapes only its own monitors.

```yaml
scrape_configs:
  - job_name: upsera
    scheme: https
    authorization: { credentials: ups_xxxxxxxx }   # or credentials_file
    static_configs: [{ targets: ["status.example.com"] }]
```

Without a valid token the endpoint answers 401.

## Metrics

All metrics are gauges.

| Metric | Meaning |
|---|---|
| `upsera_monitor_up` | `1` up, degraded or pending, `0` down. Absent while paused or not yet checked. |
| `upsera_monitor_status` | `0` down, `1` up, `2` pending, `3` maintenance, `4` degraded. |
| `upsera_monitor_response_ms` | Latency of the most recent check. |
| `upsera_monitor_paused` | `1` when paused. |
| `upsera_monitor_cert_expiry_seconds` | Seconds until the TLS certificate expires, for HTTPS monitors. |

Labels on every metric: `monitor_id`, `monitor`, `type`, `group`.
