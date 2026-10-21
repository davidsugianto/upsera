# Alerting

![The Channels page with a webhook channel marked Default and a Slack channel.](/screenshots/alert-channels.png)

## How an alert flows

1. A monitor goes `down` after its retries. An alert opens.
2. Upsera notifies the channels of the first escalation step.
3. If nobody acknowledges the alert, it moves to the next step after that step's delay.
4. When the monitor recovers, the alert resolves and the channels get a recovery message.

Checks and alerts keep working while the database is down. Alert and notification log writes wait in
memory and are written when the database is back.

Where the messages go:

1. the monitor's escalation policy, if it has one;
2. else the channels chosen on the monitor;
3. else the team's default channels.

## Channels

| Type | Notes |
|---|---|
| Telegram | Bot token and chat id. Messages have an Acknowledge button. |
| Discord | Webhook URL. |
| Slack (webhook) | Incoming webhook. No buttons. |
| Slack app | Bot with Socket Mode. Messages have an Acknowledge button. |
| Email (SMTP) | Your SMTP server. |
| Webhook | Calls your URL, with optional custom headers. |

Channel secrets are encrypted with a key derived from `APP_SECRET`. The API and dashboard show them as
`********`. Changing `APP_SECRET` makes existing channels unusable until you enter their secrets again.
Use *Send test* on a channel to check it.

## Escalation policies

A policy is an ordered list of steps. Each step has channels and a delay in seconds. Step one is sent when
the alert opens. Later steps are sent after their delay if the alert is still open and not acknowledged.

## Acknowledgements

Acknowledge an alert in the dashboard, with the button in Telegram or the Slack app, or through the API.
An acknowledged alert stops escalating.

## Dependencies and flapping

A monitor with a parent stays silent while the parent is down. A monitor that flips between up and down
quickly is damped, so you do not get a message for each flip.

## Maintenance windows

A maintenance window covers chosen monitors for a time range. It can be one time, daily or weekly. A
recurring window must not be longer than its period. Daily and weekly windows use the server time zone
(`TZ`). Monitors inside a window show `maintenance`, and checks do not open alerts.

## Notification-only events

Some events send a message to the first escalation step's channels but never open an alert. There is no
acknowledgement or escalation for them.

| Event | When |
|---|---|
| `degraded`, `latency_recovered` | A monitor became much slower than usual, and when it returned to normal. See [degraded state](./slos#degraded-state). |
| `slo_burn`, `slo_burn_recovered` | The error budget burns too fast, and when it stops. See [SLOs](./slos#slos). |
| Certificate expiry | The TLS certificate has 14, 7 or 1 days left. |
| Probe silent | A remote probe is not heard from. It goes to the team's default channels. See [probes](./probes#probe-silent-alert). |

A down parent silences `degraded` messages of its children. It does not silence `slo_burn` messages.
