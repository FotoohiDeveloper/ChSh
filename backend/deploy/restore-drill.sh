#!/usr/bin/env bash
# Proves a backup can actually be restored: loads it into a scratch database and counts rows.
# Usage: ./restore-drill.sh backups/chador-YYYYMMDD-HHMMSS.dump
set -euo pipefail
cd "$(dirname "$0")"
f="${1:?usage: restore-drill.sh <dump file>}"
dc() { docker compose exec -T postgres "$@"; }
dc psql -U chador_owner -d postgres -c "DROP DATABASE IF EXISTS chador_drill" -c "CREATE DATABASE chador_drill"
dc pg_restore -U chador_owner -d chador_drill --no-owner < "$f"
dc psql -U chador_owner -d chador_drill -At -c "SELECT 'tents', count(*) FROM tents UNION ALL SELECT 'assignments', count(*) FROM assignments UNION ALL SELECT 'pilgrims', count(*) FROM pilgrims UNION ALL SELECT 'events', count(*) FROM tent_events"
dc psql -U chador_owner -d postgres -c "DROP DATABASE chador_drill"
echo "restore drill OK"
