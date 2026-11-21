-- 0203_organization_branding.sql — an organization's own theme (spec 62 §12.1,
-- decision R-24, item P-23).
--
-- organizations.branding  a flat JSON object of design tokens the event
--                         center writes into :root and the ticket PDF and
--                         letters use for their accent colour. Keys are a
--                         closed list, checked by the API (hiam
--                         validateBranding): primary, on_primary,
--                         on_primary_muted, ink, accent, bg (each "#rrggbb"
--                         or "rgba(...)") and font (Assistant, Onest,
--                         Golos Text). An empty object means the platform's
--                         own palette.
--
-- Numbered 0203 after 0201 (funnel) and 0202 (test channel).
-- =====================================================================

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    ADD COLUMN branding jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT organizations_branding_object_check
        CHECK (jsonb_typeof(branding) = 'object');

-- +goose Down

SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    DROP COLUMN IF EXISTS branding;
