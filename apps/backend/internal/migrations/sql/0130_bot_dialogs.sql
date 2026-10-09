-- 0130_bot_dialogs.sql — the Telegram event-center bot keeps its short
-- dialogs in the database (spec 08_architecture/35 §4.1, EC-01).
--
-- Until now every multi-step dialog of the bot (the team invite, the session
-- move/cancel, the organizer application) lived in a map inside the arena-bot
-- process, so a restart or a deploy silently dropped it: an owner typed the
-- colleague's e-mail after a deploy, the bot no longer knew it was waiting for
-- one, and nothing reached the API. bot_dialogs is that memory on disk. One
-- live dialog per Telegram account and kind (a person cannot be inviting two
-- colleagues at once); the expiry slides 30 minutes forward on every answer,
-- and a row past its expiry is answered "this dialog has expired" once and
-- then deleted. The event wizard keeps its own, richer bot_drafts row.
--
-- bot_drafts gains reminded_at for the 24-hour idle reminder of spec 28 §5.3
-- (never built until EC-01): the sweep sends one reminder per draft and only
-- an edit of the draft (which rewrites the row) makes it eligible again.
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up

CREATE TABLE bot_dialogs (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    telegram_user_id bigint      NOT NULL REFERENCES bot_telegram_links(telegram_user_id) ON DELETE CASCADE,
    -- The organization the dialog acts in; NULL for a dialog that is not
    -- about one organization.
    org_id           uuid        NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind             text        NOT NULL CHECK (kind <> '' AND length(kind) <= 32),
    step             text        NOT NULL DEFAULT '',
    state            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bot_dialogs_user_kind_uq UNIQUE (telegram_user_id, kind)
);

CREATE INDEX bot_dialogs_expires_idx ON bot_dialogs (expires_at);

COMMENT ON TABLE bot_dialogs IS
    'Short multi-step dialogs of the Telegram event-center bot (team invite, session move, ...), '
    'kept here instead of in process memory so a bot restart loses nothing. One live dialog per '
    'Telegram account and kind; expires_at slides 30 minutes forward on every answer.';

ALTER TABLE bot_drafts ADD COLUMN reminded_at timestamptz NULL;

COMMENT ON COLUMN bot_drafts.reminded_at IS
    'When the 24-hour idle reminder about this draft was sent; NULL until then. '
    'Rewriting the draft (any answer) clears it.';

-- +goose Down

ALTER TABLE bot_drafts DROP COLUMN IF EXISTS reminded_at;

DROP TABLE IF EXISTS bot_dialogs;
