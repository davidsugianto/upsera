# Backup and restore

Upsera keeps all state in Postgres (the `upsera` schema), plus one secret that lives outside it.
Back up both, and keep them together.

| What | Where | Why |
|---|---|---|
| The database | Postgres (compose volume `pg_data`, or Supabase) | Monitors, heartbeats, alerts, teams, users, status pages. |
| `APP_SECRET` | `deploy/.env` | Encrypts notification channel secrets (bot tokens, SMTP passwords, webhook URLs) in the database. A restored database with a different `APP_SECRET` still starts, but every channel and the instance SMTP settings must be entered again. |
| `caddy_data` volume | Only with the `https` profile | Holds issued certificates and the ACME account. Losing it only means certificates are re-issued, which can hit Let's Encrypt rate limits if done repeatedly. |

`deploy/.env` also holds `DATABASE_URL` and `POSTGRES_PASSWORD`; copy the whole file somewhere safe
(a password manager, not the same disk as the database).

## Bundled Postgres (`local-db` profile)

Back up:

```sh
docker compose -f deploy/docker-compose.yml --profile local-db exec -T postgres \
  pg_dump -U upsera -d upsera --format=custom > upsera-$(date +%F).dump
```

Restore into a fresh stack (same `APP_SECRET`):

```sh
docker compose -f deploy/docker-compose.yml --profile local-db up -d postgres
docker compose -f deploy/docker-compose.yml --profile local-db exec -T postgres \
  pg_restore -U upsera -d upsera --clean --if-exists --no-owner < upsera-2026-10-11.dump
docker compose -f deploy/docker-compose.yml --profile local-db up -d
```

Stop Upsera while restoring (`docker compose ... stop upsera`) so it is not writing heartbeats into a
database that is being replaced. The server runs its migrations on start, so restoring a dump taken on
an older version and then starting a newer image upgrades it.

## Supabase or another external Postgres

Use the provider's backups (Supabase takes daily backups on paid plans), or `pg_dump` against the
direct connection (not the transaction pooler):

```sh
pg_dump "$DIRECT_DATABASE_URL" --schema=upsera --format=custom > upsera.dump
```

## What you can skip

Raw heartbeats are pruned after `HEARTBEAT_RETENTION_DAYS` (default 14) and rolled up into daily uptime,
so most of the database's size is disposable. If the backup size matters, dump everything except the
`upsera.heartbeats` rows (`--exclude-table-data=upsera.heartbeats`); uptime history survives in
`uptime_daily`, only the recent latency charts and event history start empty.

## Test your restore

A backup you have not restored is a hope. Once, on a spare machine: restore the dump and the saved
`.env`, start the stack, sign in, and use *Send test* on a notification channel. That last step proves
`APP_SECRET` matches the data.
