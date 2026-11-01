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
--
-- session_sale_subscriptions: "notify me when sales open" (package
-- salesubscribe). A buyer leaves an address on a "coming soon" session, ticks
-- the consent (its version, time and a salted hash of the address the request
-- came from are kept), and confirms it from a letter (double opt-in). When the
-- session opens, every confirmed address that was not told yet gets one
-- letter linking back to the page it subscribed on. Rows are kept: the
-- consent also covers invitations to the organizer's later events (owner
-- decision 2026-10-31); a new subscription to the same session asks again.

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

CREATE TABLE session_sale_subscriptions (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    session_id         uuid        NOT NULL REFERENCES sessions(id)       ON DELETE CASCADE,
    event_id           uuid        NOT NULL REFERENCES events(id)         ON DELETE CASCADE,
    org_id             uuid        NOT NULL REFERENCES organizations(id)  ON DELETE CASCADE,
    channel_id         uuid        NULL     REFERENCES sales_channels(id) ON DELETE SET NULL,
    email              text        NOT NULL,
    email_normalized   text        NOT NULL,
    locale             text        NOT NULL DEFAULT '',
    page_url           text        NOT NULL DEFAULT '',
    source             text        NOT NULL CHECK (source IN ('widget', 'site')),
    consent_version    text        NOT NULL,
    consent_at         timestamptz NOT NULL,
    ip_hash            text        NULL,
    status             text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed')),
    confirm_token_hash text        NULL,
    confirm_sent_at    timestamptz NULL,
    confirmed_at       timestamptz NULL,
    notified_at        timestamptz NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, email_normalized)
);

CREATE INDEX session_sale_subscriptions_waiting_idx
    ON session_sale_subscriptions (session_id) WHERE status = 'confirmed' AND notified_at IS NULL;
CREATE UNIQUE INDEX session_sale_subscriptions_token_idx
    ON session_sale_subscriptions (confirm_token_hash) WHERE confirm_token_hash IS NOT NULL;
CREATE INDEX session_sale_subscriptions_ip_idx
    ON session_sale_subscriptions (ip_hash, created_at) WHERE ip_hash IS NOT NULL;

COMMENT ON TABLE session_sale_subscriptions IS
    'Notify me when sales open: double opt-in addresses per session, with the consent version, time and a salted IP hash.';

CREATE TABLE sales_alert_watch_state (
    id         smallint    PRIMARY KEY CHECK (id = 1),
    seeded_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS sales_alert_watch_state;
DROP TABLE IF EXISTS session_sale_subscriptions;
DROP TABLE IF EXISTS session_soldout_notices;
DROP TABLE IF EXISTS session_sales_state_changes;
DROP TABLE IF EXISTS session_sales_states;
