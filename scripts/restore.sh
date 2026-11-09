#!/usr/bin/env bash
# Puts one database back from a backup:  ./scripts/restore.sh ~/hxvy-backups/2026-11-09_0330 auth_db
#
# It REPLACES that database's tables with what the backup has, so it asks first. It is built so
# that a failed restore never leaves things worse than they were:
#   1. the backup file is read through before anything is touched (a damaged file stops here);
#   2. the current database is saved to a "pre-restore" folder next to the backups;
#   3. the whole replacement runs in ONE transaction, so it either completes or changes nothing;
#   4. the service is started again whatever happens, including on Ctrl+C or an error.
set -euo pipefail
cd "$(dirname "$0")/.."
COMPOSE="docker compose -f docker-compose.prod.yml"
dir="${1:-}"; db="${2:-}"
case "$db" in auth_db|workout_db|nutrition_db|activity_db|social_db) ;; *)
  echo "usage: $0 <backup folder> <auth_db|workout_db|nutrition_db|activity_db|social_db>" >&2; exit 2 ;;
esac
dump="$dir/$db.dump"
[ -s "$dump" ] || { echo "no $db.dump in $dir" >&2; exit 1; }

user="$(grep '^POSTGRES_USER=' .env 2>/dev/null | cut -d= -f2 || true)"; user="${user:-postgres}"
role="${db%_db}_svc"
service="${db%_db}-service"

# 1. Can the file be read at all? Nothing has been touched yet.
if ! $COMPOSE exec -T postgres pg_restore --list < "$dump" > /dev/null 2>&1; then
  echo "$dump cannot be read (damaged or not a backup). Nothing was changed." >&2
  exit 1
fi

read -r -p "Replace $db with the copy in $dir ? Everything added since will be lost. Type the database name to go on: " answer
[ "$answer" = "$db" ] || { echo "Cancelled."; exit 1; }

# 2. A copy of what is there now, in case the backup turns out to be the wrong one.
safety_dir="${BACKUP_DIR:-$HOME/hxvy-backups}/pre-restore-$(date -u +%Y-%m-%d_%H%M%S)"
mkdir -p "$safety_dir"; chmod 700 "$safety_dir"
$COMPOSE exec -T postgres pg_dump -U "$user" -Fc "$db" > "$safety_dir/$db.dump"
echo "The current $db is saved in $safety_dir"

# 4. Whatever happens from here, the service must be running again at the end.
restart_service() {
  if ! $COMPOSE start "$service" > /dev/null; then
    echo "WARNING: $service could not be started. Run:  $COMPOSE start $service" >&2
  fi
}
trap restart_service EXIT

# The service is stopped meanwhile, so nothing writes while the tables are replaced.
$COMPOSE stop "$service" > /dev/null

# 3. One transaction: if any statement fails, everything is rolled back and the database is as it was.
if $COMPOSE exec -T postgres pg_restore -U "$user" -d "$db" \
     --single-transaction --exit-on-error --clean --if-exists --no-owner --role="$role" < "$dump"; then
  echo "Restored $db."
else
  echo "The restore FAILED and was rolled back: $db is exactly as it was. $service is being started again." >&2
  exit 1
fi
