#!/usr/bin/env bash
# Saves every database to a dated folder:  ./scripts/backup.sh
# Keeps the last 14 days. Run it every night with cron (see DEPLOY.md). The folder is
# BACKUP_DIR (default ~/hxvy-backups); also copy it off this server now and then, because a
# backup on the same disk does not survive the disk.
set -euo pipefail
cd "$(dirname "$0")/.."
COMPOSE="docker compose -f docker-compose.prod.yml"
DIR="${BACKUP_DIR:-$HOME/hxvy-backups}"
KEEP_DAYS="${KEEP_DAYS:-14}"
stamp="$(date -u +%Y-%m-%d_%H%M)"
dest="$DIR/$stamp"
mkdir -p "$dest"
chmod 700 "$DIR" "$dest"

user="$(grep '^POSTGRES_USER=' .env 2>/dev/null | cut -d= -f2 || true)"
user="${user:-postgres}"
for db in auth_db workout_db nutrition_db activity_db social_db; do
  # -Fc: compressed, and pg_restore can take back one table or the whole thing.
  $COMPOSE exec -T postgres pg_dump -U "$user" -Fc "$db" > "$dest/$db.dump.part"
  mv "$dest/$db.dump.part" "$dest/$db.dump"
done
echo "Backup saved: $dest ($(du -sh "$dest" | cut -f1))"

find "$DIR" -mindepth 1 -maxdepth 1 -type d -mtime +"$KEEP_DAYS" -exec rm -rf {} +
