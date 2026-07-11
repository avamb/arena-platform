-- 0060_checkout_token.sql — opaque public checkout token for anonymous order-status lookups (feature #319).
--
-- Adds checkout_token to checkout_sessions: a high-entropy hex string minted by
-- PostgreSQL at INSERT time via gen_random_bytes. Used as the path parameter for
-- GET /v1/public/checkout/{checkout_token} so buyers can check order status
-- without exposing internal UUIDs.
--
-- The UNIQUE constraint + index enable O(1) lookups from the public endpoint.

-- +goose Up

ALTER TABLE checkout_sessions
    ADD COLUMN IF NOT EXISTS checkout_token text
        NOT NULL DEFAULT encode(gen_random_bytes(32), 'hex');

ALTER TABLE checkout_sessions
    ADD CONSTRAINT checkout_sessions_checkout_token_unique UNIQUE (checkout_token);

CREATE INDEX IF NOT EXISTS checkout_sessions_checkout_token_idx
    ON checkout_sessions (checkout_token);

-- +goose Down

DROP INDEX IF EXISTS checkout_sessions_checkout_token_idx;
ALTER TABLE checkout_sessions DROP CONSTRAINT IF EXISTS checkout_sessions_checkout_token_unique;
ALTER TABLE checkout_sessions DROP COLUMN IF EXISTS checkout_token;
