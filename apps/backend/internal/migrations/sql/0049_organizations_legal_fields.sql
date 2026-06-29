-- +goose Up
-- =====================================================================
-- arena_new — Organizations legal & contact fields (Wave O, feature #253)
--
-- Adds juridical/legal identification, registered address, public contact
-- channels, branding, and KYB (Know Your Business) verification status to
-- the organizations tenant table. These fields back the SuperAdmin and
-- org-admin "legal & billing" surfaces and are also used by invoice
-- rendering, tax compliance, and KYB workflows.
--
-- Design decisions:
--   * Legal/contact attributes are kept on the organization row (not a
--     separate table) because they are 1:1 with the tenant and read on
--     virtually every billing/invoice surface.
--   * tax_id format validation is performed application-side; the DB
--     only constrains tax_id_scheme to a known enum via CHECK so we can
--     add new schemes without an enum-type migration.
--   * legal_address_country uses ISO-3166-1 alpha-2 (e.g. 'DE', 'IL',
--     'GB', 'US'). Application validates against an allowlist.
--   * logo_media_id is a forward reference to the media table that
--     ships in Wave G. The column is nullable and intentionally has
--     NO FK constraint here — the FK will be added in the Wave G
--     migration once the media table exists.
--   * kyb_status is constrained to 'unverified', 'pending', 'verified',
--     'rejected'. Default 'unverified' preserves existing-row semantics.
--   * No new RBAC permissions are added — the existing 'org.update'
--     permission covers writes to all of these fields.
-- =====================================================================

ALTER TABLE organizations
    ADD COLUMN legal_name                  text        NULL,
    ADD COLUMN tax_id                      text        NULL,
    ADD COLUMN tax_id_scheme               text        NULL,
    ADD COLUMN registration_number         text        NULL,
    ADD COLUMN legal_address_line1         text        NULL,
    ADD COLUMN legal_address_line2         text        NULL,
    ADD COLUMN legal_address_postal_code   text        NULL,
    ADD COLUMN legal_address_city          text        NULL,
    ADD COLUMN legal_address_country       text        NULL,
    ADD COLUMN contact_email               text        NULL,
    ADD COLUMN contact_phone               text        NULL,
    ADD COLUMN website_url                 text        NULL,
    ADD COLUMN logo_media_id               uuid        NULL,
    ADD COLUMN kyb_status                  text        NOT NULL DEFAULT 'unverified',
    ADD COLUMN kyb_verified_at             timestamptz NULL;

-- Constrain tax_id_scheme to a known enum. NULL is allowed (org has no
-- tax registration yet); when set, must be one of the known schemes.
ALTER TABLE organizations
    ADD CONSTRAINT organizations_tax_id_scheme_check
        CHECK (tax_id_scheme IS NULL
            OR tax_id_scheme IN ('eu_vat', 'gb_vat', 'il_vat', 'us_ein', 'other'));

-- Constrain kyb_status to the verification state machine.
ALTER TABLE organizations
    ADD CONSTRAINT organizations_kyb_status_check
        CHECK (kyb_status IN ('unverified', 'pending', 'verified', 'rejected'));

-- Constrain legal_address_country to ISO-3166-1 alpha-2 shape when set.
-- (App layer validates membership in a country allowlist.)
ALTER TABLE organizations
    ADD CONSTRAINT organizations_legal_address_country_check
        CHECK (legal_address_country IS NULL
            OR legal_address_country ~ '^[A-Z]{2}$');

-- Partial index: kyb queue lookups by SuperAdmin (e.g. "pending review").
CREATE INDEX orgs_kyb_status_pending
    ON organizations (kyb_status)
    WHERE deleted_at IS NULL AND kyb_status IN ('pending', 'rejected');

COMMENT ON COLUMN organizations.legal_name IS
    'Registered juridical name of the tenant entity (distinct from the '
    'public display name). Used on invoices, contracts, and tax forms.';
COMMENT ON COLUMN organizations.tax_id IS
    'Tax registration identifier (VAT, EIN, etc.). Format is validated '
    'application-side based on tax_id_scheme.';
COMMENT ON COLUMN organizations.tax_id_scheme IS
    'Identifies the tax-id scheme: eu_vat, gb_vat, il_vat, us_ein, other. '
    'NULL when the organization has not registered a tax id.';
COMMENT ON COLUMN organizations.registration_number IS
    'Company registry number in the home country (e.g. HRB for Germany, '
    'OGRN-equivalents elsewhere).';
COMMENT ON COLUMN organizations.legal_address_country IS
    'ISO-3166-1 alpha-2 country code (e.g. DE, IL, GB, US).';
COMMENT ON COLUMN organizations.logo_media_id IS
    'Optional FK to the media table (added in Wave G). Nullable; the FK '
    'constraint itself is deferred until the media table exists.';
COMMENT ON COLUMN organizations.kyb_status IS
    'KYB (Know Your Business) verification status. State machine: '
    'unverified -> pending -> verified | rejected.';
COMMENT ON COLUMN organizations.kyb_verified_at IS
    'Timestamp when KYB verification last reached the verified state.';

-- +goose Down
-- Drop indexes/constraints first, then the columns. The kyb_status_pending
-- partial index drops automatically with its column.
ALTER TABLE organizations
    DROP CONSTRAINT IF EXISTS organizations_tax_id_scheme_check,
    DROP CONSTRAINT IF EXISTS organizations_kyb_status_check,
    DROP CONSTRAINT IF EXISTS organizations_legal_address_country_check;

DROP INDEX IF EXISTS orgs_kyb_status_pending;

ALTER TABLE organizations
    DROP COLUMN IF EXISTS kyb_verified_at,
    DROP COLUMN IF EXISTS kyb_status,
    DROP COLUMN IF EXISTS logo_media_id,
    DROP COLUMN IF EXISTS website_url,
    DROP COLUMN IF EXISTS contact_phone,
    DROP COLUMN IF EXISTS contact_email,
    DROP COLUMN IF EXISTS legal_address_country,
    DROP COLUMN IF EXISTS legal_address_city,
    DROP COLUMN IF EXISTS legal_address_postal_code,
    DROP COLUMN IF EXISTS legal_address_line2,
    DROP COLUMN IF EXISTS legal_address_line1,
    DROP COLUMN IF EXISTS registration_number,
    DROP COLUMN IF EXISTS tax_id_scheme,
    DROP COLUMN IF EXISTS tax_id,
    DROP COLUMN IF EXISTS legal_name;
