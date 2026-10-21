#!/usr/bin/env bash
# Checks that `make screenshots` cannot reach a real database. It copies the
# compose file next to a FAKE deploy/.env that points at a remote database
# (the Supabase setup the README documents) and asserts that, with the
# override run.sh generates, the throwaway upsera service talks to the
# throwaway postgres container only and does not reuse the .env secrets.
# It also checks that run.sh names a missing required tool. Nothing is started
# and the real deploy/.env is never read.
#   tools/screenshots/check-isolation.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
# shellcheck source=tools/screenshots/override.sh
source "$here/override.sh"

for tool in docker jq openssl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "check-isolation: $tool is required" >&2; exit 2; }
done

colima_sock="$HOME/.config/colima/default/docker.sock"
if [ -z "${DOCKER_HOST:-}" ] && [ -S "$colima_sock" ]; then
  export DOCKER_HOST="unix://$colima_sock"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/deploy"
cp "$root/deploy/docker-compose.yml" "$root/deploy/Caddyfile" "$tmp/deploy/"
cat >"$tmp/deploy/.env" <<'EOF'
DATABASE_URL=postgres://real:realpw@db.supabase.example.org:5432/prod
APP_SECRET=REAL-SECRET-REAL-SECRET-REAL-SECRET-0123
BASE_URL=https://status.real.example.org
TZ=UTC
EOF

port=3181
pw=testpw0123
effective() { # extra compose args...
  UPSERA_PORT="$port" POSTGRES_PASSWORD="$pw" docker compose -p upsera-shots \
    -f "$tmp/deploy/docker-compose.yml" "$@" --profile local-db config --format json
}

fail=0
check() { # description, jq filter, json
  if jq -e "$2" >/dev/null <<<"$3"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi
}

# The premise: without an override the .env leaks into the throwaway project.
base=$(effective)
check "premise: without an override the remote DATABASE_URL reaches the service" \
  '.services.upsera.environment.DATABASE_URL | test("supabase")' "$base"

write_override "$tmp/override.yml" "$port" "$pw"
iso=$(effective -f "$tmp/override.yml")
check "DATABASE_URL points at the throwaway postgres container" \
  '.services.upsera.environment.DATABASE_URL | test("@postgres:5432/")' "$iso"
# shellcheck disable=SC2016 # $p is a jq variable, not a shell one
check "the postgres container and DATABASE_URL use the same throwaway password" \
  '.services.postgres.environment.POSTGRES_PASSWORD as $p | .services.upsera.environment.DATABASE_URL | contains(":" + $p + "@")' "$iso"
check "APP_SECRET is not the one from .env" \
  '.services.upsera.environment.APP_SECRET | (startswith("REAL") | not) and length >= 32' "$iso"
check "BASE_URL is the plain-http throwaway URL" \
  ".services.upsera.environment.BASE_URL == \"http://localhost:$port\"" "$iso"
if assert_isolated "$port" <<<"$iso"; then echo "ok   assert_isolated passes on the isolated config"; else echo "FAIL assert_isolated rejected the isolated config"; fail=1; fi
if assert_isolated "$port" <<<"$base"; then echo "FAIL assert_isolated accepted a leaking config"; fail=1; else echo "ok   assert_isolated rejects the leaking config"; fi

# A missing tool must be named, not surface later as a misleading "not
# isolated" message. Stub every required tool but one and expect exit 2 and
# that tool's name (run.sh checks before it touches deploy/.env or Docker).
bin="$tmp/bin"
for missing in docker jq openssl go node npx curl; do
  rm -rf "$bin"
  mkdir "$bin"
  for t in docker jq openssl go node npx curl; do
    if [ "$t" != "$missing" ]; then printf '#!/bin/sh\n' >"$bin/$t"; chmod +x "$bin/$t"; fi
  done
  rc=0
  out=$(PATH="$bin" "$BASH" "$here/run.sh" 2>&1) || rc=$?
  if [ "$rc" = 2 ] && grep -q "$missing is required" <<<"$out"; then
    echo "ok   run.sh names the missing tool: $missing"
  else
    echo "FAIL run.sh without $missing: exit $rc, output: $(head -c 200 <<<"$out" | tr '\n' ' ')"
    fail=1
  fi
done

exit "$fail"
