#!/usr/bin/env bash
# End-to-end smoke test of the real binary, configured the way production is:
# APP_ENV=prod, secrets read from files, separate owner/runtime database roles,
# HTTP vendors (a local stub stands in for the identity and SMS services),
# real worker process, real tent list from the label generator.
#
# Needs: go, curl, jq, python3, psql, a PostgreSQL superuser URL and a Redis.
#   SMOKE_PG_SUPER   default postgres://postgres@localhost:5433/postgres?host=/tmp
#   SMOKE_REDIS_URL  default redis://localhost:6380/1
set -euo pipefail
cd "$(dirname "$0")/.."

SUPER="${SMOKE_PG_SUPER:-postgres://postgres@localhost:5433/postgres?host=/tmp}"
REDIS="${SMOKE_REDIS_URL:-redis://localhost:6380/1}"
CSV="${SMOKE_TENT_CSV:-../labels/output/tent_ids.csv}"
APP_URL="${SMOKE_APP_URL:-postgres://chador_app@localhost:5433/chador_smoke?host=/tmp&sslmode=disable}"
OWNER_URL="${SMOKE_OWNER_URL:-postgres://chador_owner@localhost:5433/chador_smoke?host=/tmp&sslmode=disable}"
API=http://127.0.0.1:18080
W="$(mktemp -d)"
PIDS=()
pass=0; fail=0

cleanup() { for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$W"; }
trap cleanup EXIT
ok()   { pass=$((pass+1)); echo "  ok   $1"; }
bad()  { fail=$((fail+1)); echo "  FAIL $1"; }
check(){ if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (got '$1', want '$2')"; fi; }

echo "== prepare database ($SUPER)"
psql "$SUPER" -qAt <<SQL
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='chador_owner') THEN CREATE ROLE chador_owner LOGIN; END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='chador_app')   THEN CREATE ROLE chador_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE; END IF;
END \$\$;
DROP DATABASE IF EXISTS chador_smoke;
CREATE DATABASE chador_smoke OWNER chador_owner;
SQL
psql "$SUPER" -qAt -c "REVOKE ALL ON DATABASE chador_smoke FROM PUBLIC" -c "GRANT CONNECT ON DATABASE chador_smoke TO chador_app" >/dev/null
redis-cli -u "$REDIS" FLUSHDB >/dev/null

echo "== build"
go build -o "$W/chador" ./cmd/chador
printf '%s' "$APP_URL" > "$W/database_url"; printf '%s' "$OWNER_URL" > "$W/migrate_url"; printf '%s' "$REDIS" > "$W/redis_url"
openssl rand -base64 32 | tr -d '\n' > "$W/enc"; openssl rand -base64 32 | tr -d '\n' > "$W/idx"
printf 'stub-token' > "$W/vendor_token"

export APP_ENV=prod APP_TZ=Asia/Tehran HTTP_ADDR=127.0.0.1:18080 TRUST_PROXY=false AUTO_MIGRATE=false APP_DB_ROLE=chador_app
export DATABASE_URL_FILE="$W/database_url" MIGRATE_DATABASE_URL_FILE="$W/migrate_url" REDIS_URL_FILE="$W/redis_url"
export PII_ENC_KEY_FILE="$W/enc" PII_IDX_KEY_FILE="$W/idx" INQUIRY_TOKEN_FILE="$W/vendor_token" SMS_TOKEN_FILE="$W/vendor_token"
export INQUIRY_MODE=http INQUIRY_URL=http://127.0.0.1:18099/verify SMS_MODE=http SMS_URL=http://127.0.0.1:18099/send
export UNDO_WINDOW=3m REMINDER_AFTER=6h

echo "== configuration guards"
set +e
APP_ENV=prod INQUIRY_MODE=mock "$W/chador" migrate >"$W/out" 2>&1; rc=$?
[ $rc -ne 0 ] && grep -q "mock" "$W/out" && ok "prod refuses mock providers" || bad "prod accepted mock providers"
PII_ENC_KEY_FILE=/nonexistent "$W/chador" migrate >"$W/out" 2>&1; rc=$?
[ $rc -ne 0 ] && ok "refuses to start without encryption key" || bad "started without key"
cp "$W/enc" "$W/same"; PII_IDX_KEY_FILE="$W/same" "$W/chador" migrate >"$W/out" 2>&1; rc=$?
[ $rc -ne 0 ] && ok "refuses identical encryption/index keys" || bad "accepted identical keys"
set -e

echo "== migrate, bootstrap"
"$W/chador" migrate 2>&1 | tail -1 | jq -r .msg | grep -q "migrations applied" && ok "migrate (as owner role)" || bad migrate
"$W/chador" migrate 2>&1 | tail -1 | jq -e '(.files // []) == []' >/dev/null && ok "migrate is idempotent" || bad "migrate not idempotent"
ADMIN_OUT="$("$W/chador" create-admin --username admin --name "مدیر سامانه")"
ADMIN_PW="$(echo "$ADMIN_OUT" | sed -n 's/^one-time password: //p')"
[ -n "$ADMIN_PW" ] && ok "admin created" || bad "admin not created"
"$W/chador" create-admin --username admin2 >/dev/null 2>&1 && bad "second bootstrap admin allowed" || ok "bootstrap refuses a second admin"
"$W/chador" seed-places >/dev/null && ok "seed-places"
IMPORT="$("$W/chador" import-tents --file "$CSV")"
echo "     $IMPORT"
echo "$IMPORT" | grep -q "1015 newly registered" && ok "label CSV imported (1015 tents)" || bad "tent import"

echo "== start stub vendor, api, worker"
if (exec 3<>/dev/tcp/127.0.0.1/18099) 2>/dev/null; then echo "port 18099 is already in use (leftover stub?)"; exit 1; fi
python3 scripts/stub_vendor.py 18099 "$W/vendor.log" & PIDS+=($!)
METRICS_ADDR=127.0.0.1:19090 "$W/chador" serve >"$W/api.log" 2>&1 & API_PID=$!; PIDS+=($API_PID)
"$W/chador" worker >"$W/worker.log" 2>&1 & WORKER_PID=$!; PIDS+=($WORKER_PID)
for i in $(seq 1 50); do curl -fs "$API/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
check "$(curl -s -o /dev/null -w '%{http_code}' $API/readyz)" 200 "readyz"

j() { curl -s -H 'Content-Type: application/json' "$@"; }
login() { j -X POST $API/api/v1/auth/login -d "{\"username\":\"$1\",\"password\":\"$2\"}" | jq -r '.tokens.access_token'; }
authed() { local tok="$1"; shift; curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' "$@"; }
setpw() { # user temp new
  local t; t="$(login "$1" "$2")"
  authed "$t" -X POST $API/api/v1/auth/change-password -d "{\"old_password\":\"$2\",\"new_password\":\"$3\"}" >/dev/null
  login "$1" "$3"
}

echo "== admin"
ADMIN="$(setpw admin "$ADMIN_PW" 'Admin-passphrase-1234')"
PLACES="$(authed "$ADMIN" $API/api/v1/places)"
GATE_ID="$(echo "$PLACES" | jq '[.[]|select(.kind=="gate")][0].id')"
DOOR_ID="$(authed "$ADMIN" -X POST $API/api/v1/admin/places/$GATE_ID/doors -d '{"name":"درب ۱"}' | jq .id)"
check "$(echo "$PLACES" | jq length)" 5 "five places seeded (3 gates, storage, workshop)"
SRV="$(authed "$ADMIN" -X POST $API/api/v1/admin/users -d '{"username":"khadem01","full_name":"خادم نمونه","role":"server"}')"
OFF="$(authed "$ADMIN" -X POST $API/api/v1/admin/users -d '{"username":"office01","full_name":"دفتر","role":"office"}')"
SRV_PW="$(echo "$SRV" | jq -r .temp_password)"; OFF_PW="$(echo "$OFF" | jq -r .temp_password)"
authed "$ADMIN" -X PUT $API/api/v1/admin/settings/socks_unit_price -d 25000 -o /dev/null
authed "$ADMIN" -X POST $API/api/v1/admin/tents -d '{"codes":["CH-009999"]}' | jq -e '.created==1' >/dev/null && ok "admin registers a tent" || bad "admin tents"

echo "== a server at the gate"
S="$(setpw khadem01 "$SRV_PW" 'Server-passphrase-5678')"
check "$(authed "$S" -o /dev/null -w '%{http_code}' $API/api/v1/admin/users)" 403 "server cannot list users"
check "$(authed "$S" -X POST $API/api/v1/shifts/start -d "{\"place_id\":$GATE_ID,\"door_id\":$DOOR_ID}" | jq -r .place_name)" "$(echo "$PLACES" | jq -r "[.[]|select(.id==$GATE_ID)][0].name")" "shift started"
NID=0084575948
PIL="$(authed "$S" -X POST $API/api/v1/pilgrims -d "{\"national_id\":\"$NID\",\"birth_date\":\"1370/05/12\",\"mobile\":\"09123456789\"}")"
PID="$(echo "$PIL" | jq -r .id)"
check "$(echo "$PIL" | jq -r .verification)" verified "pilgrim verified by the (stub) identity service"
check "$(echo "$PIL" | jq -r .full_name)" "علی رضایی" "name returned by the identity service"
check "$(echo "$PIL" | jq -r .national_id_masked)" "008****948" "national id masked in the response"
ASG="$(authed "$S" -X POST $API/api/v1/assignments -d "{\"pilgrim_id\":\"$PID\",\"codes\":[\"CH-000001\",\"CH-000002\",\"CH-000003\"],\"socks_qty\":2,\"op_id\":\"7d6f3a52-0000-4000-8000-000000000001\"}")"
check "$(echo "$ASG" | jq '.items|length')" 3 "three tents handed out"
check "$(echo "$ASG" | jq .socks_amount)" 50000 "socks priced from the admin setting"
check "$(authed "$S" -X POST $API/api/v1/assignments -d "{\"pilgrim_id\":\"$PID\",\"codes\":[\"CH-000001\"]}" | jq -r .error.code)" tents_unavailable "double hand-out refused"
check "$(authed "$S" $API/api/v1/tents/CH-000002 | jq -r .status)" with_pilgrim "scan shows tent with pilgrim"
RET="$(authed "$S" -X POST $API/api/v1/returns -d '{"items":[{"code":"CH-000001"},{"code":"CH-000002","status":"dirty"},{"code":"CH-000003","status":"repair"}]}')"
check "$(echo "$RET" | jq .completed_pilgrims)" 1 "pilgrim completed"
sleep 2 # let the worker deliver the SMS jobs

echo "== worker + vendors"
check "$(grep -c '"path": "/send"' "$W/vendor.log" || true)" 2 "two SMS sent (delivered, returned)"
grep '"path": "/send"' "$W/vendor.log" | jq -r '.body.to' | sort -u | grep -qx "09123456789" && ok "SMS sent to the normalised mobile" || bad "SMS recipient"
grep -q "$NID" <(grep '"path": "/send"' "$W/vendor.log") && bad "national id leaked into an SMS" || ok "no national id in SMS text"
grep '"path": "/verify"' "$W/vendor.log" | jq -e '.auth=="Bearer stub-token" and .body.national_id=="0084575948" and .body.birth_date=="1991-08-03"' >/dev/null && ok "inquiry request: bearer token, Jalali birth date converted" || bad "inquiry request"

echo "== office, reports, metrics"
O="$(setpw office01 "$OFF_PW" 'Office-passphrase-9012')"
DASH="$(authed "$O" $API/api/v1/dashboard)"
check "$(echo "$DASH" | jq '.totals.dirty')" 1 "dashboard: 1 dirty"
check "$(echo "$DASH" | jq '.totals.repair')" 1 "dashboard: 1 repair"
check "$(echo "$DASH" | jq '[.totals[]]|add')" 1016 "dashboard counts all 1016 tents"
check "$(authed "$O" "$API/api/v1/reports/daily" | jq .assigned)" 3 "daily report: 3 assigned"
authed "$O" "$API/api/v1/reports/daily?format=xlsx" -o "$W/r.xlsx"; head -c2 "$W/r.xlsx" | grep -q PK && ok "xlsx report downloads" || bad "xlsx"
curl -s http://127.0.0.1:19090/metrics | grep -q 'chador_http_requests_total' && ok "metrics on the internal port" || bad "metrics"
check "$(curl -s -o /dev/null -w '%{http_code}' $API/metrics)" 404 "metrics are not served on the public port"

echo "== what the runtime role can do to the database"
check "$(psql "$APP_URL" -qAt -c "UPDATE audit_log SET action='x'" 2>&1 | grep -c 'permission denied')" 1 "runtime role cannot rewrite the audit log"
check "$(psql "$APP_URL" -qAt -c "DROP TRIGGER audit_log_append_only ON audit_log" 2>&1 | grep -c 'must be owner')" 1 "runtime role cannot drop the guard triggers"
check "$(psql "$OWNER_URL" -qAt -c "SELECT count(*) FROM pilgrims WHERE nid_enc IS NOT NULL AND position('$NID'::bytea in nid_enc)=0")" 1 "national id is encrypted in the table"

echo "== logs contain no secrets or personal data"
cat "$W/api.log" "$W/worker.log" > "$W/all.log"
for needle in "$NID" "09123456789" "$ADMIN_PW" "$SRV_PW" "Server-passphrase-5678" "Admin-passphrase-1234" "$(cat "$W/enc")" "$ADMIN" "$S"; do
  if grep -qF -- "$needle" "$W/all.log"; then bad "log contains: ${needle:0:6}..."; else ok "log is clean of ${needle:0:6}..."; fi
done
jq -e . "$W/api.log" >/dev/null 2>&1 && ok "logs are structured JSON" || bad "logs not JSON"

echo "== graceful shutdown"
kill -TERM "$API_PID"; for i in $(seq 1 60); do kill -0 "$API_PID" 2>/dev/null || break; sleep 0.25; done
kill -0 "$API_PID" 2>/dev/null && bad "api did not stop" || ok "api stopped on SIGTERM"
kill -TERM "$WORKER_PID"; for i in $(seq 1 60); do kill -0 "$WORKER_PID" 2>/dev/null || break; sleep 0.25; done
kill -0 "$WORKER_PID" 2>/dev/null && bad "worker did not stop" || ok "worker stopped on SIGTERM"

echo
echo "smoke: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
