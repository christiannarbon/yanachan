#!/usr/bin/env bash
# Boots the built binary in server mode against a running Redis/Valkey and
# checks what only a real process shows: the startup refusal, the Redis ping,
# the routes server mode registers, and readiness following Redis.
#
#   GHDASH_REDIS_URL=redis://127.0.0.1:6379/0 GHDASH_REDIS_PASSWORD=... \
#     scripts/smoke-server-mode.sh <binary> <valkey container name>
set -euo pipefail

bin=$1
container=$2
: "${GHDASH_REDIS_URL:?set GHDASH_REDIS_URL}"
: "${GHDASH_REDIS_PASSWORD:?set GHDASH_REDIS_PASSWORD}"
port=${SMOKE_PORT:-19181}
base="http://127.0.0.1:$port"
logs=$(mktemp -d)

fail() {
  echo "::error::$*"
  for f in "$logs"/*.log; do echo "--- $f"; cat "$f"; done
  exit 1
}

run() {
  exec env -u GH_TOKEN -u GITHUB_TOKEN -u GHDASH_GITHUB_TOKEN \
    GHDASH_AUTH_MODE=server \
    GITHUB_CLIENT_ID=smoke-client-id \
    GITHUB_CLIENT_SECRET=smoke-placeholder \
    GHDASH_PUBLIC_URL="$base" \
    GHDASH_SESSION_KEYS="$(openssl rand -base64 32)" \
    GHDASH_ALLOWED_ORGS=smoke-org \
    GHDASH_ADDR="127.0.0.1:$port" \
    "$@"
}

# A half-configured server mode refuses to start and says why.
if env -u GH_TOKEN -u GITHUB_TOKEN -u GHDASH_GITHUB_TOKEN GHDASH_AUTH_MODE=server \
  "$bin" >"$logs/refuse.log" 2>&1; then
  fail "server mode started without its configuration"
fi
grep -q 'config: GITHUB_CLIENT_ID is required' "$logs/refuse.log" || fail "no config: line for the client ID"
grep -q 'config: server mode needs an access policy' "$logs/refuse.log" || fail "no config: line for the policy"
echo "  refuses to start half-configured"

# A wrong Redis password stops it at startup, not at the first sign-in.
if (GHDASH_REDIS_PASSWORD=wrong-password run "$bin") >"$logs/wrongpass.log" 2>&1; then
  fail "server mode started with a wrong Redis password"
fi
grep -q 'connect to redis' "$logs/wrongpass.log" || fail "no Redis error at startup"
grep -q 'wrong-password' "$logs/wrongpass.log" && fail "the Redis password reached the log"
echo "  refuses to start without Redis"

if curl -s -o /dev/null "$base/"; then
  fail "something already listens on port $port"
fi
(run "$bin") >"$logs/server.log" 2>&1 &
server=$!
trap 'kill "$server" 2>/dev/null || true; docker unpause "$container" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 30); do
  curl -sf "$base/api/ready" >/dev/null && break
  sleep 1
done
test "$(curl -s -o /dev/null -w '%{http_code}' "$base/api/ready")" = 200 || fail "not ready with Redis up"
echo "  ready"

curl -sf "$base/api/auth/status" | grep -q '"authMode":"server"' || fail "status does not say server mode"
loc=$(curl -s -o /dev/null -w '%{redirect_url}' "$base/api/auth/login")
case "$loc" in
  https://github.com/login/oauth/authorize\?*) ;;
  *) fail "login did not redirect to GitHub: $loc" ;;
esac
echo "  sign-in goes to GitHub"

test "$(curl -s -o /dev/null -w '%{http_code}' "$base/api/settings")" = 401 || fail "settings without a session is not 401"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/api/auth/device/start")
test "$code" = 404 || fail "a local-only route answered $code"
echo "  needs a session; local-only routes are gone"

docker pause "$container" >/dev/null
sleep 1
read -r code took < <(curl -s -o /dev/null -w '%{http_code} %{time_total}\n' "$base/api/ready")
test "$code" = 503 || fail "ready answered $code while Redis is unreachable"
awk -v t="$took" 'BEGIN { exit !(t < 1.5) }' || fail "readiness took ${took}s; its timeout is one second"
test "$(curl -s -o /dev/null -w '%{http_code}' "$base/api/health")" = 200 || fail "health followed Redis down"
docker unpause "$container" >/dev/null
for _ in $(seq 1 10); do
  test "$(curl -s -o /dev/null -w '%{http_code}' "$base/api/ready")" = 200 && break
  sleep 1
done
test "$(curl -s -o /dev/null -w '%{http_code}' "$base/api/ready")" = 200 || fail "not ready again after Redis came back"
echo "  readiness follows Redis; health does not"

kill -TERM "$server"
wait "$server" || true
grep -q 'shutting down' "$logs/server.log" || fail "no clean shutdown"
echo "  shuts down cleanly"
