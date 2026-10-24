#!/usr/bin/env bash
# Creates deploy/secrets/* with strong random values. Never overwrites existing files.
set -euo pipefail
cd "$(dirname "$0")"
umask 077
mkdir -p secrets && chmod 700 secrets
rand() { openssl rand -base64 33 | tr -d '/+=\n' | cut -c1-40; }
put() { # name value
  if [ -e "secrets/$1" ]; then echo "keep   secrets/$1 (exists)"; else printf '%s' "$2" > "secrets/$1"; echo "create secrets/$1"; fi
}
OWNER_PW="${OWNER_PW:-$(rand)}"; APP_PW="${APP_PW:-$(rand)}"; REDIS_PW="${REDIS_PW:-$(rand)}"
put owner_db_password "$OWNER_PW"
put app_db_password   "$APP_PW"
put redis_password    "$REDIS_PW"
put database_url        "postgres://chador_app:$(cat secrets/app_db_password)@postgres:5432/chador?sslmode=disable"
put migrate_database_url "postgres://chador_owner:$(cat secrets/owner_db_password)@postgres:5432/chador?sslmode=disable"
put redis_url           "redis://:$(cat secrets/redis_password)@redis:6379/0"
put pii_enc_key "$(openssl rand -base64 32)"
put pii_idx_key "$(openssl rand -base64 32)"
[ -e secrets/inquiry_token ] || : > secrets/inquiry_token
[ -e secrets/sms_token ]     || : > secrets/sms_token
# Files are readable inside containers (non-root user); the directory is what protects them on the host.
chmod 644 secrets/*
cat <<MSG

Done. IMPORTANT:
  * Back up secrets/pii_enc_key and secrets/pii_idx_key somewhere safe and OFF this server.
    If they are lost, every encrypted national ID / mobile number becomes unreadable for good.
  * Put the vendor tokens into secrets/inquiry_token and secrets/sms_token (then set INQUIRY_MODE/SMS_MODE=http in .env).
MSG
