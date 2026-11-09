-- 0213_telegram_connect_codes.sql — "Connect Telegram" from the web event
-- center (acceptance run 2026-11-09, finding F-007).
--
-- An organizer whose organization was approved through the website works in
-- the web event center and has no Telegram link. The bot answered their
-- /start with the application of a NEW organization; the only way to link
-- Telegram to the existing one was to invite themselves from the Team screen.
--
-- Now the signed-in person asks the web for a one-time code
-- (POST /v1/organizations/{org_id}/telegram-connect-codes) and opens
-- t.me/<bot>?start=con_<code> on the phone (a button or a QR code). The bot
-- shows "Connect this Telegram to <organization>?" and, on the person's
-- press, links the account under BOT_SERVICE_TOKEN
-- (POST /v1/bot/telegram-connect/accept). The link itself is the ordinary
-- bot_telegram_links row an accepted invitation writes.
--
--   code_hash              SHA-256 of the code; the code itself is never stored.
--   user_id                the person the Telegram account is linked to.
--   org_id                 the organization the bot opens after the link.
--   expires_at             15 minutes after issue.
--   used_at                the link was made; a code works once.
--   used_telegram_user_id  the Telegram account that used it.
--   revoked_at             a newer code of the same person took this one back.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE telegram_connect_codes (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash             text        NOT NULL,
    user_id               uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id                uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    created_at            timestamptz NOT NULL DEFAULT now(),
    expires_at            timestamptz NOT NULL,
    used_at               timestamptz,
    used_telegram_user_id bigint,
    revoked_at            timestamptz,
    CONSTRAINT telegram_connect_codes_code_hash_uq UNIQUE (code_hash),
    CONSTRAINT telegram_connect_codes_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT telegram_connect_codes_used_check CHECK ((used_at IS NULL) = (used_telegram_user_id IS NULL))
);

-- Open codes of one person (a new code takes the old ones back).
CREATE INDEX telegram_connect_codes_open_idx ON telegram_connect_codes (user_id)
    WHERE used_at IS NULL AND revoked_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS telegram_connect_codes;
