#!/bin/sh
# Production version of init.sql. Runs once, the first time Postgres starts with an empty volume.
# The five service passwords come from the environment (.env), so none is written in the repo.
# Same layout as init.sql: one server, five databases, five logins, each login only for its own.
set -eu

for name in AUTH_DB_PASSWORD WORKOUT_DB_PASSWORD NUTRITION_DB_PASSWORD ACTIVITY_DB_PASSWORD SOCIAL_DB_PASSWORD; do
  eval "value=\${$name:-}"
  if [ "${#value}" -lt 24 ]; then
    echo "init-prod.sh: $name must be set and at least 24 characters (run scripts/make-prod-env.sh)" >&2
    exit 1
  fi
done

psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres <<EOSQL
CREATE ROLE auth_svc      LOGIN PASSWORD '${AUTH_DB_PASSWORD}';
CREATE ROLE workout_svc   LOGIN PASSWORD '${WORKOUT_DB_PASSWORD}';
CREATE ROLE nutrition_svc LOGIN PASSWORD '${NUTRITION_DB_PASSWORD}';
CREATE ROLE activity_svc  LOGIN PASSWORD '${ACTIVITY_DB_PASSWORD}';
CREATE ROLE social_svc    LOGIN PASSWORD '${SOCIAL_DB_PASSWORD}';

CREATE DATABASE auth_db      OWNER auth_svc;
CREATE DATABASE workout_db   OWNER workout_svc;
CREATE DATABASE nutrition_db OWNER nutrition_svc;
CREATE DATABASE activity_db  OWNER activity_svc;
CREATE DATABASE social_db    OWNER social_svc;

REVOKE CONNECT ON DATABASE auth_db      FROM PUBLIC;
REVOKE CONNECT ON DATABASE workout_db   FROM PUBLIC;
REVOKE CONNECT ON DATABASE nutrition_db FROM PUBLIC;
REVOKE CONNECT ON DATABASE activity_db  FROM PUBLIC;
REVOKE CONNECT ON DATABASE social_db    FROM PUBLIC;
EOSQL
