-- +goose Up
-- =====================================================================
-- arena_new — Currency derived from geography (Wave 4, AB-38)
-- + seat-status rename blocked → unavailable (folded from AB-49 so the
--   session_seats CHECK is touched exactly once in this wave)
--
-- Owner rule (final): currency is DERIVED from the venue's country/city
-- and OVERRIDABLE on the session. The invariant that matters is ONE
-- CURRENCY PER SESSION — enforced with a composite FK from ticket_tiers
-- (session_id, currency) to sessions (id, currency) ON UPDATE CASCADE,
-- so a session currency change either rewrites all its tiers atomically
-- or fails; a tier can never carry a currency different from its
-- session's.
--
-- Resolution at session create (application layer):
--   venue -> city.currency_override ?? country.currency,
--   then the operator may override the resolved value explicitly
--   (recorded as sessions.currency_source = 'override').
-- =====================================================================

-- ---------------------------------------------------------------------
-- countries.currency — ISO 4217, seeded for all 10 seeded countries
-- ---------------------------------------------------------------------

ALTER TABLE countries
    ADD COLUMN currency char(3) NULL;

UPDATE countries c
SET    currency = seed.currency
FROM   (VALUES
    ('IL', 'ILS'),
    ('EE', 'EUR'),
    ('US', 'USD'),
    ('DE', 'EUR'),
    ('GB', 'GBP'),
    ('FR', 'EUR'),
    ('LV', 'EUR'),
    ('LT', 'EUR'),
    ('FI', 'EUR'),
    ('SE', 'SEK')
) AS seed(iso2, currency)
WHERE  c.iso2 = seed.iso2;

-- Defensive: any country row seeded outside this migration series.
UPDATE countries SET currency = 'USD' WHERE currency IS NULL;

ALTER TABLE countries
    ALTER COLUMN currency SET NOT NULL,
    ADD CONSTRAINT countries_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$');

COMMENT ON COLUMN countries.currency IS
    'ISO 4217 currency code of the country (e.g. ILS, EUR). Base of the '
    'session currency derivation chain. Wave 4 — AB-38.';

-- ---------------------------------------------------------------------
-- cities.currency_override — rare intra-country divergence
-- ---------------------------------------------------------------------

ALTER TABLE cities
    ADD COLUMN currency_override char(3) NULL
        CONSTRAINT cities_currency_override_iso4217
        CHECK (currency_override IS NULL OR currency_override ~ '^[A-Z]{3}$');

COMMENT ON COLUMN cities.currency_override IS
    'ISO 4217 override for countries with more than one currency in '
    'circulation. NULL = inherit countries.currency. Wave 4 — AB-38.';

-- ---------------------------------------------------------------------
-- sessions.currency + currency_source
-- ---------------------------------------------------------------------

ALTER TABLE sessions
    ADD COLUMN currency char(3) NULL,
    ADD COLUMN currency_source text NOT NULL DEFAULT 'derived'
        CONSTRAINT sessions_currency_source_check
        CHECK (currency_source IN ('derived', 'override'));

-- Backfill: derive from the venue's geography.
-- Chain: city.currency_override -> city's country.currency ->
--        country matching venues.country (ISO2) -> USD (defensive).
UPDATE sessions s
SET    currency = COALESCE(ci.currency_override, cc.currency, vc.currency, 'USD')
FROM   venues v
LEFT JOIN cities    ci ON ci.id   = v.city_id
LEFT JOIN countries cc ON cc.id   = ci.country_id
LEFT JOIN countries vc ON vc.iso2 = v.country
WHERE  s.venue_id = v.id;

ALTER TABLE sessions
    ALTER COLUMN currency SET NOT NULL,
    ADD CONSTRAINT sessions_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$'),
    ADD CONSTRAINT sessions_id_currency_uniq UNIQUE (id, currency);

COMMENT ON COLUMN sessions.currency IS
    'ISO 4217 currency every price of this session is denominated in. '
    'Derived from the venue''s city/country at create time, or set '
    'explicitly by the operator (see currency_source). One currency per '
    'session is the hard invariant. Wave 4 — AB-38.';

COMMENT ON COLUMN sessions.currency_source IS
    '''derived'' — resolved from venue geography; ''override'' — set '
    'deliberately by the operator. Lets the UI show provenance and lets '
    'a venue change safely re-derive only the derived ones. Wave 4 — AB-38.';

-- ---------------------------------------------------------------------
-- ticket_tiers.currency — wire-compatible, but constrained to equal the
-- session currency via composite FK (ON UPDATE CASCADE = changing the
-- session currency rewrites all tiers in the same statement).
-- ---------------------------------------------------------------------

-- Align existing tiers with their session before constraining.
UPDATE ticket_tiers t
SET    currency = s.currency
FROM   sessions s
WHERE  t.session_id = s.id
  AND  t.currency IS DISTINCT FROM s.currency::text;

ALTER TABLE ticket_tiers
    ALTER COLUMN currency DROP DEFAULT;

ALTER TABLE ticket_tiers
    ALTER COLUMN currency TYPE char(3) USING substring(trim(currency) from 1 for 3);

ALTER TABLE ticket_tiers
    ADD CONSTRAINT ticket_tiers_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$'),
    ADD CONSTRAINT ticket_tiers_currency_matches_session
        FOREIGN KEY (session_id, currency)
        REFERENCES sessions (id, currency)
        ON UPDATE CASCADE;

COMMENT ON COLUMN ticket_tiers.currency IS
    'ISO 4217 code kept for wire compatibility, but no longer an operator '
    'input: always equals the owning session''s currency (composite FK '
    'ticket_tiers_currency_matches_session, ON UPDATE CASCADE). '
    'Wave 4 — AB-38.';

-- ---------------------------------------------------------------------
-- Seat-status rename: blocked → unavailable (AB-49, folded into pass 1)
--
-- "Unavailable" is the Bil24 / MACS-facing word for an admin-withheld
-- place. The status machine is otherwise unchanged:
--   available → held → sold, available ↔ unavailable.
-- ---------------------------------------------------------------------

ALTER TABLE session_seats
    DROP CONSTRAINT session_seats_status_check;

UPDATE session_seats SET status = 'unavailable' WHERE status = 'blocked';

ALTER TABLE session_seats
    ADD CONSTRAINT session_seats_status_check
        CHECK (status IN ('available', 'held', 'sold', 'unavailable'));

COMMENT ON COLUMN session_seats.status IS
    'Seat lifecycle: available (default) → held (reservation open) '
    '→ sold (ticket issued), or unavailable (admin hold; formerly '
    '''blocked'', renamed in 0081). Transitions MUST be gated by the '
    'FOR UPDATE + conditional-UPDATE hold protocol described in §5.2. '
    'sold → unavailable is forbidden: a sold seat must be refunded to '
    'available first.';

-- +goose Down
ALTER TABLE session_seats
    DROP CONSTRAINT session_seats_status_check;

UPDATE session_seats SET status = 'blocked' WHERE status = 'unavailable';

ALTER TABLE session_seats
    ADD CONSTRAINT session_seats_status_check
        CHECK (status IN ('available', 'held', 'sold', 'blocked'));

ALTER TABLE ticket_tiers
    DROP CONSTRAINT IF EXISTS ticket_tiers_currency_matches_session,
    DROP CONSTRAINT IF EXISTS ticket_tiers_currency_iso4217;

ALTER TABLE ticket_tiers
    ALTER COLUMN currency TYPE text USING trim(currency);

ALTER TABLE ticket_tiers
    ALTER COLUMN currency SET DEFAULT 'USD';

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_id_currency_uniq,
    DROP CONSTRAINT IF EXISTS sessions_currency_iso4217,
    DROP CONSTRAINT IF EXISTS sessions_currency_source_check;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS currency_source,
    DROP COLUMN IF EXISTS currency;

ALTER TABLE cities
    DROP COLUMN IF EXISTS currency_override;

ALTER TABLE countries
    DROP CONSTRAINT IF EXISTS countries_currency_iso4217;

ALTER TABLE countries
    DROP COLUMN IF EXISTS currency;
