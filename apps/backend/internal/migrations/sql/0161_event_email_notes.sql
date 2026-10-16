-- 0161_event_email_notes.sql — EC-31 (task 7-1 of the event-center execution
-- spec): the organizer's own text of an event that the ticket and the
-- session-change letters print under "About the event".
--
-- A table of its own, not a column of events: the events row has one shared
-- Go scanner (scanEventRow) fed by about fifteen queries, and a column there
-- would have to be selected by every one of them. The same trade-off was made
-- for event_promoters (0113).
--
-- The text is plain text, already filtered by eventnote.Normalize before it is
-- stored (no HTML, no control characters, at most 5 https links). The length
-- is checked here too, and a test reads this file to keep it equal to
-- eventnote.MaxRunes. A row exists only while the event HAS a note: clearing
-- it deletes the row.
--
-- Every row names the event of its own organization (composite foreign key),
-- and goes with a hard-deleted event. No permission is seeded: the note is
-- edited under the existing event.update.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE event_email_notes (
    event_id   uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL,
    note       text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT event_email_notes_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT event_email_notes_length_check
        CHECK (char_length(note) BETWEEN 1 AND 2000)
);

CREATE INDEX event_email_notes_org_idx ON event_email_notes (org_id);

-- +goose Down
DROP TABLE IF EXISTS event_email_notes;
