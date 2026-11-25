-- 0226_seat_hold_on_click.sql — seat hold at the moment of the click
-- (spec 08_architecture/67, sections 3.2 and 7.1).
--
--   reservations.*                 the hold key, the visitor hashes, the two
--                                  steps (select, payment), extensions, the
--                                  "payment in progress" mark, the re-hold
--                                  link, the Stripe closing mark and the
--                                  policy snapshot of the hold
--   payment_intents.page_expires_at when the provider's payment page expires
--   hold_policy_profiles           the hold times and limits (standard,
--                                  high_demand), changed by a superadmin
--   events.hold_profile            the event's profile, NULL = standard
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE reservations
    ADD COLUMN hold_token_hash     bytea       NULL,
    ADD COLUMN visitor_device_hash bytea       NULL,
    ADD COLUMN visitor_ip_hash     bytea       NULL,
    ADD COLUMN select_deadline     timestamptz NULL,
    ADD COLUMN payment_started_at  timestamptz NULL,
    ADD COLUMN extensions_used     smallint    NOT NULL DEFAULT 0
        CHECK (extensions_used >= 0),
    ADD COLUMN paying_until        timestamptz NULL,
    ADD COLUMN rehold_of           uuid        NULL REFERENCES reservations(id),
    ADD COLUMN closing_started_at  timestamptz NULL,
    ADD COLUMN hold_policy         jsonb       NULL;

CREATE UNIQUE INDEX reservations_hold_token_hash_uniq
    ON reservations (hold_token_hash)
    WHERE hold_token_hash IS NOT NULL;

CREATE INDEX reservations_session_device_open_idx
    ON reservations (session_id, visitor_device_hash)
    WHERE state IN ('draft', 'active');

CREATE INDEX reservations_session_ip_open_idx
    ON reservations (session_id, visitor_ip_hash)
    WHERE state IN ('draft', 'active');

CREATE INDEX reservations_device_expired_idx
    ON reservations (visitor_device_hash, expired_at)
    WHERE state = 'expired';

ALTER TABLE payment_intents
    ADD COLUMN page_expires_at timestamptz NULL;

CREATE TABLE hold_policy_profiles (
    name                  text        PRIMARY KEY
        CHECK (name ~ '^[a-z][a-z0-9_]{0,31}$'),
    select_seconds        integer     NOT NULL CHECK (select_seconds BETWEEN 120 AND 1800),
    payment_seconds       integer     NOT NULL CHECK (payment_seconds BETWEEN 300 AND 1800),
    extend_seconds        integer     NOT NULL CHECK (extend_seconds >= 0),
    extend_count          integer     NOT NULL CHECK (extend_count BETWEEN 0 AND 3),
    paying_seconds        integer     NOT NULL CHECK (paying_seconds >= 0),
    max_seats_per_hold    integer     NOT NULL CHECK (max_seats_per_hold >= 1),
    max_active_per_device integer     NOT NULL CHECK (max_active_per_device >= 1),
    max_active_per_ip     integer     NOT NULL CHECK (max_active_per_ip >= 1),
    rehold_pause_seconds  integer     NOT NULL CHECK (rehold_pause_seconds >= 0),
    poll_interval_ms      integer     NOT NULL CHECK (poll_interval_ms BETWEEN 1000 AND 10000),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    updated_by            uuid        NULL
);

INSERT INTO hold_policy_profiles
    (name, select_seconds, payment_seconds, extend_seconds, extend_count, paying_seconds,
     max_seats_per_hold, max_active_per_device, max_active_per_ip, rehold_pause_seconds, poll_interval_ms)
VALUES
    ('standard',    600, 900, 300, 1, 600, 10, 2, 6, 300, 3000),
    ('high_demand', 420, 600, 300, 1, 600, 10, 2, 6, 300, 1500);

ALTER TABLE events
    ADD COLUMN hold_profile text NULL REFERENCES hold_policy_profiles(name);

COMMENT ON COLUMN reservations.hold_token_hash IS
    'SHA-256 of the public hold token (spec 67). NULL on holds of the old path and of the gateway.';
COMMENT ON COLUMN reservations.extensions_used IS
    'How many extensions the hold has used, for the whole hold (spec 67 section 3.3).';
COMMENT ON COLUMN reservations.closing_started_at IS
    'When the expiry job began closing the provider payment page; after it no extension and no paying mark is accepted (spec 67 section 5.2).';
COMMENT ON COLUMN reservations.hold_policy IS
    'The hold times and limits in force when the hold was created (spec 67 section 7); later profile edits do not move an open hold.';
COMMENT ON COLUMN payment_intents.page_expires_at IS
    'When the provider payment page of this intent expires, written by the page creation (spec 67 section 3.2).';
COMMENT ON TABLE hold_policy_profiles IS
    'Seat hold times and limits per profile (spec 67 section 7.1). Changed by a platform superadmin only.';
COMMENT ON COLUMN events.hold_profile IS
    'The hold policy profile of the event; NULL means standard (spec 67 section 7.1).';

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE events DROP COLUMN IF EXISTS hold_profile;
DROP TABLE IF EXISTS hold_policy_profiles;
ALTER TABLE payment_intents DROP COLUMN IF EXISTS page_expires_at;

DROP INDEX IF EXISTS reservations_device_expired_idx;
DROP INDEX IF EXISTS reservations_session_ip_open_idx;
DROP INDEX IF EXISTS reservations_session_device_open_idx;
DROP INDEX IF EXISTS reservations_hold_token_hash_uniq;

ALTER TABLE reservations
    DROP COLUMN IF EXISTS hold_policy,
    DROP COLUMN IF EXISTS closing_started_at,
    DROP COLUMN IF EXISTS rehold_of,
    DROP COLUMN IF EXISTS paying_until,
    DROP COLUMN IF EXISTS extensions_used,
    DROP COLUMN IF EXISTS payment_started_at,
    DROP COLUMN IF EXISTS select_deadline,
    DROP COLUMN IF EXISTS visitor_ip_hash,
    DROP COLUMN IF EXISTS visitor_device_hash,
    DROP COLUMN IF EXISTS hold_token_hash;
