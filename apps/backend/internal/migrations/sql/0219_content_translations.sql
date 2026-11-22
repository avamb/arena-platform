-- 0219_content_translations.sql — organizer-editable translations of an
-- event's content (spec 08_architecture/63 §16.1, task Л-1).
--
-- events.content_locale    the language of the original text: a lowercase
--                          two-letter code. Existing rows take it from the
--                          organization's default_locale ("ru-RU" -> "ru"),
--                          'en' when that is not two letters. A BEFORE INSERT
--                          trigger fills a NULL the same way, so the existing
--                          INSERT code needs no change.
-- events.report_text_html  the photo-report text of the original.
-- content_translations     one row per (owner, locale): the translated name
--                          (owner change 22.11: only when it differs), short
--                          description, description and report text, written
--                          by hand (manual) or by the machine translator.
--                          source_hash is the sha256 of the original fields at
--                          the moment of translation, so a stale translation
--                          is found by comparing it with the current original.
--
-- Deleting an event row deletes its translations (AFTER DELETE trigger). The
-- series table is not on master yet, so a series has no trigger here.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE events ADD COLUMN content_locale text NULL;

UPDATE events e
   SET content_locale = CASE
           WHEN lower(split_part(o.default_locale, '-', 1)) ~ '^[a-z]{2}$'
               THEN lower(split_part(o.default_locale, '-', 1))
           ELSE 'en'
       END
  FROM organizations o
 WHERE o.id = e.org_id;

UPDATE events SET content_locale = 'en' WHERE content_locale IS NULL;

ALTER TABLE events ALTER COLUMN content_locale SET NOT NULL;
ALTER TABLE events ADD CONSTRAINT events_content_locale_check
    CHECK (content_locale ~ '^[a-z]{2}$');

ALTER TABLE events ADD COLUMN report_text_html text NULL;

COMMENT ON COLUMN events.content_locale IS
    'Language of the original event texts, lowercase two letters. Filled from organizations.default_locale on INSERT when NULL.';
COMMENT ON COLUMN events.report_text_html IS
    'Photo-report text of the original, sanitized HTML (richtext description profile).';

-- +goose StatementBegin
CREATE FUNCTION events_fill_content_locale() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    loc text;
BEGIN
    IF NEW.content_locale IS NULL THEN
        SELECT lower(split_part(default_locale, '-', 1)) INTO loc
          FROM organizations WHERE id = NEW.org_id;
        IF loc IS NULL OR loc !~ '^[a-z]{2}$' THEN
            loc := 'en';
        END IF;
        NEW.content_locale := loc;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER events_fill_content_locale
    BEFORE INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION events_fill_content_locale();

CREATE TABLE content_translations (
    owner_type        text        NOT NULL CHECK (owner_type IN ('event', 'series')),
    owner_id          uuid        NOT NULL,
    org_id            uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    locale            text        NOT NULL CHECK (locale IN ('cs', 'de', 'en', 'es', 'et', 'fi', 'fr', 'he', 'hu', 'it', 'lt', 'lv', 'pl', 'ru', 'uk')),
    name              text        NULL,
    short_description text        NULL,
    description_html  text        NULL,
    report_text_html  text        NULL,
    source            text        NOT NULL CHECK (source IN ('manual', 'machine')),
    source_hash       text        NOT NULL,
    updated_by        uuid        NULL REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_type, owner_id, locale)
);

CREATE INDEX content_translations_org_id_idx ON content_translations (org_id);

COMMENT ON TABLE content_translations IS
    'Translations of an event or series content per locale (spec 63 §16). A manual row is never replaced by a machine translation. source_hash = sha256 of the original fields at translation time.';
COMMENT ON COLUMN content_translations.name IS
    'Translated title, only when it differs from the original (owner change 22.11). NULL = the original name.';

-- +goose StatementBegin
CREATE FUNCTION events_delete_content_translations() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM content_translations
     WHERE owner_type = 'event' AND owner_id = OLD.id;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER events_delete_content_translations
    AFTER DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION events_delete_content_translations();

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS events_delete_content_translations ON events;
DROP FUNCTION IF EXISTS events_delete_content_translations();
DROP TABLE IF EXISTS content_translations;

DROP TRIGGER IF EXISTS events_fill_content_locale ON events;
DROP FUNCTION IF EXISTS events_fill_content_locale();

ALTER TABLE events DROP COLUMN IF EXISTS report_text_html;
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_content_locale_check;
ALTER TABLE events DROP COLUMN IF EXISTS content_locale;
