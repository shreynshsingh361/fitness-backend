#!/usr/bin/env bash
# Brings the server up to date:  ./scripts/deploy.sh
# Pulls the latest code, rebuilds what changed, restarts it, and waits until it answers.
set -euo pipefail
cd "$(dirname "$0")/.."
COMPOSE="docker compose -f docker-compose.prod.yml"

[ -f .env ] || { echo "no .env here: run scripts/make-prod-env.sh first" >&2; exit 1; }

if [ -d .git ] && [ "${1:-}" != "--no-pull" ]; then
  git rev-parse HEAD > .previous 2>/dev/null || true     # what we are leaving, in case the new one fails
  git pull --ff-only
fi

# A backup first, so a bad update can be undone (if it cannot be made, the deploy stops: better than updating without a safety net). (Skipped on the very first start: no database yet.)
if $COMPOSE ps --status running postgres 2>/dev/null | grep -q postgres; then
  ./scripts/backup.sh
fi

$COMPOSE up -d --build --remove-orphans

host="$(grep '^API_HOST=' .env | cut -d= -f2)"
port="$(grep '^HTTPS_PORT=' .env | cut -d= -f2 || true)"
url="https://$host${port:+:$port}/health"
# DEPLOY_CURL_OPTS=-k lets a test run with Caddy's own certificate for "localhost".
echo "Waiting for $url ..."
for i in $(seq 1 60); do
  # shellcheck disable=SC2086
  if curl -fsS ${DEPLOY_CURL_OPTS:-} --max-time 5 "$url" >/dev/null 2>&1; then
    echo "Up: $url"
    git rev-parse HEAD > .last-good 2>/dev/null || true   # what scripts/rollback.sh goes back to
    $COMPOSE ps
    exit 0
  fi
  sleep 3
done
echo "Did not answer in 3 minutes. Look at:  docker compose -f docker-compose.prod.yml logs --tail 50" >&2
exit 1
