-- 0145_refund_scan_exceptions.sql — a platform superadmin may let ONE refund
-- of a ticket that was already scanned at the gate go through (a show
-- cancelled half-way, a scan made by mistake; owner decision 2026-10-13).
--
-- One row per ticket (UNIQUE ticket_id): one that expired unused is granted
-- again on the same row, a spent one is final. It is valid until expires_at
-- (at most 24 hours after it was granted) and is spent by the first refund
-- that uses it: used_at and used_by_refund record that refund,
-- so the refund keeps passing the scan check at its claim while no other
-- refund of the ticket can.
--
-- No permission is seeded (the route is for the platform superadmin only), so
-- the superadmin parity guard has nothing to check.

-- +goose Up
CREATE TABLE refund_scan_exceptions (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ticket_id      uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    granted_by     text        NOT NULL,
    reason         text        NOT NULL,
    note           text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz,
    used_by_refund uuid        REFERENCES refunds(id) ON DELETE SET NULL,
    CONSTRAINT refund_scan_exceptions_ticket_uq UNIQUE (ticket_id),
    CONSTRAINT refund_scan_exceptions_expiry_check
        CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours')
);

CREATE INDEX refund_scan_exceptions_org_idx ON refund_scan_exceptions (org_id, created_at DESC);

COMMENT ON TABLE refund_scan_exceptions IS
    'A platform superadmin''s one-time permission to refund a ticket that was already scanned: valid until expires_at (24 h at most), spent by the first refund that uses it (used_by_refund).';

-- +goose Down
DROP TABLE IF EXISTS refund_scan_exceptions;
