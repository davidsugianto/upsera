# Contributing to Upsera

Thanks for helping. Bug reports, fixes and small features are welcome; for anything larger, open an
issue first so we can agree on the approach before you write it.

## Set up

You need Go 1.26+, Node 24 (`web/.nvmrc`) and Docker (the integration tests start Postgres 17 with
testcontainers). With colima, export these first:

```sh
export DOCKER_HOST=unix://$HOME/.config/colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

`make help` lists every task. The ones you will use most:

```sh
make check        # everything CI runs: gofmt, go vet, eslint, tsc, go test -race, vitest, web build
make test-short   # Go unit tests only (fast, no Docker)
make test-web     # Vitest
make dev          # Vite on :5173, proxying /api to a server on :3080
make up / down    # the Docker Compose stack on http://localhost:3080
```

`make check` takes about 10 minutes, mostly the end-to-end tests in `cmd/upsera`. Run it before you
open a pull request.

## Rules that CI or review will hold you to

- **Regenerate the API types.** The dashboard's API types (`web/src/api/schema.d.ts`) are generated
  from the Go API's OpenAPI spec. After any change to a request, response or route, run
  `make gen-api` and commit the result. CI regenerates the file and fails if it differs from the one
  committed.
- **Never edit a committed migration.** Files in `internal/store/migrations` have already run on
  real databases. Change the schema by adding a new, higher-numbered migration (with a
  `-- +goose Down` section). Every table lives in schema `upsera` and every query is
  schema-qualified.
- **Go 1.26 idioms.** Use `new(expr)` (for example `new(30)` or `new(time.Now())`) instead of pointer
  helpers such as `intPtr`.
- **Format.** `gofmt` for Go, `npm run lint` for the dashboard.

## Where tests live

- Go unit and store tests sit next to the code as `*_test.go`. Store tests use
  `internal/testutil` (`Store(t)`, `PostgresURL(t)`, `FakeSMTP(t)`), which start Postgres with
  testcontainers.
- End-to-end "done when" tests run the real server in `cmd/upsera/*_test.go`.
- Dashboard tests are Vitest files (`*.test.ts(x)`) next to the components in `web/src`.

A behaviour change comes with a test that fails without it.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/): `feat(scope): ...`,
  `fix(scope): ...`, `docs: ...`, `refactor: ...`, `test: ...`, `chore: ...`.
- One topic per pull request, branched from `master`. Keep it focused, describe what changed and
  how you tested it, and make sure CI is green.
- Report security problems privately, not in an issue or pull request: see [SECURITY.md](SECURITY.md).

By contributing you agree that your work is released under the [MIT licence](LICENSE).
