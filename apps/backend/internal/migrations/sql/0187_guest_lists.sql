-- 0188_guest_lists.sql — the organizer's guest list of a session (owner
-- decision 2026-11-02, spec 51 section 4.4).
--
-- A guest list is a CSV of the session's active tickets: guest, category,
-- seat, ticket, barcode, VIP, entry, order, e-mail, phone, invitation. The
-- file is served on demand (GET .../sessions/{id}/guest-list.csv) and sent
-- automatically three times per session: 24 hours before the start, 3 hours
-- before it and at "doors open" (the start when no doors time is set). Nothing
-- is sent while sales go on, and a session without active tickets gets no file.
--
-- guest_list_subscriptions: one row per member of an organization who ticked
-- "send me the guest list". The file holds buyers' personal data, so it goes
-- ONLY to that person: the private chat with the event center bot when they
-- are linked to it, else their e-mail (owner decision: e-mail is the fallback
-- for people without Telegram). Never to a group chat. Only owners and
-- managers (membership roles org_admin and organizer) get a file.
--
-- guest_list_deliveries: one row per session, moment and person, written by
-- the guest_list.schedule worker job when the moment comes (the UNIQUE key
-- makes a second pass a no-op, so a list is never sent twice). channel says
-- how it goes: 'telegram' rows are claimed by the bot process
-- (POST /v1/bot/guest-list-deliveries/claim, a 10-minute lease) which fetches
-- the CSV as that person and reports back; 'email' rows, and 'telegram' rows
-- of a person no longer linked to the bot, are mailed by the worker itself.
-- attempts is also the claim token a report must name. Rows are deleted 90
-- days after their moment.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE guest_list_subscriptions (
    org_id     uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

COMMENT ON TABLE guest_list_subscriptions IS
    'Members who receive the guest list of every session of the organization automatically (24 h, 3 h before the start and at doors open).';

CREATE TABLE guest_list_deliveries (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id       uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slot             text        NOT NULL CHECK (slot IN ('h24', 'h3', 'doors')),
    channel          text        NOT NULL CHECK (channel IN ('telegram', 'email')),
    due_at           timestamptz NOT NULL,
    attempts         integer     NOT NULL DEFAULT 0,
    claimed_at       timestamptz,
    sent_at          timestamptz,
    last_error       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT guest_list_deliveries_once UNIQUE (session_id, slot, user_id)
);

CREATE INDEX guest_list_deliveries_open_idx
    ON guest_list_deliveries (channel, due_at)
    WHERE sent_at IS NULL AND attempts < 3;

-- worker_jobs.cleanup deletes rows whose moment is older than 90 days.
CREATE INDEX guest_list_deliveries_due_idx ON guest_list_deliveries (due_at);

COMMENT ON TABLE guest_list_deliveries IS
    'One automatic guest list per session, moment and person; the unique key keeps a list from being sent twice.';
COMMENT ON COLUMN guest_list_deliveries.slot IS
    'h24 = 24 hours before the start, h3 = 3 hours before, doors = at doors open (the start when no doors time is set).';
COMMENT ON COLUMN guest_list_deliveries.claimed_at IS
    'When the bot or the worker took the row; a row taken more than 10 minutes ago and not sent may be taken again.';
COMMENT ON COLUMN guest_list_deliveries.last_error IS
    'Why the last attempt failed (no rights any more, Telegram refused). Never holds data from the file.';

-- +goose Down

DROP TABLE IF EXISTS guest_list_deliveries;
DROP TABLE IF EXISTS guest_list_subscriptions;
