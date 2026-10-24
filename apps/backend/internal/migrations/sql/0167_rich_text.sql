-- 0167_rich_text.sql — RT-02 of the rich-text spec (08_architecture/43): the
-- format flag of the two formatted texts, the room for their HTML and the
-- backup table of the backfill command.
--
-- events.description_format and event_email_notes.note_format say how the
-- stored text is to be read: 'text' is the old plain text with the
-- "**bold**, _italic_, - list" marks, 'art' is "text Arena", the small HTML
-- subset of internal/platform/richtext. The server never guesses it from the
-- tags. The flag of an event covers its translations too (the i18n_text rows
-- of namespace event.description), so the backfill converts them together.
-- Every writer goes through richtext.Normalize and stores 'art'; a row that
-- nobody has written since stays 'text' and is read through the legacy
-- converter until the arena-richtext-backfill command has converted it.
--
-- The note's stored length is now the length of its HTML: 8000 characters
-- (richtext.ProfileEmailNote.MaxHTML). The visible 2000 are checked by Go.
-- The old CHECK is replaced with NOT VALID + VALIDATE, so the table is locked
-- only for the catalog change and not for the scan.
--
-- richtext_backfill_backup keeps what the backfill replaced, one row per text:
-- entity is event_description, event_description_i18n or event_email_note,
-- row_id is the event id, locale is '' except for the translations.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE events
    ADD COLUMN description_format text NOT NULL DEFAULT 'text';
ALTER TABLE events
    ADD CONSTRAINT events_description_format_check
        CHECK (description_format IN ('text', 'art')) NOT VALID;
ALTER TABLE events VALIDATE CONSTRAINT events_description_format_check;

ALTER TABLE event_email_notes
    ADD COLUMN note_format text NOT NULL DEFAULT 'text';
ALTER TABLE event_email_notes
    ADD CONSTRAINT event_email_notes_format_check
        CHECK (note_format IN ('text', 'art')) NOT VALID;
ALTER TABLE event_email_notes VALIDATE CONSTRAINT event_email_notes_format_check;

ALTER TABLE event_email_notes
    DROP CONSTRAINT event_email_notes_length_check;
ALTER TABLE event_email_notes
    ADD CONSTRAINT event_email_notes_length_check
        CHECK (char_length(note) BETWEEN 1 AND 8000) NOT VALID;
ALTER TABLE event_email_notes VALIDATE CONSTRAINT event_email_notes_length_check;

CREATE TABLE richtext_backfill_backup (
    run_id     uuid        NOT NULL,
    entity     text        NOT NULL,
    row_id     text        NOT NULL,
    locale     text        NOT NULL DEFAULT '',
    old_value  text        NOT NULL,
    old_format text        NOT NULL,
    new_value  text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, entity, row_id, locale),
    CONSTRAINT richtext_backfill_backup_entity_check
        CHECK (entity IN ('event_description', 'event_description_i18n', 'event_email_note'))
);

CREATE INDEX richtext_backfill_backup_created_idx ON richtext_backfill_backup (created_at);

-- +goose Down
DROP TABLE IF EXISTS richtext_backfill_backup;

-- The old 2000-character CHECK is put back NOT VALID: a note stored as HTML
-- since this migration may be longer, and the Down must not fail on it.
ALTER TABLE event_email_notes
    DROP CONSTRAINT IF EXISTS event_email_notes_length_check;
ALTER TABLE event_email_notes
    ADD CONSTRAINT event_email_notes_length_check
        CHECK (char_length(note) BETWEEN 1 AND 2000) NOT VALID;
ALTER TABLE event_email_notes
    DROP CONSTRAINT IF EXISTS event_email_notes_format_check;
ALTER TABLE event_email_notes DROP COLUMN IF EXISTS note_format;

ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_description_format_check;
ALTER TABLE events DROP COLUMN IF EXISTS description_format;
