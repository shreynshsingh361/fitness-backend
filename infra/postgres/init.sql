-- Runs once, the first time the Postgres container starts with an empty volume.
--
-- ONE Postgres server, but FIVE separate databases, each with its own login.
-- This is the "database per service" rule of microservices: the workout service
-- literally cannot read the auth service's users table, because its login has no
-- access to auth_db. If you want a service's data, you must ask its API.
--
-- These passwords are for local development only. Real deployments must take
-- them from a secrets manager and never commit them.

CREATE ROLE auth_svc      LOGIN PASSWORD 'auth_dev_password';
CREATE ROLE workout_svc   LOGIN PASSWORD 'workout_dev_password';
CREATE ROLE nutrition_svc LOGIN PASSWORD 'nutrition_dev_password';
CREATE ROLE activity_svc  LOGIN PASSWORD 'activity_dev_password';
CREATE ROLE social_svc    LOGIN PASSWORD 'social_dev_password';

CREATE DATABASE auth_db      OWNER auth_svc;
CREATE DATABASE workout_db   OWNER workout_svc;
CREATE DATABASE nutrition_db OWNER nutrition_svc;
CREATE DATABASE activity_db  OWNER activity_svc;
CREATE DATABASE social_db    OWNER social_svc;

-- By default every login may connect to every database. Take that away.
REVOKE CONNECT ON DATABASE auth_db      FROM PUBLIC;
REVOKE CONNECT ON DATABASE workout_db   FROM PUBLIC;
REVOKE CONNECT ON DATABASE nutrition_db FROM PUBLIC;
REVOKE CONNECT ON DATABASE activity_db  FROM PUBLIC;
REVOKE CONNECT ON DATABASE social_db    FROM PUBLIC;
