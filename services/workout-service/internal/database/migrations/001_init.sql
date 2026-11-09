-- The workout service owns exercises, workouts and sets.
-- user_id is just a uuid: the users table lives in the auth service's own
-- database, so there is deliberately NO foreign key to it.

CREATE TABLE exercises (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    muscle_group text NOT NULL,
    equipment    text NOT NULL
);

CREATE TABLE workouts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL,
    -- Sent by the client. If a phone retries a request after a network
    -- failure, this unique pair stops the workout from being saved twice.
    client_op_id     uuid        NOT NULL,
    name             text        NOT NULL,
    notes            text        NOT NULL DEFAULT '',
    started_at       timestamptz NOT NULL,
    duration_minutes integer     NOT NULL DEFAULT 0 CHECK (duration_minutes BETWEEN 0 AND 1440),
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_op_id)
);

CREATE INDEX workouts_user_started_idx ON workouts (user_id, started_at DESC);

CREATE TABLE workout_sets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workout_id  uuid             NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    exercise_id uuid             NOT NULL REFERENCES exercises(id),
    position    integer          NOT NULL,
    reps        integer          NOT NULL CHECK (reps > 0),
    weight_kg   double precision NOT NULL CHECK (weight_kg >= 0)
);

CREATE INDEX workout_sets_workout_idx ON workout_sets (workout_id, position);
