# Documentation screenshots

`make screenshots` regenerates the nine PNGs in `site/public/screenshots/` that
the docs site embeds. `run.sh` starts a throwaway Upsera stack (compose project
`upsera-shots` on port 3181, its own volumes, using your `deploy/.env` read-only, with
its `DATABASE_URL`, `APP_SECRET` and `BASE_URL` overridden, see below),
creates a demo user, applies `deploy/examples/demo.yaml` with `upsctl` and loads
30 days of history from `seed.sql`: mostly UP checks, a short outage on one
monitor, a long one on another (a missed SLO) and a slow stretch that makes
`checkout-api` genuinely DEGRADED. Then it restarts the server so it loads that
state and tears the stack down again with `down -v`, also when something fails.

`capture.mjs` drives Chromium (Playwright, 1440x900) through the dashboard in
the dark and light theme, the monitor list, a monitor with its error budget
card, the SLO form, the monthly report, the public status page, Administration
> Probes and the notification channels. It fails with a non-zero exit code when
the browser logs a console error or a page error, or when an API request fails or
returns 4xx/5xx (except the 404 that means a monitor has no SLO), so a run is
also a real-browser smoke test of the dashboard.

Rerun it any time the UI changes: `make screenshots` (needs Docker, Go, Node 24,
`npx`, `curl`, `jq` and `openssl`; the script names any that is missing and stops; the first run installs Playwright's Chromium into its own cache with
`npm ci` and `npx playwright install chromium` in this directory). The demo
monitors check `example.com` and `example.org` once after the restart, so the
machine needs internet access; if they cannot be reached the script stops
before taking any picture. Nothing outside `tools/screenshots/` gets a Node
dependency.

**Isolation.** The compose file takes `DATABASE_URL`, `APP_SECRET` and `BASE_URL` from `deploy/.env`,
which may point at a real database (the README documents Supabase). So `run.sh` layers a generated
compose override over it (`override.sh`, written to a temporary directory and deleted on exit): the
server gets the throwaway Postgres container with a random password, a random `APP_SECRET` and a
plain-http `BASE_URL`. Before it starts anything, it checks the effective config and refuses to run if
it is not isolated. `check-isolation.sh` tests this against a fake `.env` that points at a remote
database, and that `run.sh` names a missing tool; it starts nothing and never reads your real `.env`.
