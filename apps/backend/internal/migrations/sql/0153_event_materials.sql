-- 0153_event_materials.sql — HP-1, the data model of an event's page
-- materials (task 1-1 of the event-center execution spec, plan stage 1):
--
--   event_gallery_items    up to 6 photos per event        (position 0..5)
--   event_videos           up to 4 video links per event   (position 0..3)
--   event_partner_links    up to 3 "where else to buy"     (position 0..2)
--   event_map_points       one map point per event
--   event_report_photos    the photo report, up to 30      (position 0..29)
--   event_page_stats       per-day page counters (task 2-3 / 2-4, HP-2a)
--
-- Limits live in the schema twice on purpose: `position` is a smallint
-- CHECKed to 0..N-1 and (event_id, position) is UNIQUE, so no event can hold
-- more than N rows even when two requests race (a count-then-insert in a
-- handler cannot guarantee that). The Go side names the same numbers in
-- internal/platform/eventmaterials, and a test there reads this file, so
-- raising a limit means one migration plus one constant, never one alone.
-- Because (event_id, position) is the identity of a row, a handler that
-- reorders items must do it in one transaction with the constraint DEFERRED
-- (the unique constraints are DEFERRABLE INITIALLY IMMEDIATE). Such a
-- constraint cannot be used as an ON CONFLICT target.
--
-- Photos are media_objects rows (media_id) with the new owner types
-- 'event_gallery' and 'event_report' — mediastore.AllowedOwnerTypes and the
-- CHECK below widen together, and TestAllowedOwnerTypes_MatchMigrationCheckConstraint
-- keeps the pair honest. Every table carries org_id and a composite foreign
-- key (event_id, org_id) -> events(id, org_id), so a row can only name an
-- event of its own organization and disappears with a hard-deleted event.
--
-- The photo report is hidden until the event date by the READ query (task
-- 1-2b), not by a column: the date is the event's own sessions, which can
-- move, so a stored "visible from" would drift.
--
-- No permission is seeded: materials are edited under the existing
-- event.update, so the superadmin parity guard has nothing to check.

-- +goose Up
-- The foreign keys below touch busy tables: fail fast rather than queue
-- behind a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

-- Media owner types for the gallery and the photo report.
ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report'
        ));

CREATE TABLE event_gallery_items (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid        NOT NULL,
    event_id   uuid        NOT NULL,
    media_id   uuid        NOT NULL REFERENCES media_objects(id),
    position   smallint    NOT NULL,
    caption    text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_gallery_items_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_gallery_items_position_check CHECK (position BETWEEN 0 AND 5),
    CONSTRAINT event_gallery_items_caption_check
        CHECK (caption IS NULL OR char_length(caption) BETWEEN 1 AND 200),
    CONSTRAINT event_gallery_items_position_uq UNIQUE (event_id, position)
        DEFERRABLE INITIALLY IMMEDIATE,
    CONSTRAINT event_gallery_items_media_uq UNIQUE (event_id, media_id)
);

CREATE INDEX event_gallery_items_org_idx ON event_gallery_items (org_id);
CREATE INDEX event_gallery_items_media_idx ON event_gallery_items (media_id);

-- A video is a LINK: provider plus the id parsed out of it, never the bytes.
-- youtube / vimeo / rutube carry a parsed video_id (the page builds the
-- privacy-friendly embed from it); 'https' is any other https page, shown as
-- a plain link, so it has no id.
CREATE TABLE event_videos (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid        NOT NULL,
    event_id   uuid        NOT NULL,
    provider   text        NOT NULL,
    video_id   text,
    url        text        NOT NULL,
    position   smallint    NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_videos_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_videos_position_check CHECK (position BETWEEN 0 AND 3),
    CONSTRAINT event_videos_provider_check
        CHECK (provider IN ('youtube', 'vimeo', 'rutube', 'https')),
    CONSTRAINT event_videos_id_check CHECK (
        (provider = 'https' AND video_id IS NULL)
        OR (provider <> 'https' AND video_id IS NOT NULL AND char_length(video_id) BETWEEN 1 AND 64)
    ),
    CONSTRAINT event_videos_url_check
        CHECK (url ~ '^https://' AND char_length(url) <= 500),
    CONSTRAINT event_videos_position_uq UNIQUE (event_id, position)
        DEFERRABLE INITIALLY IMMEDIATE
);

CREATE INDEX event_videos_org_idx ON event_videos (org_id);

-- "Where else to buy": another seller of the same event. seller_name is shown
-- next to every link so the buyer knows who they would buy from.
CREATE TABLE event_partner_links (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id      uuid        NOT NULL,
    event_id    uuid        NOT NULL,
    url         text        NOT NULL,
    seller_name text        NOT NULL,
    position    smallint    NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_partner_links_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_partner_links_position_check CHECK (position BETWEEN 0 AND 2),
    CONSTRAINT event_partner_links_url_check
        CHECK (url ~ '^https://' AND char_length(url) <= 500),
    CONSTRAINT event_partner_links_seller_check
        CHECK (char_length(btrim(seller_name)) BETWEEN 1 AND 120),
    CONSTRAINT event_partner_links_position_uq UNIQUE (event_id, position)
        DEFERRABLE INITIALLY IMMEDIATE
);

CREATE INDEX event_partner_links_org_idx ON event_partner_links (org_id);

-- One point on the map per event (PRIMARY KEY event_id). The page shows a
-- link and a static scheme built from the coordinates.
CREATE TABLE event_map_points (
    event_id   uuid          PRIMARY KEY,
    org_id     uuid          NOT NULL,
    latitude   numeric(9, 6) NOT NULL,
    longitude  numeric(9, 6) NOT NULL,
    label      text,
    created_at timestamptz   NOT NULL DEFAULT now(),
    updated_at timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT event_map_points_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_map_points_latitude_check CHECK (latitude BETWEEN -90 AND 90),
    CONSTRAINT event_map_points_longitude_check CHECK (longitude BETWEEN -180 AND 180),
    CONSTRAINT event_map_points_label_check
        CHECK (label IS NULL OR char_length(label) BETWEEN 1 AND 200)
);

CREATE INDEX event_map_points_org_idx ON event_map_points (org_id);

-- The photo report: photos taken at the event, shown after its date.
CREATE TABLE event_report_photos (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid        NOT NULL,
    event_id   uuid        NOT NULL,
    media_id   uuid        NOT NULL REFERENCES media_objects(id),
    position   smallint    NOT NULL,
    caption    text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_report_photos_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_report_photos_position_check CHECK (position BETWEEN 0 AND 29),
    CONSTRAINT event_report_photos_caption_check
        CHECK (caption IS NULL OR char_length(caption) BETWEEN 1 AND 200),
    CONSTRAINT event_report_photos_position_uq UNIQUE (event_id, position)
        DEFERRABLE INITIALLY IMMEDIATE,
    CONSTRAINT event_report_photos_media_uq UNIQUE (event_id, media_id)
);

CREATE INDEX event_report_photos_org_idx ON event_report_photos (org_id);
CREATE INDEX event_report_photos_media_idx ON event_report_photos (media_id);

-- Per-day counters of the public event page (HP-2a, written by task 2-3 and
-- read by 2-4). The row carries NO visitor data: no IP, country, hour or user
-- agent, only the event, the day, the kind, and the two utm values from the
-- address, cut and restricted to a safe alphabet, so a row on a small event
-- cannot become a quasi-identifier. A client that looks like a bot is counted
-- apart in suspect_hits. The counters are an organizer's guide, not proof.
-- kind is an allowlist: a new kind needs a migration and a metric label.
CREATE TABLE event_page_stats (
    event_id     uuid   NOT NULL,
    org_id       uuid   NOT NULL,
    day          date   NOT NULL,
    kind         text   NOT NULL,
    utm_source   text   NOT NULL DEFAULT '',
    utm_campaign text   NOT NULL DEFAULT '',
    hits         bigint NOT NULL DEFAULT 0,
    suspect_hits bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (event_id, day, kind, utm_source, utm_campaign),
    CONSTRAINT event_page_stats_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_page_stats_kind_check
        CHECK (kind IN ('page_view', 'video_play', 'partner_click')),
    CONSTRAINT event_page_stats_utm_source_check
        CHECK (utm_source ~ '^[A-Za-z0-9._~-]{0,64}$'),
    CONSTRAINT event_page_stats_utm_campaign_check
        CHECK (utm_campaign ~ '^[A-Za-z0-9._~-]{0,64}$'),
    CONSTRAINT event_page_stats_counts_check CHECK (hits >= 0 AND suspect_hits >= 0)
);

-- The organization-wide export reads by day.
CREATE INDEX event_page_stats_org_day_idx ON event_page_stats (org_id, day);

COMMENT ON TABLE event_gallery_items IS
    'HP-1: up to 6 photos of an event page. position 0..5 + UNIQUE (event_id, position) cap it.';
COMMENT ON TABLE event_videos IS
    'HP-1: up to 4 video LINKS of an event page (provider + parsed id), never bytes.';
COMMENT ON TABLE event_partner_links IS
    'HP-1: up to 3 "where else to buy" links, each with the seller named.';
COMMENT ON TABLE event_map_points IS
    'HP-1: the one map point of an event.';
COMMENT ON TABLE event_report_photos IS
    'HP-1: the photo report, hidden until the event date by the read query.';
COMMENT ON TABLE event_page_stats IS
    'HP-2a: per-day page counters, no visitor data. hits are people, suspect_hits look like bots.';

-- +goose Down
DROP TABLE IF EXISTS event_page_stats;
DROP TABLE IF EXISTS event_report_photos;
DROP TABLE IF EXISTS event_map_points;
DROP TABLE IF EXISTS event_partner_links;
DROP TABLE IF EXISTS event_videos;
DROP TABLE IF EXISTS event_gallery_items;

-- Reverting drops the media rows uploaded for the gallery and the photo
-- report, otherwise the narrowed CHECK would fail to validate. This is a
-- hard DELETE: the media-gc sweep only reclaims rows that carry deleted_at,
-- so the stored bytes of these objects stay orphaned in the media backend
-- (acceptable for a Down that production never runs).
DELETE FROM media_objects WHERE owner_type IN ('event_gallery', 'event_report');

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster'
        ));
