#!/usr/bin/env bash
# Smoke test a running Upsera: wait for it, create the first admin if the
# instance is fresh, log in, and read the dashboard API with the session.
#
#   deploy/smoke.sh [base-url]        # default http://localhost:${UPSERA_PORT:-3080}
#
# Environment:
#   SMOKE_EMAIL     admin email (default smoke@example.com)
#   SMOKE_PASSWORD  admin password; on a fresh instance one is generated and
#                   printed when unset, on a set-up instance it is required
#   SMOKE_TIMEOUT   seconds to wait for the server (default 120)
#
# Needs bash and curl. Session cookies stay in a temporary file that is
# removed on exit; they are never printed.
set -euo pipefail

base="${1:-http://localhost:${UPSERA_PORT:-3080}}"
base="${base%/}"
email="${SMOKE_EMAIL:-smoke@example.com}"
password="${SMOKE_PASSWORD:-}"
timeout="${SMOKE_TIMEOUT:-120}"

die() {
	echo "FAIL: $*" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || die "curl is required"

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
jar="$work/cookies"
body="$work/body"
: >"$jar"
chmod 600 "$jar"

# json_string prints $1 as a JSON string literal (inputs are checked for
# control characters up front, so escaping \ and " is enough).
json_string() {
	local s="$1"
	s="${s//\\/\\\\}"
	s="${s//\"/\\\"}"
	printf '"%s"' "$s"
}

# request METHOD PATH [JSON]: writes the response body to $body and prints
# the HTTP status. Cookies are read from and written to the jar.
request() {
	local method="$1" path="$2" data="${3:-}"
	local args=(--silent --show-error --max-time 10 --output "$body" --write-out '%{http_code}'
		--cookie "$jar" --cookie-jar "$jar" --request "$method")
	if [ -n "$data" ]; then
		args+=(--header 'Content-Type: application/json' --data-binary @-)
		printf '%s' "$data" | curl "${args[@]}" "$base$path"
	else
		curl "${args[@]}" "$base$path"
	fi
}

case "$email$password" in
*[[:cntrl:]]*) die "control characters are not supported in SMOKE_EMAIL/SMOKE_PASSWORD" ;;
esac

start=$SECONDS

echo "waiting for $base (up to ${timeout}s)"
until [ "$(request GET /healthz 2>/dev/null || true)" = "200" ]; do
	if [ $((SECONDS - start)) -ge "$timeout" ]; then
		die "$base/healthz did not answer 200 within ${timeout}s"
	fi
	sleep 2
done

status="$(request GET /api/setup)"
[ "$status" = "200" ] || die "GET /api/setup returned $status"
if grep -q '"needed":[[:space:]]*true' "$body"; then
	if [ -z "$password" ]; then
		password="smoke-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
		echo "generated admin password for $email: $password"
	fi
	payload="{\"email\":$(json_string "$email"),\"name\":\"Smoke Admin\",\"password\":$(json_string "$password"),\"team_name\":\"Smoke\"}"
	status="$(request POST /api/setup "$payload")"
	[ "$status" = "201" ] || die "POST /api/setup returned $status: $(head -c 300 "$body")"
	echo "created first admin $email"
	# Start from a clean jar so the login below is what gets tested.
	: >"$jar"
else
	[ -n "$password" ] || die "instance is already set up; set SMOKE_EMAIL and SMOKE_PASSWORD to an existing admin"
fi

payload="{\"email\":$(json_string "$email"),\"password\":$(json_string "$password")}"
status="$(request POST /api/auth/login "$payload")"
[ "$status" = "200" ] || die "POST /api/auth/login returned $status: $(head -c 300 "$body")"
echo "logged in as $email"

status="$(request GET /api/auth/me)"
[ "$status" = "200" ] || die "GET /api/auth/me returned $status"
team_id="$(tr -d '\n' <"$body" | sed -n 's/.*"teams":[[:space:]]*\[[[:space:]]*{[^}]*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p')"
[ -n "$team_id" ] || die "GET /api/auth/me listed no team"

status="$(request GET "/api/teams/$team_id/monitors")"
[ "$status" = "200" ] || die "GET /api/teams/$team_id/monitors returned $status"

status="$(request GET /)"
[ "$status" = "200" ] || die "GET / (dashboard) returned $status"
grep -qi '<html' "$body" || die "GET / did not return the dashboard HTML"

echo "OK: working dashboard API in $((SECONDS - start))s"
