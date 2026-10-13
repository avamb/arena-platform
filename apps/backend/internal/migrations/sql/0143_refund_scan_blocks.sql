-- 0143_refund_scan_blocks.sql — refunds of a ticket that was already scanned
-- at the gate are refused. Every refused attempt is journaled here, once per
-- ticket and actor, so the organization's owners and the operator hear about
-- it ONCE per day and not on every press of a button.

-- +goose Up
-- The foreign keys below lock busy tables: fail fast rather than queue behind
-- a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

CREATE TABLE refund_scan_blocks (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id     uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    -- 'user:<uuid>', 'api_key:<uuid>' or 'system' (a refund the engine itself
    -- found blocked when it was about to send it).
    actor_key     text NOT NULL,
    actor_label   text,
    attempts      integer NOT NULL DEFAULT 1,
    first_at      timestamptz NOT NULL DEFAULT now(),
    last_at       timestamptz NOT NULL DEFAULT now(),
    notified_at   timestamptz,
    CONSTRAINT refund_scan_blocks_ticket_actor_uq UNIQUE (ticket_id, actor_key)
);

CREATE INDEX refund_scan_blocks_org_idx ON refund_scan_blocks (org_id, last_at DESC);

COMMENT ON TABLE refund_scan_blocks IS
    'A refund refused because the ticket was already scanned (refund.ticket_scanned): one row per ticket and actor, attempts counted, notified_at throttles the owner e-mail and the sales-bot message to one per day.';

-- +goose Down
DROP TABLE IF EXISTS refund_scan_blocks;
