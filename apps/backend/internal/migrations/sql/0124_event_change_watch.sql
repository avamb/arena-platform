-- 0124_event_change_watch.sql — tell the operator when an event is published or
-- changes what a buyer sees.
--
-- The operator's Telegram group ("Order ASO") gets a message when an event is
-- published for the first time and when a published event's name, poster, dates
-- or prices change (owner decision 2026-10-07: only the operator, never the
-- organizer's own group). The sales bot already posts there; this adds a switch
-- to it and the memory that makes "changed" decidable.
--
-- Why a memory and not the outbox: the Telegram bot saves an event through the
-- event-bundle import, which raises v1.event.published once and nothing at all
-- for a later rename, a moved date, a new price or a new poster; a price edit
-- through the API raises nothing either. So the watcher (internal/platform/
-- eventwatch, job events.change_watch) compares what an event looks like NOW
-- with what it announced last, whichever path wrote the change.
--
--   sales_notification_subscriptions.on_event_changes — the switch. Default
--     false: an organization's own group never gets it. The operator rows
--     (org_id NULL) are switched on right here.
--   event_watch_snapshots — per published event: the snapshot last announced,
--     and the digest seen on the latest run with the time it was first seen, so
--     a change is announced only once it has stayed the same for a while (an
--     event is saved date by date, and a half-saved event must not be announced).
--   event_watch_state — a single row; seeded_at says the events that already
--     existed were recorded silently, so deploying this announces nothing old.
--
-- No FK from event_watch_snapshots to events: a row must survive an event being
-- unpublished for a while, and a hard-deleted event just leaves a dead row.
-- No permissions: nothing here is reachable through the HTTP API.

-- +goose Up

ALTER TABLE sales_notification_subscriptions
    ADD COLUMN on_event_changes boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN sales_notification_subscriptions.on_event_changes IS
    'Receives "event published / event changed" messages. Meant for operator subscriptions (org_id NULL) only.';

UPDATE sales_notification_subscriptions SET on_event_changes = true WHERE org_id IS NULL;

CREATE TABLE event_watch_snapshots (
    event_id         uuid        PRIMARY KEY,
    org_id           uuid        NOT NULL,
    announced        jsonb,
    announced_digest text,
    observed_digest  text        NOT NULL,
    observed_at      timestamptz NOT NULL,
    announced_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE event_watch_snapshots IS
    'What each published event looked like when the operator was last told (announced, NULL until the first announcement) and the digest seen on the latest watcher run.';

CREATE TABLE event_watch_state (
    id        boolean     PRIMARY KEY DEFAULT true CHECK (id),
    seeded_at timestamptz NOT NULL
);

COMMENT ON TABLE event_watch_state IS
    'One row, written once: the events that existed at that moment were recorded without announcing them.';

-- +goose Down

DROP TABLE IF EXISTS event_watch_state;
DROP TABLE IF EXISTS event_watch_snapshots;
ALTER TABLE sales_notification_subscriptions DROP COLUMN IF EXISTS on_event_changes;
