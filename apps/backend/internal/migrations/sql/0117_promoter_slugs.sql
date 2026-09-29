-- 0117_promoter_slugs.sql — a promoter gets its own public page.
--
-- Until now the hosted sales pages were addressed by the ORGANIZATION only
-- (tickets.arenasoldout.com/{org_slug} and /{org_slug}/{event_slug}). An
-- organization that sells for several promoters (migration 0113) wants each
-- promoter to hand out ITS OWN link — "teatrkolibel", not
-- "masterclassteatro/teatrkolibel" — and the organization's slug is a
-- technical detail nobody outside needs to see (owner decision 2026-09-29).
--
-- org_promoters.slug is that page's address: tickets.arenasoldout.com/{slug}
-- lists the promoter's published events, /{slug}/{event_slug} is one of
-- them. The slug lives in the SAME namespace as organizations.slug (the
-- public resolver tries the organization first, then the promoter), so it
-- is unique across the platform, case-insensitively, and the API refuses a
-- promoter slug that equals an organization's. NULL = no page yet (rows
-- created before this migration); the API assigns one on creation from the
-- promoter's name.

-- +goose Up

ALTER TABLE org_promoters ADD COLUMN slug text;

CREATE UNIQUE INDEX org_promoters_slug_uq
    ON org_promoters (lower(slug))
    WHERE slug IS NOT NULL;

COMMENT ON COLUMN org_promoters.slug IS
    'Public page address: tickets.arenasoldout.com/{slug}. Platform-wide, case-insensitive unique; must not equal an organization slug (the API checks). NULL = no page.';

-- +goose Down

DROP INDEX IF EXISTS org_promoters_slug_uq;
ALTER TABLE org_promoters DROP COLUMN IF EXISTS slug;
