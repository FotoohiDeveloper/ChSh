#!/usr/bin/env bash
# One-shot installer for the Chador backend on a fresh Debian/Ubuntu server (run as root).
#
#   curl -fsSLO https://raw.githubusercontent.com/FotoohiDeveloper/ChSh/ccr-8fe79f7c-fapwqg/backend/deploy/install.sh
#   DOMAIN=shch.badr-group.ir ACME_EMAIL=you@example.ir bash install.sh
#
# Safe to run again: existing secrets, database and admin are never overwritten.
# Optional environment variables:
#   DOMAIN         public host name (required)            ACME_EMAIL  e-mail for the TLS certificate
#   INSTALL_DIR    default /opt/ChSh                     BRANCH      default ccr-8fe79f7c-fapwqg
#   REPO           default https://github.com/FotoohiDeveloper/ChSh.git
#   INQUIRY_URL / INQUIRY_TOKEN   identity-inquiry vendor (national ID check)
#   SMS_URL / SMS_TOKEN           SMS vendor
#   GATES          comma-separated gate names to create, e.g. "گیت ۱,گیت ۲"   (only when no places exist yet)
#   IMPORT_TENTS=0 skip importing labels/output/tent_ids.csv (1000+ tent codes)
set -euo pipefail

DOMAIN="${DOMAIN:-}"
ACME_EMAIL="${ACME_EMAIL:-}"
INSTALL_DIR="${INSTALL_DIR:-/opt/ChSh}"
BRANCH="${BRANCH:-ccr-8fe79f7c-fapwqg}"
REPO="${REPO:-https://github.com/FotoohiDeveloper/ChSh.git}"
IMPORT_TENTS="${IMPORT_TENTS:-1}"

say()  { printf '\n\033[1;32m== %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m!! %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31mxx %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root"
[ -n "$DOMAIN" ] || die "set DOMAIN, e.g. DOMAIN=shch.badr-group.ir bash install.sh"
command -v apt-get >/dev/null || die "this script supports Debian/Ubuntu (apt)"

say "1/7 base packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git curl openssl ca-certificates dnsutils >/dev/null

say "2/7 Docker"
if ! docker compose version >/dev/null 2>&1; then
  # distro packages first (works on mirrors that block docker.com), official script as fallback
  apt-get install -y -qq docker.io docker-compose-v2 >/dev/null 2>&1 \
    || apt-get install -y -qq docker.io docker-compose-plugin >/dev/null 2>&1 \
    || { curl -fsSL https://get.docker.com | sh; }
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is not available; install it and run again"

say "3/7 DNS and firewall"
SERVER_IP="$(curl -fsS4 https://api.ipify.org 2>/dev/null || true)"
DNS_IP="$(dig +short A "$DOMAIN" | tail -1 || true)"
if [ -n "$SERVER_IP" ] && [ "$SERVER_IP" != "$DNS_IP" ]; then
  warn "$DOMAIN resolves to '${DNS_IP:-nothing}', this server is '$SERVER_IP'."
  warn "Create an A record  $DOMAIN -> $SERVER_IP  or the TLS certificate cannot be issued."
fi
if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
  ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null
fi

say "4/7 source code in $INSTALL_DIR"
if [ -d "$INSTALL_DIR/.git" ]; then
  git -C "$INSTALL_DIR" fetch -q origin "$BRANCH"
  git -C "$INSTALL_DIR" checkout -q "$BRANCH"
  git -C "$INSTALL_DIR" pull -q --ff-only origin "$BRANCH"
else
  git clone -q -b "$BRANCH" "$REPO" "$INSTALL_DIR"
fi
cd "$INSTALL_DIR/backend/deploy"

say "5/7 configuration and secrets"
if [ ! -f .env ]; then cp .env.example .env; fi
setenv() { # key value  (replace the line, or append)
  if grep -q "^$1=" .env; then sed -i "s|^$1=.*|$1=$2|" .env; else printf '%s=%s\n' "$1" "$2" >> .env; fi
}
setenv SITE_ADDRESS "$DOMAIN"
[ -n "$ACME_EMAIL" ] && setenv ACME_EMAIL "$ACME_EMAIL"
[ -n "${INQUIRY_URL:-}" ] && setenv INQUIRY_URL "$INQUIRY_URL"
[ -n "${SMS_URL:-}" ] && setenv SMS_URL "$SMS_URL"
mkdir -p pwa
./init-secrets.sh
[ -n "${INQUIRY_TOKEN:-}" ] && printf '%s' "$INQUIRY_TOKEN" > secrets/inquiry_token
[ -n "${SMS_TOKEN:-}" ] && printf '%s' "$SMS_TOKEN" > secrets/sms_token
if [ -z "${INQUIRY_URL:-}" ] || [ -z "${INQUIRY_TOKEN:-}" ]; then
  warn "identity-inquiry vendor not configured: hand-outs will fail identity checks until INQUIRY_URL/INQUIRY_TOKEN are set in .env and secrets/."
fi
if [ -z "${SMS_URL:-}" ] || [ -z "${SMS_TOKEN:-}" ]; then
  warn "SMS vendor not configured: reminder SMS will not be sent."
fi

say "6/7 build and start (first build takes a few minutes)"
docker compose up -d --build
for i in $(seq 1 60); do
  docker compose exec -T api /usr/local/bin/chador healthcheck >/dev/null 2>&1 && break
  [ "$i" -eq 60 ] && { docker compose ps; docker compose logs --tail 40 api; die "API did not become healthy"; }
  sleep 3
done

say "7/7 first-time data"
ADMIN_FILE="/root/chador-admin-password.txt"
if docker compose run --rm api create-admin --username admin > /tmp/chador-admin.out 2>&1; then
  sed -n 's/^one-time password: //p' /tmp/chador-admin.out > "$ADMIN_FILE"; chmod 600 "$ADMIN_FILE"
  echo "admin user created; one-time password saved in $ADMIN_FILE (you must change it at first login)"
else
  echo "admin user already exists (or creation failed):"; tail -2 /tmp/chador-admin.out
fi
rm -f /tmp/chador-admin.out
if [ -n "${GATES:-}" ]; then docker compose run --rm api seed-places --gates "$GATES" || warn "seed-places skipped (places may already exist)"; fi
CSV="$INSTALL_DIR/labels/output/tent_ids.csv"
if [ "$IMPORT_TENTS" = "1" ] && [ -f "$CSV" ]; then
  docker compose run --rm -v "$(dirname "$CSV"):/in:ro" api import-tents --file /in/tent_ids.csv
fi

cat <<MSG

Done.   https://$DOMAIN/            (certificate may take a minute on first start)
  health : curl -fsS https://$DOMAIN/readyz
  admin  : username 'admin', one-time password in $ADMIN_FILE
  logs   : cd $INSTALL_DIR/backend/deploy && docker compose logs -f api
  IMPORTANT: copy $INSTALL_DIR/backend/deploy/secrets/pii_enc_key and pii_idx_key to a safe place OFF this server.
             Lost keys = encrypted national IDs / phone numbers can never be read again.
  Backups  : $INSTALL_DIR/backend/deploy/backup.sh  (schedule it with cron)
MSG
