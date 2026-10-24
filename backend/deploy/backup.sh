#!/usr/bin/env bash
# Logical backup of the database. Personal data inside is already encrypted with
# keys that are NOT in the dump, so a leaked backup alone does not expose it.
# Cron example (every 3 hours):  0 */3 * * *  /opt/chador/deploy/backup.sh >> /var/log/chador-backup.log 2>&1
set -euo pipefail
cd "$(dirname "$0")"
OUT="${BACKUP_DIR:-./backups}"; KEEP_DAYS="${KEEP_DAYS:-14}"
umask 077; mkdir -p "$OUT"
ts="$(date +%Y%m%d-%H%M%S)"; file="$OUT/chador-$ts.dump"
docker compose exec -T postgres pg_dump -U chador_owner -Fc --no-owner chador > "$file.tmp"
# a dump that cannot be listed is not a backup
docker compose exec -T postgres pg_restore --list < "$file.tmp" > /dev/null
mv "$file.tmp" "$file"
if [ -n "${BACKUP_PASSPHRASE_FILE:-}" ]; then
  openssl enc -aes-256-cbc -pbkdf2 -salt -pass "file:$BACKUP_PASSPHRASE_FILE" -in "$file" -out "$file.enc" && rm "$file"
  file="$file.enc"
fi
find "$OUT" -name 'chador-*.dump*' -mtime "+$KEEP_DAYS" -delete
echo "$(date -Is) backup ok: $file ($(du -h "$file" | cut -f1))"
