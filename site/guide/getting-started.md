# Getting started

Upsera is one server binary plus Postgres. The dashboard is built into the binary.
Pick one way to run it.

## Docker Compose, built from source

You need Docker with Compose 2.20 or newer. Colima works.

From a clone of the repository:

```sh
make up      # writes deploy/.env with random secrets if missing, builds the image, starts the stack
make logs    # follow the server logs
make down    # stop (the Postgres volume is kept)
```

Open `http://localhost:3080/`.

The stack in `deploy/docker-compose.yml` runs three containers:

- the Upsera server,
- a Postgres 17 container (the `local-db` profile),
- a read-only Docker socket proxy, used by Docker container monitors.

`make help` lists every task.

### Create the first admin

On the first visit the setup screen asks for your email, name, password and a team name.
It creates the instance admin and the first team, and signs you in. After that the setup
screen is gone. Then add a monitor from the dashboard.

You can also do it with the API. `GET /api/setup` tells you whether setup is still needed.

```sh
curl -c jar -X POST localhost:3080/api/setup -H 'content-type: application/json' \
  -d '{"email":"you@example.com","name":"You","password":"a long password","team_name":"Ops"}'
```

### Start over

To start with an empty database:

```sh
docker compose -f deploy/docker-compose.yml --profile local-db down -v
```

### Use Supabase instead of the bundled Postgres

Set its `DATABASE_URL` in `deploy/.env` (see `deploy/.env.example`) and run:

```sh
docker compose -f deploy/docker-compose.yml up -d --build
```

Leave out `--profile local-db`.

## Docker Compose, from a release

Released images are published to GHCR as `ghcr.io/davidsugianto/upsera`.
The compose file reads the image tag from `UPSERA_VERSION`. Without it the tag is `dev`,
which is the image `make up` builds.

To run a release, create `deploy/.env` (copy `deploy/.env.example` and fill in the secrets,
or run `make env`) and set the version you want:

```sh
export UPSERA_VERSION=0.1.0     # an image tag that exists on GHCR
docker compose -f deploy/docker-compose.yml --profile local-db pull
docker compose -f deploy/docker-compose.yml --profile local-db up -d
```

Check the repository's releases page for the tags that exist. Use `up -d --build` instead
when you want to build from source.

## Run locally without Docker

You need Go 1.26 or newer and Postgres 17. Supabase works too.

```sh
docker run -d --name upsera-pg -e POSTGRES_USER=upsera -e POSTGRES_PASSWORD=upsera \
  -e POSTGRES_DB=upsera -p 5432:5432 postgres:17-alpine

export DATABASE_URL='postgres://upsera:upsera@localhost:5432/upsera?sslmode=disable'
export APP_SECRET="$(openssl rand -base64 32)"
go run ./cmd/upsera
```

The server listens on port 3080. API documentation is at `http://localhost:3080/api/docs`.

The dashboard is built into the binary with `go:embed`. Build it first, or the server shows a
"not built" page at `/`. This needs Node 24 or newer:

```sh
npm --prefix web ci && npm --prefix web run build
```

## API access for scripts

Cookie sessions must send the returned `csrf_token` as `X-CSRF-Token` on writes.
For scripts, create a team token (`POST /api/teams/{id}/tokens`, scope `read` or `write`)
and send it as `Authorization: Bearer ups_...`.

## Next steps

- [Configuration](./configuration) lists every environment variable.
- [Monitors](./monitors) explains the check types.
- [Alerting](./alerting) shows how to get notified.
- [HTTPS with Caddy](./https) puts the server on a public domain.
- [Backups](./backups): back up the database and `APP_SECRET` together.
