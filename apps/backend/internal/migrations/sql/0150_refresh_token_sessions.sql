-- 0150_refresh_token_sessions.sql — a refresh token remembers which client
-- started its session and how long that session may stay idle.
--
-- Owner decision 2026-10-14 (spec 08_architecture/37, section 4.4): the web
-- event center keeps a session for 72 hours WITHOUT activity, like the
-- Stripe dashboard, and never longer than 30 days from the sign-in. Every
-- other client (admin-web, the old login) keeps the fixed 30-day token.
--
--   client_channel      X-Client-Channel of the sign-in (event_center_web,
--                       admin_web, ...), carried through every rotation.
--   idle_seconds        the idle window; NULL = the classic fixed lifetime.
--                       Each rotation sets expires_at = now() + idle_seconds,
--                       capped at session_started_at + 30 days.
--   session_started_at  the sign-in that started the chain of rotations.
--
-- Rows issued before this migration keep NULLs and behave exactly as before.
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
-- refresh_tokens is written by every sign-in and every refresh: fail fast
-- rather than queue behind a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

ALTER TABLE refresh_tokens
    ADD COLUMN client_channel     text,
    ADD COLUMN idle_seconds       integer,
    ADD COLUMN session_started_at timestamptz,
    ADD CONSTRAINT refresh_tokens_idle_seconds_check CHECK (idle_seconds IS NULL OR idle_seconds > 0);

-- +goose Down
ALTER TABLE refresh_tokens
    DROP CONSTRAINT IF EXISTS refresh_tokens_idle_seconds_check,
    DROP COLUMN IF EXISTS session_started_at,
    DROP COLUMN IF EXISTS idle_seconds,
    DROP COLUMN IF EXISTS client_channel;
