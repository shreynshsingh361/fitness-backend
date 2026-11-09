-- The nutrition service owns the food catalogue, each user's food diary,
-- calorie goals and water logs.
-- user_id is just a uuid: the users table lives in the auth service's own
-- database, so there is deliberately NO foreign key to it.

CREATE TABLE foods (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name      text             NOT NULL UNIQUE,
    serving   text             NOT NULL,            -- e.g. '1 large egg (50 g)'
    calories  integer          NOT NULL CHECK (calories >= 0),
    protein_g double precision NOT NULL CHECK (protein_g >= 0),
    carbs_g   double precision NOT NULL CHECK (carbs_g >= 0),
    fat_g     double precision NOT NULL CHECK (fat_g >= 0)
);

-- A diary entry stores a SNAPSHOT of the food's name and nutrition (already
-- multiplied by the number of servings). If someone later edits the food in the
-- catalogue, yesterday's diary must not change. That is why the numbers are
-- copied here instead of joined from the foods table.
CREATE TABLE diary_entries (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid             NOT NULL,
    -- Sent by the client so a retried request cannot add the meal twice.
    client_op_id uuid             NOT NULL,
    eaten_on     date             NOT NULL,
    meal         text             NOT NULL CHECK (meal IN ('breakfast', 'lunch', 'dinner', 'snack')),
    food_id      uuid REFERENCES foods(id) ON DELETE SET NULL,
    food_name    text             NOT NULL,
    serving      text             NOT NULL,
    servings     double precision NOT NULL CHECK (servings > 0),
    calories     integer          NOT NULL CHECK (calories >= 0),
    protein_g    double precision NOT NULL,
    carbs_g      double precision NOT NULL,
    fat_g        double precision NOT NULL,
    created_at   timestamptz      NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_op_id)
);

CREATE INDEX diary_entries_user_day_idx ON diary_entries (user_id, eaten_on);

CREATE TABLE nutrition_goals (
    user_id    uuid PRIMARY KEY,
    calories   integer     NOT NULL CHECK (calories BETWEEN 500 AND 10000),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE water_logs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL,
    logged_on  date        NOT NULL,
    amount_ml  integer     NOT NULL CHECK (amount_ml BETWEEN 1 AND 5000),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX water_logs_user_day_idx ON water_logs (user_id, logged_on);
