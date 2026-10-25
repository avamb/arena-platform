-- 0169_session_change_message_format.sql — RT-11 of the rich-text spec
-- (08_architecture/44): the format flag of the organizer's message to buyers
-- about a moved or cancelled session.
--
-- session_changes.message_format says how session_changes.message is to be
-- read: 'text' is the old plain text (every row written before this
-- migration, and a client that does not send a format), 'art' is "text Arena",
-- the small HTML subset of internal/platform/richtext (profile
-- session_message: 1000 visible characters, 4000 characters of HTML, 3 links,
-- https only). The server never guesses it from the tags. sessionchange.Apply
-- is the one writer: an 'art' message goes through richtext.Normalize and is
-- stored as ART, a 'text' message (the Telegram bot, every client that sends
-- no format) is stored as typed. The change and cancel letters read the
-- message through richtext.Render with the flag, so a row queued or written
-- before the release still renders as before.
--
-- Locking: session_changes is a journal that every session move or
-- cancellation appends to. ADD COLUMN with a constant default is a catalog
-- change in PostgreSQL 11 and later (no rewrite); lock_timeout bounds the wait
-- for the ACCESS EXCLUSIVE lock, so a deploy that meets a long transaction
-- fails fast. The CHECK is added with the column, valid at once: every
-- existing row has just received the constant 'text'.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_changes
    ADD COLUMN message_format text NOT NULL DEFAULT 'text';
ALTER TABLE session_changes
    ADD CONSTRAINT session_changes_message_format_check
        CHECK (message_format IN ('text', 'art'));
COMMENT ON COLUMN session_changes.message_format IS
    'How message is stored: text (old plain text) or art (the HTML subset of richtext, profile session_message).';

-- +goose Down
-- Dropping the flag would leave ART written since the release without its
-- marker, and the code before this migration would print the tags as text. So
-- the Down REFUSES while any journal row holds an art message. Rolling the
-- code back needs a manual conversion first: richtext.PlainText of each such
-- message into session_changes.message, then message_format set to 'text'.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM session_changes WHERE message_format = 'art') THEN
        RAISE EXCEPTION '0169 down refused: session_changes hold rich text messages (format art); convert them to plain text and set the flag to text first, see the comment of this migration';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE session_changes
    DROP CONSTRAINT IF EXISTS session_changes_message_format_check;
ALTER TABLE session_changes DROP COLUMN IF EXISTS message_format;
