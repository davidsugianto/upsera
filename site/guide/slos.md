# Degraded state and SLOs

![A monitor page with the Degraded badge and the error budget card: 99.9% target over 30 days, SLI, burn rates and a burndown chart.](/screenshots/monitor-detail.png)

## Degraded state

A monitor that answers, but far slower than usual, is shown as `degraded` instead of `up`.

- A region is degraded when its p95 latency over the last 15 minutes is more than 3 times its own 7-day p95
  baseline, and at least 100 ms above it.
- It recovers when the latency is at 2 times the baseline or lower.
- It needs 100 baseline samples first.
- It applies to HTTP, keyword, TCP, ping and DNS monitors. Push and Docker monitors never degrade.
- `down` and `pending` always win over `degraded`.
- Degraded counts as up for uptime.

Upsera sends one `degraded` message and later one `latency_recovered` message to the monitor's first
escalation step. It never opens an alert.

The baseline is read from the database once an hour. A database outage freezes the baseline but never stops
your checks. There is no setting for these thresholds.

## SLOs

An SLO gives a monitor a target, for example 99.9 %, over a window of 7 to 90 days (default 30). The monitor
page shows the error budget and a burndown chart.

![The monitor form's Service level objective section: the toggle, a 99.9 target, a 30 day window and the Advanced burn thresholds.](/screenshots/slo-form.png)

A check is good when the monitor is up, pending or degraded. Maintenance is not counted.

### Burn-rate messages

Upsera sends `slo_burn` when the budget burns too fast, and `slo_burn_recovered` when it stops. Both are
notification only. They use a two-window rule:

| Rule | Fires when | Default |
|---|---|---|
| Fast | the 1 hour and 5 minute burn rates are both at or above the threshold | 14.4 |
| Slow | the 6 hour and 30 minute burn rates are both at or above the threshold | 6 |

Both thresholds are editable per SLO. The evaluator reads heartbeats from the database once a minute, so it
skips a tick while Postgres is down. DOWN alerts are not affected.

## SLA report

*Reports* in the team menu lists, per month, for every monitor: uptime, whether the SLO was met or missed,
downtime, number of alerts and p95 latency. You can download it as CSV or print it.

![The SLA report for October 2026 with a partial-month notice; one monitor missed its 99.9 % target and one met it.](/screenshots/reports.png)

The API is `GET /api/teams/{id}/reports/sla?month=YYYY-MM`. The month uses the server time zone and defaults
to the current month. The current month is marked partial. Downtime is the time covered by the monitor's
alerts. The p95 is the check-weighted average of the daily p95 values.

## Rolling back to an older build

A build older than Phase 7 does not know status `4` (degraded). Before rolling back, run:

```sql
UPDATE upsera.heartbeats SET status = 1 WHERE status = 4;
UPDATE upsera.monitor_state SET status = 1 WHERE status = 4;
```

The older build starts fine against a database that already has migration `00006`. The extra `upsera.slos`
table does no harm. To remove it as well, run the `-- +goose Down` statements of
`internal/store/migrations/00006_slos.sql` and
`DELETE FROM upsera.goose_db_version WHERE version_id = 6` together. One without the other leaves the
migration state wrong. That also deletes the log rows of the new event kinds.
