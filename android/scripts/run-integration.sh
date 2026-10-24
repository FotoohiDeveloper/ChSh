#!/usr/bin/env bash
# Runs the :core tests against a real backend: builds ../backend, starts it on a throw-away database
# (mock identity/SMS providers, 4-second access tokens so token refresh is exercised), then runs Gradle.
#
# Needs: go, psql, redis, a PostgreSQL superuser, JDK 17+.   (No Android SDK needed.)
# DESTROYS the database named in IT_DATABASE_URL — never point it at real data.
#   IT_PG_SUPER      default postgres://postgres@localhost:5432/postgres
#   IT_DATABASE_URL  default postgres://postgres@localhost:5432/<IT_DB_NAME>?sslmode=disable
#   IT_DB_NAME       default chador_android_it
#   IT_REDIS_URL     default redis://localhost:6379/2
#   MAVEN_MIRROR     optional Maven Central mirror (see settings-core.gradle.kts)
set -euo pipefail
cd "$(dirname "$0")/.."
ANDROID_DIR="$PWD"
BACKEND="$(cd ../backend && pwd)"

SUPER="${IT_PG_SUPER:-postgres://postgres@localhost:5432/postgres}"
DB="${IT_DB_NAME:-chador_android_it}"
DBURL="${IT_DATABASE_URL:-postgres://postgres@localhost:5432/$DB?sslmode=disable}"
REDIS="${IT_REDIS_URL:-redis://localhost:6379/2}"
PORT="${IT_PORT:-18081}"
W="$(mktemp -d)"
finish() {
  rc=$?
  if [ $rc -ne 0 ] && [ -f "$W/api.log" ]; then echo "== backend log (last errors)"; grep -E '"level":"(ERROR|WARN)"' "$W/api.log" "$W/worker.log" | tail -15; fi
  kill $(jobs -p) 2>/dev/null || true; rm -rf "$W"; exit $rc
}
trap finish EXIT

echo "== database $DB"
psql "$SUPER" -qAt -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB" >/dev/null
redis-cli -u "$REDIS" FLUSHDB >/dev/null

echo "== backend"
(cd "$BACKEND" && go build -o "$W/chador" ./cmd/chador)
export APP_ENV=dev HTTP_ADDR="127.0.0.1:$PORT" DATABASE_URL="$DBURL" REDIS_URL="$REDIS" TRUST_PROXY=false
export PII_ENC_KEY="$(openssl rand -base64 32)" PII_IDX_KEY="$(openssl rand -base64 32)"
export ACCESS_TTL=4s REFRESH_TTL=1h INQUIRY_MODE=mock SMS_MODE=mock
"$W/chador" migrate >/dev/null
ADMIN_PW="$("$W/chador" create-admin --username admin | sed -n 's/^one-time password: //p')"
"$W/chador" serve >"$W/api.log" 2>&1 &
"$W/chador" worker >"$W/worker.log" 2>&1 &
for _ in $(seq 1 50); do curl -fs "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fs "http://127.0.0.1:$PORT/readyz" >/dev/null || { echo "backend did not start"; cat "$W/api.log"; exit 1; }

echo "== gradle :core:test"
cd "$ANDROID_DIR"
CHADOR_BASE_URL="http://127.0.0.1:$PORT" CHADOR_ADMIN_USER=admin CHADOR_ADMIN_PASSWORD="$ADMIN_PW" \
  ${GRADLE:-./gradlew} -c settings-core.gradle.kts :core:cleanTest :core:test --console=plain "$@"
echo "== backend log: errors (should be none)"
grep -c '"level":"ERROR"' "$W/api.log" "$W/worker.log" || true
