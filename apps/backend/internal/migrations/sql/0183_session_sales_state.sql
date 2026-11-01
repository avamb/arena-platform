-- 0183_session_sales_state.sql — one sales state per session for EVERY sales
-- channel (owner decision 2026-10-31): "coming soon" or "sold out" set by the
-- organizer, or nothing (sales follow the categories as before).
--
-- session_sales_states holds the state a session carries NOW. A row exists
-- only while the state is set; "open" deletes it, so the real places and
-- sale windows come back exactly as they were (the state never rewrites
-- availability or sales_end_at). The hold gate (hcheckout.CheckSessionSalesOpen)
-- refuses every NEW hold while a row exists; holds taken before keep going
-- to payment, and tickets, refunds and scanning are untouched.
--
-- session_sales_state_changes is the journal: every real change, who made it
-- (actor type, id, name and membership role at that moment), through which
-- client (event center web, Telegram bot, site, API) and the outbox row
-- (v1.session.updated) that tells the WordPress sites. The row's delivery is
-- what the event center shows as "the site is updated / not yet".
--
-- The alerts (package salesalert, job sales_state.watch):
--   session_soldout_notices holds a session while the organizer has been told
--   it is sold out (by hand, or the last place sold with no unpaid hold left);
--   the row is deleted when places come back, so the next sell-out is told
--   again. site_alerted_at marks a journal row whose site delivery was still
--   missing after 15 minutes and was told to the owner once.
--   sales_alert_watch_state remembers the first pass, which records the
--   sessions already sold out at deploy without telling anybody.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE session_sales_states (
    session_id   uuid        PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    state        text        NOT NULL CHECK (state IN ('soon', 'sold_out')),
    changed_at   timestamptz NOT NULL DEFAULT now(),
    change_id    uuid        NULL
);

COMMENT ON TABLE session_sales_states IS
    'The organizer''s sales state of a session (soon / sold_out), obeyed by every sales channel; no row = sales open.';

CREATE TABLE session_sales_state_changes (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    session_id      uuid        NOT NULL REFERENCES sessions(id)      ON DELETE CASCADE,
    event_id        uuid        NOT NULL REFERENCES events(id)        ON DELETE CASCADE,
    org_id          uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    old_state       text        NOT NULL CHECK (old_state IN ('open', 'soon', 'sold_out')),
    new_state       text        NOT NULL CHECK (new_state IN ('open', 'soon', 'sold_out')),
    source          text        NOT NULL,
    actor_type      text        NOT NULL DEFAULT 'system',
    actor_id        text        NOT NULL DEFAULT '',
    actor_name      text        NOT NULL DEFAULT '',
    actor_role      text        NOT NULL DEFAULT '',
    outbox_event_id uuid        NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (old_state <> new_state)
);

CREATE INDEX session_sales_state_changes_session_idx
    ON session_sales_state_changes (session_id, created_at DESC);

COMMENT ON TABLE session_sales_state_changes IS
    'Journal of session sales state changes: who, role, when, from which client, and the outbox row that tells the sites.';

ALTER TABLE session_sales_state_changes ADD COLUMN site_alerted_at timestamptz NULL;

CREATE TABLE session_soldout_notices (
    session_id   uuid        PRIMARY KEY REFERENCES sessions(id)      ON DELETE CASCADE,
    org_id       uuid        NOT NULL    REFERENCES organizations(id) ON DELETE CASCADE,
    manual       boolean     NOT NULL,
    notified_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE session_soldout_notices IS
    'Sessions the organizer has been told are sold out; deleted when places come back so the next sell-out is told again.';

CREATE TABLE sales_alert_watch_state (
    id         smallint    PRIMARY KEY CHECK (id = 1),
    seeded_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS sales_alert_watch_state;
DROP TABLE IF EXISTS session_soldout_notices;
DROP TABLE IF EXISTS session_sales_state_changes;
DROP TABLE IF EXISTS session_sales_states;
