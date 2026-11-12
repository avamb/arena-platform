-- 0214_outbox_event_legs.sql — per-leg delivery state of the outbox fan-out
-- (finding F-FC-01).
--
-- arena-worker hands every outbox_events row to several legs (sales_notify,
-- conversions, webhook, macs, bil24_wp, billing_accel). The fan-out used to
-- stop at the first failing leg and retry the whole row: while MACS answered
-- 500 the WordPress sites and billing never got the event, and the legs before
-- MACS were called again on every retry.
--
-- Now every leg is called, and this table remembers per event which legs
-- delivered (outbox.LegFanOut, outbox.PGLegStateStore). A delivered leg is
-- never called again for that event; the outbox_events row keeps retrying,
-- and dead-letters, only because of the legs that still fail.
--
--   event_id      the outbox_events row; the state goes with it.
--   leg           the stable leg name from cmd/arena-worker.
--   attempts      how many times the leg was called for this event.
--   last_error    the error of the last failed call, at most 2000 bytes.
--   delivered_at  the leg delivered; it is skipped from then on.
--   updated_at    the last write.
--
-- A row is written only when an event needs a second attempt: a pass in which
-- every leg delivers writes nothing.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE outbox_event_legs (
    event_id     uuid        NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    leg          text        NOT NULL,
    attempts     int         NOT NULL DEFAULT 0,
    last_error   text,
    delivered_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, leg)
);

-- +goose Down
DROP TABLE IF EXISTS outbox_event_legs;
