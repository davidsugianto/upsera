---
layout: home

hero:
  name: Upsera
  text: Uptime monitoring you host yourself
  tagline: Multi-region checks, status pages and SLOs for teams. One server and Postgres.
  image:
    src: /screenshots/dashboard-light.png
    alt: The Upsera dashboard listing six monitors with heartbeat bars and uptime
  actions:
    - theme: brand
      text: 5 minutes to a dashboard
      link: /guide/getting-started
    - theme: alt
      text: How it compares
      link: /comparison

features:
  - title: Many check types
    details: HTTP, keyword, TCP, ping, DNS, push (heartbeat) and Docker container checks, with TLS certificate expiry warnings.
    link: /guide/monitors
    linkText: Monitors
  - title: Alerts that escalate
    details: Telegram, Discord, Slack, email and webhook channels, escalation steps, acknowledgements, monitor dependencies and maintenance windows.
    link: /guide/alerting
    linkText: Alerting
  - title: Public status pages
    details: Pages at /status/slug, optionally on your own domain. Incidents are drafted from outages. Email, webhook and RSS subscribers.
    link: /guide/status-pages
    linkText: Status pages
  - title: Fewer false alarms
    details: Run upsera-probe in other places. A monitor goes down only when a quorum of regions agrees.
    link: /guide/probes
    linkText: Probes
  - title: Degraded state and SLOs
    details: A slow target is shown as degraded. Set an SLO, watch the error budget, get burn-rate messages and a monthly SLA report.
    link: /guide/slos
    linkText: SLOs
  - title: Monitors as code
    details: Export, diff and apply a team's setup as YAML with upsctl, for example from CI.
    link: /guide/upsctl
    linkText: upsctl
---
