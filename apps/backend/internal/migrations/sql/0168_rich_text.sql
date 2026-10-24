-- 0168_rich_text.sql — RT-02 of the rich-text spec (08_architecture/43): the
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
--
-- Locking: goose runs this file in ONE transaction, so every VALIDATE below
-- scans its table while the ACCESS EXCLUSIVE lock taken by the ALTER of the
-- same transaction is still held. NOT VALID + VALIDATE would therefore not
-- shorten the lock here. The tables (events, event_email_notes) are small and
-- the scans are a few milliseconds; lock_timeout bounds the wait for the lock.
-- The two format CHECKs are added valid at once: the column has just been
-- filled with the constant 'text'. Only the length CHECK of the note, whose
-- old definition is replaced, keeps the NOT VALID + VALIDATE pair so that the
-- statement order stays obvious.
--
-- richtext_backfill_backup keeps what the backfill replaced, one row per text:
-- entity is event_description, event_description_i18n or event_email_note,
-- row_id is the event id, locale is '' except for the translations. The
-- backfill command deletes rows older than N days on request
-- (--purge-backup-older-than).

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE events
    ADD COLUMN description_format text NOT NULL DEFAULT 'text';
ALTER TABLE events
    ADD CONSTRAINT events_description_format_check
        CHECK (description_format IN ('text', 'art'));

ALTER TABLE event_email_notes
    ADD COLUMN note_format text NOT NULL DEFAULT 'text';
ALTER TABLE event_email_notes
    ADD CONSTRAINT event_email_notes_format_check
        CHECK (note_format IN ('text', 'art'));

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
-- Dropping the flag columns would leave the ART written since the release
-- without its marker, and the code before this migration would show the tags
-- as text. So the Down REFUSES while any event or note is stored as 'art'.
-- Rolling the code back therefore needs a manual conversion first, per text:
-- richtext.ToLegacy (or a copy of the backup row's old_value of the backfill)
-- into the column, then description_format / note_format set to 'text' with
-- an UPDATE. The rows of i18n_text (namespace event.description) follow the
-- flag of their event and are converted together with it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM events WHERE description_format = 'art')
       OR EXISTS (SELECT 1 FROM event_email_notes WHERE note_format = 'art') THEN
        RAISE EXCEPTION '0168 down refused: events or event_email_notes hold rich text (format art); convert them to plain text and set the flag to text first, see the comment of this migration';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TABLE IF EXISTS richtext_backfill_backup;

-- The old 2000-character CHECK is put back NOT VALID: a note stored as 'text'
-- since this migration may be longer than 2000 characters of the old
-- plain-text limit, and the Down must not fail on it.
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
