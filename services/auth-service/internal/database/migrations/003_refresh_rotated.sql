-- A refresh token is used once: using it swaps it for a new one (it is "rotated"). If a token that
-- was already rotated turns up again, either the real user or a thief is holding an old copy, and
-- there is no telling which, so every session of that user is ended. rotated_at tells a token
-- swapped for a new one apart from one revoked by sign-out, which proves nothing.
ALTER TABLE refresh_tokens ADD COLUMN rotated_at timestamptz;
