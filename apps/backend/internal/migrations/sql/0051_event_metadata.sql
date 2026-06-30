-- +goose Up
-- =====================================================================
-- arena_new — Event metadata & artists (Wave Events Admin, feature #279 / E-1)
--
-- Extends the events table with content-management metadata required by
-- the public event page (slug, short description, genre, age rating,
-- duration, poster, teaser/trailer URLs, SEO meta fields) and adds a
-- child table for performing artists.
--
-- Design decisions:
--   * The existing `image_url` column is retained verbatim for backward
--     compatibility with the legacy admin UI; the new code writes to
--     `poster_media_id` once Wave G (media management) ships. Until then,
--     readers fall back to image_url when poster_media_id IS NULL.
--   * `slug` is unique per org_id where deleted_at IS NULL (partial unique
--     index). NULL slugs are permitted (legacy events) and excluded from
--     the unique constraint.
--   * `age_rating` is constrained to the standard Russian/EU rating set
--     ('0+', '6+', '12+', '16+', '18+', 'NR') via CHECK. NR = Not Rated.
--   * `short_description` is capped at 280 chars (Twitter-style summary)
--     via CHECK constraint; long-form lives in events.description.
--   * `duration_minutes` is a positive integer; runtime in minutes.
--   * `poster_media_id` FK is declared but the referenced media table is
--     not yet present — the FK is added via a deferred ALTER TABLE in a
--     later migration (Wave G). For now the column is a plain UUID.
--     This avoids a forward dependency between migrations.
--   * The child `event_artists` table uses uuidv7 PK and standard
--     timestamps + soft-delete (deleted_at) matching the rest of the
--     schema. FK to events ON DELETE CASCADE keeps cleanup simple.
-- =====================================================================

ALTER TABLE events
    ADD COLUMN slug              text    NULL,
    ADD COLUMN short_description text    NULL,
    ADD COLUMN genre             text    NULL,
    ADD COLUMN age_rating        text    NULL,
    ADD COLUMN duration_minutes  integer NULL,
    ADD COLUMN poster_media_id   uuid    NULL,
    ADD COLUMN teaser_url        text    NULL,
    ADD COLUMN trailer_url       text    NULL,
    ADD COLUMN meta_description  text    NULL,
    ADD COLUMN meta_keywords     text    NULL;

-- short_description capped at 280 chars (Twitter-style summary).
ALTER TABLE events
    ADD CONSTRAINT events_short_description_length_check
        CHECK (short_description IS NULL OR char_length(short_description) <= 280);

-- age_rating constrained to the standard rating set.
ALTER TABLE events
    ADD CONSTRAINT events_age_rating_check
        CHECK (age_rating IS NULL OR age_rating IN ('0+', '6+', '12+', '16+', '18+', 'NR'));

-- duration_minutes must be positive when set.
ALTER TABLE events
    ADD CONSTRAINT events_duration_minutes_positive_check
        CHECK (duration_minutes IS NULL OR duration_minutes > 0);

-- Partial unique index: slug unique per organization for active events.
CREATE UNIQUE INDEX events_org_slug_unique
    ON events (org_id, slug)
    WHERE deleted_at IS NULL AND slug IS NOT NULL;

COMMENT ON COLUMN events.slug IS
    'URL-safe identifier for the public event page, unique per org for '
    'active events. NULL on legacy events; populated by the admin UI on '
    'first save.';
COMMENT ON COLUMN events.short_description IS
    'Twitter-style summary (<= 280 chars) shown on listing cards and '
    'social previews. Long-form description remains in events.description.';
COMMENT ON COLUMN events.genre IS
    'Free-form genre label for MVP (e.g. "Rock", "Theatre", "Comedy"). '
    'Will be replaced by a reference-table FK in a future wave.';
COMMENT ON COLUMN events.age_rating IS
    'Audience age rating: 0+, 6+, 12+, 16+, 18+, or NR (Not Rated). '
    'Optional; NULL means unspecified.';
COMMENT ON COLUMN events.duration_minutes IS
    'Total event runtime in minutes (positive integer). Optional.';
COMMENT ON COLUMN events.poster_media_id IS
    'FK (deferred to Wave G) to the media library row holding the event '
    'poster. While media management is unimplemented this column stays '
    'NULL and readers fall back to events.image_url.';
COMMENT ON COLUMN events.teaser_url IS
    'Public URL to a short teaser video (e.g. YouTube short). Optional.';
COMMENT ON COLUMN events.trailer_url IS
    'Public URL to a full trailer video. Optional.';
COMMENT ON COLUMN events.meta_description IS
    'SEO meta description (rendered in <meta name="description">). '
    'Optional; admin UI suggests using short_description if unset.';
COMMENT ON COLUMN events.meta_keywords IS
    'SEO meta keywords (rendered in <meta name="keywords">). Optional.';
COMMENT ON COLUMN events.image_url IS
    'Legacy event image URL. Retained read-only for backfill. New writes '
    'go to poster_media_id once Wave G media management ships.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Child table: event_artists
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE event_artists (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    event_id        uuid        NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name            text        NOT NULL,
    role            text        NULL,
    bio             text        NULL,
    photo_media_id  uuid        NULL,
    sort_order      integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz                              -- NULL = active
);

-- Index: list artists for an event quickly, ordered by sort_order.
CREATE INDEX event_artists_event_id_active
    ON event_artists (event_id, sort_order)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE event_artists IS
    'Performing artists / cast associated with an event. Ordered via '
    'sort_order. Soft-delete via deleted_at. Feature #279 / E-1.';
COMMENT ON COLUMN event_artists.event_id IS
    'Parent event. ON DELETE CASCADE — artists go away with their event.';
COMMENT ON COLUMN event_artists.name IS
    'Artist display name (required).';
COMMENT ON COLUMN event_artists.role IS
    'Role / billing line (e.g. "Lead Vocals", "Director"). Optional.';
COMMENT ON COLUMN event_artists.bio IS
    'Free-form biography blurb shown on the public event page. Optional.';
COMMENT ON COLUMN event_artists.photo_media_id IS
    'FK (deferred to Wave G) to the media library row holding the '
    'artist headshot. NULL until Wave G media management ships.';
COMMENT ON COLUMN event_artists.sort_order IS
    'Display order on the event page (ascending). Default 0.';
COMMENT ON COLUMN event_artists.deleted_at IS
    'Soft-delete marker (timestamptz). NULL means the artist is active.';

-- +goose Down
DROP TABLE IF EXISTS event_artists;

DROP INDEX IF EXISTS events_org_slug_unique;

ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_duration_minutes_positive_check,
    DROP CONSTRAINT IF EXISTS events_age_rating_check,
    DROP CONSTRAINT IF EXISTS events_short_description_length_check;

ALTER TABLE events
    DROP COLUMN IF EXISTS meta_keywords,
    DROP COLUMN IF EXISTS meta_description,
    DROP COLUMN IF EXISTS trailer_url,
    DROP COLUMN IF EXISTS teaser_url,
    DROP COLUMN IF EXISTS poster_media_id,
    DROP COLUMN IF EXISTS duration_minutes,
    DROP COLUMN IF EXISTS age_rating,
    DROP COLUMN IF EXISTS genre,
    DROP COLUMN IF EXISTS short_description,
    DROP COLUMN IF EXISTS slug;
