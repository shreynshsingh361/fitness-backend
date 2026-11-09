-- The social service owns who knows whom: friends, hidden people, cheers and
-- taunts, groups, group chat. It owns NO step counts (it asks the activity service)
-- and NO names (it asks the auth service), and user ids here are plain uuids with
-- no foreign key, because the users table lives in another service's database.

-- Each person has one personal invite code, created the first time they ask for it.
CREATE TABLE friend_invites (
    user_id    uuid PRIMARY KEY,
    code       text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A friendship is one row, smaller id first, so (a, b) and (b, a) cannot both exist.
CREATE TABLE friendships (
    user_a     uuid        NOT NULL,
    user_b     uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_a, user_b),
    CHECK (user_a < user_b)
);

CREATE INDEX friendships_user_b_idx ON friendships (user_b);

-- Hiding works both ways: neither person sees the other any more. The hidden
-- person is never told, so no API may reveal who has hidden you.
CREATE TABLE hidden_users (
    hider_id   uuid        NOT NULL,
    hidden_id  uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (hider_id, hidden_id)
);

CREATE INDEX hidden_users_hidden_idx ON hidden_users (hidden_id);

CREATE TABLE groups (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name               text        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    invite_code        text        NOT NULL UNIQUE,
    -- Only ranks are shown for dates that are not over yet.
    mystery_mode       boolean     NOT NULL DEFAULT false,
    chat_enabled       boolean     NOT NULL DEFAULT true,
    taunts_enabled     boolean     NOT NULL DEFAULT true,
    -- Whether typed-in steps count on this group's boards.
    count_manual_steps boolean     NOT NULL DEFAULT true,
    -- An optional fixed-date challenge. The group carries on after it ends.
    challenge_start    date,
    challenge_end      date,
    created_by         uuid        NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CHECK ((challenge_start IS NULL) = (challenge_end IS NULL)),
    CHECK (challenge_end >= challenge_start)
);

CREATE TABLE group_members (
    group_id  uuid        NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL,
    role      text        NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX group_members_user_idx ON group_members (user_id);

-- Someone an admin removed. Without this, the invite link would let them straight back in.
CREATE TABLE group_bans (
    group_id   uuid        NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE group_messages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   uuid        NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL,
    body       text        NOT NULL CHECK (char_length(body) <= 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    -- A removed message keeps its place in the chat but loses its text.
    removed_at timestamptz,
    removed_by uuid
);

CREATE INDEX group_messages_group_time_idx ON group_messages (group_id, created_at DESC);

CREATE TABLE nudges (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_user  uuid        NOT NULL,
    to_user    uuid        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('cheer', 'taunt')),
    group_id   uuid REFERENCES groups(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at    timestamptz
);

CREATE INDEX nudges_to_time_idx ON nudges (to_user, created_at DESC);
CREATE INDEX nudges_pair_idx ON nudges (from_user, to_user, kind, created_at DESC);
