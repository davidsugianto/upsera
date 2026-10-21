#!/usr/bin/env bash
# Brings up a throwaway Upsera stack (compose project upsera-shots, port 3181),
# loads the demo data, drives a browser through the pages and writes the PNGs
# to site/public/screenshots/. The stack is always torn down (down -v) on exit.
# deploy/.env is only read by docker compose, never printed or copied, and its
# DATABASE_URL, APP_SECRET and BASE_URL are overridden (see override.sh), so
# the stack never touches a real database even when .env points at one.
set -euo pipefail

# Name a missing tool up front; check-isolation.sh keeps this list in sync.
for tool in docker jq openssl go node npx curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "blocked: $tool is required (see tools/screenshots/README.md)" >&2; exit 2; }
done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
here="$root/tools/screenshots"
project=upsera-shots
port=3181
# shellcheck source=tools/screenshots/override.sh
source "$here/override.sh"

# colima: use its socket when Docker is not configured already.
colima_sock="$HOME/.config/colima/default/docker.sock"
if [ -z "${DOCKER_HOST:-}" ] && [ -S "$colima_sock" ]; then
  export DOCKER_HOST="unix://$colima_sock"
  export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
fi

if [ ! -f "$root/deploy/.env" ]; then
  echo "blocked: deploy/.env is missing (run 'make env' first)" >&2
  exit 2
fi

tmp=$(mktemp -d)
db_password=$(openssl rand -hex 16)
write_override "$tmp/override.yml" "$port" "$db_password"

dc() {
  UPSERA_PORT="$port" POSTGRES_PASSWORD="$db_password" docker compose -p "$project" \
    -f "$root/deploy/docker-compose.yml" -f "$tmp/override.yml" --profile local-db "$@"
}

# Refuse to start anything unless the effective config is the throwaway one.
if ! dc config --format json | assert_isolated "$port"; then
  echo "refusing to start: the $project stack would not be isolated from deploy/.env" >&2
  rm -rf "$tmp"
  exit 2
fi

cleanup() {
  status=$?
  trap - EXIT
  echo "==> tearing down $project"
  dc down -v --remove-orphans >/dev/null 2>&1 || echo "warning: could not tear down $project" >&2
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT

echo "==> building upsctl"
(cd "$root" && CGO_ENABLED=0 go build -o "$tmp/upsctl" ./cmd/upsctl)

echo "==> node dependencies"
if [ ! -d "$here/node_modules" ]; then (cd "$here" && npm ci); fi
(cd "$here" && npx --no-install playwright install chromium >/dev/null)

echo "==> starting a clean $project stack on :$port"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build

export UPSERA_URL="http://localhost:$port"
for _ in $(seq 1 120); do
  if curl -fsS "$UPSERA_URL/api/setup" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "$UPSERA_URL/api/setup" >/dev/null

echo "==> creating the demo user and API token"
eval "$(node "$here/setup.mjs")"

echo "==> applying deploy/examples/demo.yaml"
# Placeholder secrets for the example channels; nothing is ever sent to them.
export UPSERA_DEMO_OPS_TOKEN=demo-token
export UPSERA_DEMO_SLACK_URL=https://hooks.example.org/services/demo
"$tmp/upsctl" apply -f "$root/deploy/examples/demo.yaml" --yes >/dev/null
node "$here/wait.mjs" initial

echo "==> loading 30 days of demo history"
tz=$(sed -n 's/^TZ=//p' "$root/deploy/.env" | head -n 1 | tr -d "\"'")
tz=${tz:-UTC}
dc exec -T postgres psql -q -U upsera -d upsera -v ON_ERROR_STOP=1 -v tz="$tz" -f - <"$here/seed.sql"

echo "==> restarting upsera to pick up the seeded state"
restarted_ms=$(($(date +%s) * 1000))
dc restart upsera >/dev/null
for _ in $(seq 1 120); do
  if curl -fsS "$UPSERA_URL/api/setup" >/dev/null 2>&1; then break; fi
  sleep 1
done
node "$here/wait.mjs" seeded "$restarted_ms"

echo "==> capturing screenshots"
(cd "$here" && node capture.mjs "$root/site/public/screenshots")
