-- Adding or removing water can be retried safely. The app may save a +250 ml while it has no
-- internet and send it later, and a reply can get lost on the way back, so the same request can
-- arrive twice. Each request carries a client_op_id; one that was already applied is skipped.
CREATE TABLE water_ops (
    user_id      uuid        NOT NULL,
    client_op_id uuid        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, client_op_id)
);
