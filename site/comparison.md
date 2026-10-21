# How it compares

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
