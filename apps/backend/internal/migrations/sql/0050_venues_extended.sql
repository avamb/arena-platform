-- +goose Up
-- =====================================================================
-- arena_new — Venues extended structured address & metadata
-- (Wave V — Venues address & metadata, feature #257 / V-1)
--
-- Adds structured postal address, geocoordinates, IANA timezone, public
-- contact channels, website, and a lifecycle status to the venues table.
-- The pre-existing free-form `address` column is preserved verbatim for
-- backward compatibility — the admin UI hides it once the structured
-- address fields are populated, but legacy reads continue to work.
--
-- Design decisions:
--   * Structured address (line1/line2/postal_code/country) lives directly
--     on the venue row because every read surface (event pages, invoices,
--     scanner manifests) needs them inline; no JSON blob.
--   * country uses ISO-3166-1 alpha-2 (CHAR(2)). At INSERT/UPDATE time the
--     application layer enforces that venue.country == organizations.country
--     unless the caller passes an explicit override flag; the DB only
--     constrains the shape via CHECK so future overrides do not require
--     another migration.
--   * geo_lat / geo_lng use NUMERIC(9,6) — ~10 cm precision, covers the
--     full WGS-84 lat/lng range without floating-point ambiguity for
--     equality joins / dedup.
--   * timezone is a free-form text column; validation against the IANA
--     tzdata is performed application-side (Go time.LoadLocation). No
--     DB regex — the tz database changes faster than schema migrations.
--   * status is a small enum-like text column constrained via CHECK
--     ('active', 'draft', 'archived'). Existing rows default to 'active'.
--   * No new RBAC permissions are added — the existing
--     'venue.create' / 'venue.update' permissions (seeded in 0012)
--     cover writes to all of these fields.
-- =====================================================================

ALTER TABLE venues
    ADD COLUMN address_line1   text          NULL,
    ADD COLUMN address_line2   text          NULL,
    ADD COLUMN postal_code     text          NULL,
    ADD COLUMN country         char(2)       NULL,
    ADD COLUMN geo_lat         numeric(9,6)  NULL,
    ADD COLUMN geo_lng         numeric(9,6)  NULL,
    ADD COLUMN timezone        text          NULL,
    ADD COLUMN contact_phone   text          NULL,
    ADD COLUMN contact_email   text          NULL,
    ADD COLUMN website_url     text          NULL,
    ADD COLUMN status          text          NOT NULL DEFAULT 'active';

-- Constrain country to ISO-3166-1 alpha-2 shape when set. Application
-- layer additionally validates membership in the country allowlist and
-- enforces "must equal owning organization's country" unless overridden.
ALTER TABLE venues
    ADD CONSTRAINT venues_country_check
        CHECK (country IS NULL OR country ~ '^[A-Z]{2}$');

-- Constrain geocoordinate ranges to the valid WGS-84 envelope when set.
ALTER TABLE venues
    ADD CONSTRAINT venues_geo_lat_range_check
        CHECK (geo_lat IS NULL OR (geo_lat >= -90  AND geo_lat <= 90)),
    ADD CONSTRAINT venues_geo_lng_range_check
        CHECK (geo_lng IS NULL OR (geo_lng >= -180 AND geo_lng <= 180));

-- Constrain status to the lifecycle state machine.
ALTER TABLE venues
    ADD CONSTRAINT venues_status_check
        CHECK (status IN ('active', 'draft', 'archived'));

-- Partial index: list venues by status within an org (active/draft surfaces).
CREATE INDEX venues_org_status_active
    ON venues (org_id, status)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN venues.address_line1 IS
    'Structured street address line 1 (e.g. street + number). Optional. '
    'When populated, admin UI hides the legacy free-form address column.';
COMMENT ON COLUMN venues.address_line2 IS
    'Structured street address line 2 (e.g. suite, building). Optional.';
COMMENT ON COLUMN venues.postal_code IS
    'Postal / ZIP code. Optional. Format is country-specific and not '
    'enforced at the database level.';
COMMENT ON COLUMN venues.country IS
    'ISO-3166-1 alpha-2 country code (e.g. DE, IL, GB, US). Must match '
    'the owning organization''s country unless an explicit override is '
    'passed at the API layer.';
COMMENT ON COLUMN venues.geo_lat IS
    'WGS-84 latitude in decimal degrees, range [-90, 90]. NUMERIC(9,6) '
    'gives ~10 cm precision.';
COMMENT ON COLUMN venues.geo_lng IS
    'WGS-84 longitude in decimal degrees, range [-180, 180]. NUMERIC(9,6) '
    'gives ~10 cm precision.';
COMMENT ON COLUMN venues.timezone IS
    'IANA time zone name (e.g. Europe/Berlin, Asia/Jerusalem). Validated '
    'application-side against Go time.LoadLocation / tzdata. Not enforced '
    'at the database level because the IANA database evolves independently '
    'of schema migrations.';
COMMENT ON COLUMN venues.contact_phone IS
    'Public contact phone number for the venue. Optional. E.164 recommended.';
COMMENT ON COLUMN venues.contact_email IS
    'Public contact email for the venue. Optional.';
COMMENT ON COLUMN venues.website_url IS
    'Public website URL for the venue. Optional.';
COMMENT ON COLUMN venues.status IS
    'Lifecycle status: active (default), draft (not yet published), or '
    'archived (no longer bookable but retained for historical references).';
COMMENT ON COLUMN venues.address IS
    'Legacy free-form street address. Retained for backward compatibility. '
    'New writes should populate the structured address_line1/2, postal_code, '
    'and country columns instead; admin UI hides this field once structured '
    'fields are populated.';

-- +goose Down
DROP INDEX IF EXISTS venues_org_status_active;

ALTER TABLE venues
    DROP CONSTRAINT IF EXISTS venues_status_check,
    DROP CONSTRAINT IF EXISTS venues_geo_lng_range_check,
    DROP CONSTRAINT IF EXISTS venues_geo_lat_range_check,
    DROP CONSTRAINT IF EXISTS venues_country_check;

ALTER TABLE venues
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS website_url,
    DROP COLUMN IF EXISTS contact_email,
    DROP COLUMN IF EXISTS contact_phone,
    DROP COLUMN IF EXISTS timezone,
    DROP COLUMN IF EXISTS geo_lng,
    DROP COLUMN IF EXISTS geo_lat,
    DROP COLUMN IF EXISTS country,
    DROP COLUMN IF EXISTS postal_code,
    DROP COLUMN IF EXISTS address_line2,
    DROP COLUMN IF EXISTS address_line1;
