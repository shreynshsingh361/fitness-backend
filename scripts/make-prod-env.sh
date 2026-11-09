#!/usr/bin/env bash
# Writes .env for a real server, with fresh random secrets.
#
#   ./scripts/make-prod-env.sh api.203-0-113-7.sslip.io hxvy-32756
#                              └ the name of this server  └ your Firebase project id
#
# Refuses to overwrite an existing .env: the database passwords in it were set the first time
# Postgres started, so replacing them would lock the services out of their databases.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${1:-}"
firebase="${2:-}"
if [ -z "$host" ] || [ -z "$firebase" ]; then
  echo "usage: $0 <api host name> <firebase project id>" >&2
  echo "   e.g. $0 api.203-0-113-7.sslip.io hxvy-32756" >&2
  exit 2
fi
case "$host" in *[!a-zA-Z0-9.-]*|"") echo "the host name may only have letters, digits, dots and dashes" >&2; exit 2 ;; esac
if [ -e .env ]; then
  echo ".env already exists. Not touching it. (To start over, move it away and delete the pgdata volume.)" >&2
  exit 1
fi

secret() { openssl rand -hex "${1:-24}"; }
umask 077
cat > .env <<ENV
# Made by scripts/make-prod-env.sh on $(date -u +%Y-%m-%dT%H:%MZ). NEVER commit this file.
API_HOST=$host
FIREBASE_PROJECT_ID=$firebase
JWT_SECRET=$(secret 32)
POSTGRES_PASSWORD=$(secret)
AUTH_DB_PASSWORD=$(secret)
WORKOUT_DB_PASSWORD=$(secret)
NUTRITION_DB_PASSWORD=$(secret)
ACTIVITY_DB_PASSWORD=$(secret)
SOCIAL_DB_PASSWORD=$(secret)
ENV
echo "Wrote .env (only you can read it). Keep a copy of it somewhere safe, apart from this server:"
echo "without the database passwords a lost server cannot be rebuilt from a backup."
