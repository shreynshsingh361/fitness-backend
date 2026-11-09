-- The pace bots are gone, so nobody needs to hide them any more.
DELETE FROM hidden_users
WHERE hidden_id IN ('00000000-0000-4000-8000-00000000b001', '00000000-0000-4000-8000-00000000b002');
