-- 0218_session_external_sales.sql — a session sold by a partner box office
-- (spec 63 §14.1, Л-3).
--
-- sessions.sales_mode tells who sells the tickets of a session:
--   platform  — arena sells them (every session until now);
--   external  — a partner box office (Eventim, Ticketportal) sells them, and
--               arena only shows the session with a link to that box office.
--
-- An external session carries the partner's URL (https only) and name, and
-- optionally a display price "from" with its currency (both or neither). A
-- platform session carries none of the four fields.
--
-- An external session is never sold here: hcheckout.SessionSellable refuses
-- it on every hold path, the Bil24 gateway does not list it, the move/cancel
-- of such a session writes the journal without buyer letters, and the
-- automatic guest-list exports skip it.
--
-- capacity_total stays NOT NULL (CHECK > 0 from 0016): for an external
-- session it is the capacity of the hall. Such a session has no
-- session_seats, no tiers and no inventory_ledger rows.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    ADD COLUMN sales_mode               text   NOT NULL DEFAULT 'platform',
    ADD COLUMN external_ticket_url      text   NULL,
    ADD COLUMN external_seller_name     text   NULL,
    ADD COLUMN display_price_from_minor bigint NULL,
    ADD COLUMN display_currency         char(3) NULL;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_sales_mode_check
        CHECK (sales_mode IN ('platform', 'external')),
    ADD CONSTRAINT sessions_external_seller_name_length
        CHECK (external_seller_name IS NULL
               OR char_length(external_seller_name) BETWEEN 1 AND 80),
    ADD CONSTRAINT sessions_display_price_from_nonneg
        CHECK (display_price_from_minor IS NULL OR display_price_from_minor >= 0),
    ADD CONSTRAINT sessions_display_price_currency_pair
        CHECK ((display_price_from_minor IS NULL) = (display_currency IS NULL)),
    ADD CONSTRAINT sessions_sales_mode_fields
        CHECK (
            (sales_mode = 'external'
             AND external_ticket_url IS NOT NULL
             AND external_seller_name IS NOT NULL
             AND external_ticket_url LIKE 'https://%')
            OR
            (sales_mode = 'platform'
             AND external_ticket_url IS NULL
             AND external_seller_name IS NULL
             AND display_price_from_minor IS NULL
             AND display_currency IS NULL)
        );

COMMENT ON COLUMN sessions.sales_mode IS
    'Who sells the tickets of the session: platform (arena) or external (a '
    'partner box office). An external session is never held or sold here. '
    'Migration 0218.';
COMMENT ON COLUMN sessions.external_ticket_url IS
    'The partner box office of an external session, https only. NULL for a '
    'platform session. Migration 0218.';
COMMENT ON COLUMN sessions.external_seller_name IS
    'The name of the partner box office (1 to 80 characters). NULL for a '
    'platform session. Migration 0218.';
COMMENT ON COLUMN sessions.display_price_from_minor IS
    'The display price "from" of an external session in minor units, set '
    'together with display_currency. Migration 0218.';
COMMENT ON COLUMN sessions.display_currency IS
    'The currency of display_price_from_minor. Migration 0218.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_sales_mode_fields,
    DROP CONSTRAINT IF EXISTS sessions_display_price_currency_pair,
    DROP CONSTRAINT IF EXISTS sessions_display_price_from_nonneg,
    DROP CONSTRAINT IF EXISTS sessions_external_seller_name_length,
    DROP CONSTRAINT IF EXISTS sessions_sales_mode_check,
    DROP COLUMN IF EXISTS display_currency,
    DROP COLUMN IF EXISTS display_price_from_minor,
    DROP COLUMN IF EXISTS external_seller_name,
    DROP COLUMN IF EXISTS external_ticket_url,
    DROP COLUMN IF EXISTS sales_mode;
