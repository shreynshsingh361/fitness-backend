-- The activity service owns step counts and step goals.
-- user_id is just a uuid: the users table lives in the auth service's own
-- database, so there is deliberately NO foreign key to it.

-- One row per user, per day, per source. `day` is the user's LOCAL date, so each
-- person's day ends at their own midnight wherever they live.
--
-- The source is kept because a phone and a watch both count the same walk: a
-- day's total is the HIGHEST count of any source, never the sum.
CREATE TABLE step_entries (
    user_id    uuid        NOT NULL,
    day        date        NOT NULL,
    source     text        NOT NULL,
    steps      integer     NOT NULL CHECK (steps BETWEEN 0 AND 200000),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day, source)
);

-- Leaderboards ask "what did these people walk between these dates?".
CREATE INDEX step_entries_day_user_idx ON step_entries (day, user_id);

CREATE TABLE activity_settings (
    user_id    uuid PRIMARY KEY,
    daily_goal integer     NOT NULL CHECK (daily_goal BETWEEN 1000 AND 100000),
    country    text CHECK (country ~ '^[A-Z]{2}$'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX activity_settings_country_idx ON activity_settings (country);
