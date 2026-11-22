-- 0221_event_series.sql — a tour (series of events), spec 63 section 15.1 (Л-2).
--
-- event_series is a tour of an organization: one name, one public address
-- and one set of page materials shared by several events (one per city). An
-- event names its tour in events.series_id. A tour is archived, never deleted
-- by the API; if a row is deleted anyway, its events stay and lose the link.
--
-- Same organization: events.series_id is a COMPOSITE foreign key
-- (series_id, org_id) -> event_series (id, org_id), like the promoters of
-- migration 0113, so an event can never name another organization's tour.
-- ON DELETE SET NULL (series_id) (PostgreSQL 15+) clears only series_id and
-- keeps events.org_id.
--
-- One slug namespace: the site shows a tour and an event at the same kind of
-- address (/events/<slug>/), so a tour slug must not equal the slug of an
-- ACTIVE event of the same organization (deleted_at IS NULL AND slug IS NOT
-- NULL, the predicate of events_org_slug_unique from 0051), case-insensitive,
-- and the reverse. The API checks it first (gen.SeriesSlugTaken and the widened
-- gen.EventSlugTaken); the triggers below are the backstop: they take the
-- organization's advisory lock (hashtext('slugns:' || org_id)) for the rest of
-- the transaction, so two writers cannot pass the check at once, and raise
-- 23505 with the constraint name event_series_slug_namespace.
--
-- Media of a tour live in media_objects with the new owner_type
-- 'event_series' (mediastore.AllowedOwnerTypes gains it too).
--
-- No new permissions: reading a tour needs event.read, writing event.update.

-- +goose Up

-- ALTER TABLE events takes ACCESS EXCLUSIVE for a moment; a deploy that meets
-- a long transaction fails fast instead of queueing every request behind it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE event_series (
    id                     uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                 uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    slug                   text        NOT NULL,
    name                   text        NOT NULL,
    description_html       text        NULL,
    poster_media_id        uuid        NULL REFERENCES media_objects(id) ON DELETE SET NULL,
    hero_media_id          uuid        NULL REFERENCES media_objects(id) ON DELETE SET NULL,
    hero_video_url         text        NULL,
    partners_text_html     text        NULL,
    hide_children_in_lists boolean     NOT NULL DEFAULT true,
    position               integer     NOT NULL DEFAULT 0,
    archived_at            timestamptz NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_series_id_org_uq UNIQUE (id, org_id),
    CONSTRAINT event_series_name_len CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    CONSTRAINT event_series_slug_format CHECK (
        char_length(slug) BETWEEN 1 AND 120
        AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
    )
);

CREATE UNIQUE INDEX event_series_org_slug_uq ON event_series (org_id, lower(slug));
CREATE INDEX event_series_org_position_idx ON event_series (org_id, position);

COMMENT ON TABLE event_series IS
    'A tour of an organization (spec 63 section 15.1): shared name, address and page materials of several events.';
COMMENT ON COLUMN event_series.slug IS
    'Public address /events/<slug>/. Shares one namespace with the active event slugs of the organization (trigger event_series_slug_namespace).';
COMMENT ON COLUMN event_series.description_html IS
    'Description as HTML, written only through the richtext sanitizer.';
COMMENT ON COLUMN event_series.poster_media_id IS 'Portrait poster 4:5 (media_objects owner_type event_series).';
COMMENT ON COLUMN event_series.hero_media_id IS 'Wide photo 16:9 (media_objects owner_type event_series).';
COMMENT ON COLUMN event_series.hero_video_url IS 'YouTube or Vimeo link shown instead of the wide photo.';
COMMENT ON COLUMN event_series.hide_children_in_lists IS
    'true = the site lists the tour once instead of every event (city) of it.';

ALTER TABLE events ADD COLUMN series_id uuid NULL;

ALTER TABLE events
    ADD CONSTRAINT events_series_fk FOREIGN KEY (series_id, org_id)
        REFERENCES event_series (id, org_id) ON DELETE SET NULL (series_id);

CREATE INDEX events_series_id_idx ON events (series_id) WHERE series_id IS NOT NULL;

COMMENT ON COLUMN events.series_id IS
    'The tour of the event (event_series of the same organization); NULL = not in a tour.';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION event_series_slug_namespace_series()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND lower(NEW.slug) = lower(OLD.slug)
       AND NEW.org_id = OLD.org_id THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('slugns:' || NEW.org_id::text));
    IF EXISTS (
        SELECT 1 FROM events e
        WHERE  e.org_id = NEW.org_id
          AND  e.deleted_at IS NULL
          AND  e.slug IS NOT NULL
          AND  lower(e.slug) = lower(NEW.slug)
    ) THEN
        RAISE EXCEPTION 'tour slug % is used by an event of the organization', NEW.slug
            USING ERRCODE = 'unique_violation',
                  CONSTRAINT = 'event_series_slug_namespace',
                  TABLE = 'event_series';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION event_series_slug_namespace_events()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.slug IS NULL OR NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE'
       AND OLD.slug IS NOT NULL
       AND OLD.deleted_at IS NULL
       AND lower(NEW.slug) = lower(OLD.slug)
       AND NEW.org_id = OLD.org_id THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('slugns:' || NEW.org_id::text));
    IF EXISTS (
        SELECT 1 FROM event_series s
        WHERE  s.org_id = NEW.org_id
          AND  lower(s.slug) = lower(NEW.slug)
    ) THEN
        RAISE EXCEPTION 'event slug % is used by a tour of the organization', NEW.slug
            USING ERRCODE = 'unique_violation',
                  CONSTRAINT = 'event_series_slug_namespace',
                  TABLE = 'events';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER event_series_slug_namespace_ins_upd
    BEFORE INSERT OR UPDATE OF slug, org_id ON event_series
    FOR EACH ROW EXECUTE FUNCTION event_series_slug_namespace_series();

CREATE TRIGGER events_series_slug_namespace_ins_upd
    BEFORE INSERT OR UPDATE OF slug, deleted_at, org_id ON events
    FOR EACH ROW EXECUTE FUNCTION event_series_slug_namespace_events();

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
            'event_report',
            'shop_product',
            'shop_digital',
            'payment_receipt',
            'venue_plan_source',
            'event_series'
        ));

-- +goose Down

SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS events_series_slug_namespace_ins_upd ON events;
DROP TRIGGER IF EXISTS event_series_slug_namespace_ins_upd ON event_series;
DROP FUNCTION IF EXISTS event_series_slug_namespace_events();
DROP FUNCTION IF EXISTS event_series_slug_namespace_series();

DROP INDEX IF EXISTS events_series_id_idx;
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_series_fk;
ALTER TABLE events DROP COLUMN IF EXISTS series_id;

DROP TABLE IF EXISTS event_series;

DELETE FROM media_objects WHERE owner_type = 'event_series';

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
            'event_report',
            'shop_product',
            'shop_digital',
            'payment_receipt',
            'venue_plan_source'
        ));
