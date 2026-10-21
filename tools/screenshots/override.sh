#!/usr/bin/env bash
# Sourced by run.sh and check-isolation.sh.
#
# The compose file reads DATABASE_URL, APP_SECRET and BASE_URL from
# deploy/.env, which may point at a real (for example Supabase) database. The
# throwaway stack must never use those, so run.sh layers an override file over
# the compose file: `environment:` beats `env_file:`.

# write_override FILE PORT PASSWORD: writes a compose override (mode 0600) that
# points the upsera service at the throwaway postgres container with a fresh
# APP_SECRET and a plain-http BASE_URL (an https one would make the browser
# drop the secure session cookie on http://localhost).
write_override() {
  local file=$1 port=$2 pw=$3 secret
  secret=$(openssl rand -base64 32)
  (
    umask 077
    cat >"$file" <<EOF
services:
  upsera:
    environment:
      DATABASE_URL: "postgres://upsera:${pw}@postgres:5432/upsera?sslmode=disable"
      APP_SECRET: "${secret}"
      BASE_URL: "http://localhost:${port}"
EOF
  )
}

# assert_isolated PORT: reads `docker compose config --format json` on stdin and
# succeeds only when the upsera service uses the throwaway database, a
# generated secret and the throwaway URL. Prints nothing, so no secret leaks.
assert_isolated() {
  jq -e --arg port "$1" '
    .services.upsera.environment as $e
    | ($e.DATABASE_URL | test("@postgres:5432/"))
      and ($e.BASE_URL == "http://localhost:" + $port)
      and (($e.APP_SECRET | length) >= 32)' >/dev/null
}
