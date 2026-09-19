-- 0104_ops_watchdog.sql — dedup/cursor state for the ops watchdog worker job.
--
-- First real ticket sales start with no ops staff around; the ops.watchdog
-- worker job (internal/platform/opswatchdog) polls read-only, indexed
-- checks against orders/tickets/payment_intents/checkout_sessions/
-- worker_dead_letter/outbox_events/delivery_jobs/refunds and sends a
-- Telegram message (internal/platform/opsalert) for every sale and for
-- anything that looks wrong. This migration is READ-ONLY with respect to
-- every existing business table — it only adds two new tables owned
-- entirely by the watchdog itself:
--
--   ops_watchdog_state — one row per named check, tracking a (cursor_ts,
--                        cursor_id) pointer so a run only looks at rows
--                        newer than the last one it already saw. A check
--                        that has no prior row must initialise its cursor
--                        to now() on first run — never replay history.
--
--   ops_alerts          — one row per distinct problem, keyed by a
--                        human-assigned "fingerprint" (e.g.
--                        'paid_no_tickets:<order_id>'). The watchdog
--                        notifies on first sight, re-notifies while the
--                        fingerprint keeps recurring (throttled, see the
--                        package's re-notify interval), and writes
--                        resolved_at (plus a "resolved" Telegram message)
--                        once a later run no longer reproduces the
--                        condition.
--
-- No new permissions/role_permissions rows: nothing here is reachable
-- through the HTTP API, so there is nothing to gate with RBAC.

-- +goose Up

CREATE TABLE ops_watchdog_state (
    key         text        PRIMARY KEY,
    cursor_ts   timestamptz,
    cursor_id   text,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE ops_watchdog_state IS
    'Per-check cursor for the ops.watchdog worker job (internal/platform/'
    'opswatchdog). key is the check name (e.g. "sales_feed"). A missing row '
    'means the check has never run; the handler must seed cursor_ts=now() on '
    'first run rather than replay pre-existing history.';

CREATE TABLE ops_alerts (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    fingerprint     text        NOT NULL UNIQUE,
    severity        text        NOT NULL CHECK (severity IN ('info','warn','high','critical')),
    title           text        NOT NULL,
    details         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    last_notified_at timestamptz,
    resolved_at     timestamptz
);

COMMENT ON TABLE ops_alerts IS
    'Dedup/lifecycle state for ops.watchdog alerts. fingerprint identifies a '
    'distinct problem (e.g. "paid_no_tickets:<order_id>"); the same '
    'fingerprint is notified once, re-notified periodically while the '
    'underlying condition keeps recurring, and marked resolved_at (with a '
    '"resolved" message) once a run no longer finds it.';

CREATE INDEX ops_alerts_open_idx ON ops_alerts (severity)
    WHERE resolved_at IS NULL;

-- +goose Down

DROP TABLE IF EXISTS ops_alerts;
DROP TABLE IF EXISTS ops_watchdog_state;
