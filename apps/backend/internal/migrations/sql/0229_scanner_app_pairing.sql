-- 0229_scanner_app_pairing.sql — pairing a phone and refreshing its tokens
-- (spec 69, tasks MA-11 and MA-12).
--
--   scanner_devices
--     controller_name          the name a door controller typed when the
--                              phone was paired with a pass ('' for an
--                              organizer device).
--     prev_refresh_token_hash  SHA-256 hex of the PREVIOUS refresh token. It
--                              stays valid until the device uses the access
--                              token of its new pair (pair_confirmed), so a
--                              refresh answer lost on the network does not
--                              lock the phone out.
--     pair_confirmed           false from a refresh until the device first
--                              uses the new access token.
--     refresh_expires_at       when the refresh token stops working (a sliding
--                              30 days, moved on every refresh).
--     refreshed_at             the last refresh.
--   scanner_retired_refresh_tokens
--                            refresh tokens that were replaced. A retired
--                            token presented again means a stolen copy: the
--                            device is revoked (refresh_token_reuse).
--   scanner_pair_attempts    every exchange of a pairing code, for the
--                            attempt limit. ip_hash is the SHA-256 hex of the
--                            client address; the address itself is never
--                            stored. Rows older than a day are deleted on
--                            every write.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE scanner_devices
    ADD COLUMN controller_name         text        NOT NULL DEFAULT '',
    ADD COLUMN prev_refresh_token_hash text,
    ADD COLUMN pair_confirmed          boolean     NOT NULL DEFAULT true,
    ADD COLUMN refresh_expires_at      timestamptz,
    ADD COLUMN refreshed_at            timestamptz,
    ADD CONSTRAINT scanner_devices_controller_name_check CHECK (length(controller_name) <= 80),
    ADD CONSTRAINT scanner_devices_prev_refresh_token_hash_uq UNIQUE (prev_refresh_token_hash),
    ADD CONSTRAINT scanner_devices_prev_refresh_token_hash_check CHECK (
        prev_refresh_token_hash IS NULL OR prev_refresh_token_hash ~ '^[0-9a-f]{64}$');

CREATE TABLE scanner_retired_refresh_tokens (
    token_hash text        PRIMARY KEY,
    device_id  uuid        NOT NULL REFERENCES scanner_devices(id) ON DELETE CASCADE,
    retired_at timestamptz NOT NULL,
    CONSTRAINT scanner_retired_refresh_tokens_hash_check CHECK (token_hash ~ '^[0-9a-f]{64}$')
);

-- The cascade from a deleted device and the clean-up of old rows of a device.
CREATE INDEX scanner_retired_refresh_tokens_device_idx
    ON scanner_retired_refresh_tokens (device_id, retired_at);

CREATE TABLE scanner_pair_attempts (
    id         bigserial   PRIMARY KEY,
    ip_hash    text        NOT NULL,
    code_hash  text        NOT NULL,
    outcome    text        NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT scanner_pair_attempts_outcome_check CHECK (outcome IN ('ok', 'not_found', 'pass_full'))
);

-- The limit by address, the limit by code and the daily clean-up.
CREATE INDEX scanner_pair_attempts_ip_idx      ON scanner_pair_attempts (ip_hash, created_at);
CREATE INDEX scanner_pair_attempts_code_idx    ON scanner_pair_attempts (code_hash, created_at);
CREATE INDEX scanner_pair_attempts_created_idx ON scanner_pair_attempts (created_at);

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TABLE IF EXISTS scanner_pair_attempts;
DROP TABLE IF EXISTS scanner_retired_refresh_tokens;

ALTER TABLE scanner_devices
    DROP CONSTRAINT IF EXISTS scanner_devices_prev_refresh_token_hash_check,
    DROP CONSTRAINT IF EXISTS scanner_devices_prev_refresh_token_hash_uq,
    DROP CONSTRAINT IF EXISTS scanner_devices_controller_name_check,
    DROP COLUMN IF EXISTS refreshed_at,
    DROP COLUMN IF EXISTS refresh_expires_at,
    DROP COLUMN IF EXISTS pair_confirmed,
    DROP COLUMN IF EXISTS prev_refresh_token_hash,
    DROP COLUMN IF EXISTS controller_name;
