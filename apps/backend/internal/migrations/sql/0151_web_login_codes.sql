-- 0151_web_login_codes.sql — one-time codes that open the web event center
-- from the Telegram bot (spec 08_architecture/37 section 4.3, task
-- WEB-AUTH-2, owner decision 2026-10-14: "Open in browser" for every role,
-- according to its rights).
--
-- The bot asks for a code for the linked person under BOT_SERVICE_TOKEN and
-- sends <EVENT_CENTER_PUBLIC_URL>/login/link#code=<code> to the private chat.
-- The web exchanges the code for a web session (POST /v1/auth/web/link).
--
--   code_hash          SHA-256 of the code; the code itself is never stored.
--   user_id            the person the session is issued to.
--   telegram_user_id   the Telegram account that pressed the button; the
--                      exchange is refused when that account is no longer
--                      linked to the same person.
--   expires_at         five minutes after issue.
--   used_at            the exchange; a code works once.
--   revoked_at         a newer code of the same person, or "Sign out
--                      everywhere", took this one back.
--   notice_claimed_at  the bot has told the person about the sign-in, so the
--                      message goes out at most once.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE web_login_codes (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash         text        NOT NULL,
    user_id           uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    telegram_user_id  bigint      NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    used_at           timestamptz,
    revoked_at        timestamptz,
    notice_claimed_at timestamptz,
    CONSTRAINT web_login_codes_code_hash_uq UNIQUE (code_hash),
    CONSTRAINT web_login_codes_expiry_check CHECK (expires_at > created_at)
);

-- Open codes of one person (a new code takes the old ones back).
CREATE INDEX web_login_codes_open_idx ON web_login_codes (user_id)
    WHERE used_at IS NULL AND revoked_at IS NULL;

-- Sign-ins the bot has not announced yet.
CREATE INDEX web_login_codes_notice_idx ON web_login_codes (used_at)
    WHERE used_at IS NOT NULL AND notice_claimed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS web_login_codes;
