-- +goose Up
-- AB-49: ticket cancellation — the operator action that did not exist.
--
-- Cancellation is the operator-facing primitive and the SOLE driver of
-- inventory and gate state. The money refund is a separate, optional,
-- possibly-later consequence recorded here but never gating anything:
--   * refund_mode 'automatic' — platform calls the provider; refund_id
--     links the refunds row (0028).
--   * refund_mode 'manual'    — organizer refunds from the provider's own
--     dashboard; an OUTSTANDING OBLIGATION, not a completed refund.
--   * refund_mode 'none'      — nothing owed (comp ticket, no-refund policy).
--
-- refund_date / refund_price mirror the Bil24 export shape the external
-- scanning service (MACS, AB-50) already consumes: both live on the TICKET.
-- There is deliberately NO 'refunded' seat status — the seat returns to
-- 'available' immediately (owner decision 2026-08-01); "refunded" views in
-- reporting derive by joining tickets.
--
-- review_hold flags an order-level partial INBOUND refund (initiated
-- directly in Stripe) that a human must attribute to specific tickets.
-- The hold FLAGS, never blocks admission, and is never sent to MACS as
-- status 3 (owner decision 2026-08-01).

ALTER TABLE tickets
    ADD COLUMN cancelled_at        timestamptz NULL,
    ADD COLUMN cancellation_reason text        NULL,
    ADD COLUMN refund_mode         text        NULL,
    ADD COLUMN refund_id           uuid        NULL REFERENCES refunds(id),
    ADD COLUMN refund_date         timestamptz NULL,
    ADD COLUMN refund_price        bigint      NULL,
    ADD COLUMN review_hold         boolean     NOT NULL DEFAULT false,
    ADD COLUMN review_hold_reason  text        NULL;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_refund_mode_check
        CHECK (refund_mode IS NULL OR refund_mode IN ('none', 'manual', 'automatic')),
    ADD CONSTRAINT tickets_refund_price_check
        CHECK (refund_price IS NULL OR refund_price >= 0),
    -- A refund decision only exists on a ticket that left 'active'.
    ADD CONSTRAINT tickets_refund_mode_requires_cancel
        CHECK (refund_mode IS NULL OR cancelled_at IS NOT NULL);

COMMENT ON COLUMN tickets.cancelled_at IS
    'AB-49: when the ticket was cancelled (operator action, inbound refund '
    'webhook, or complimentary revocation). NULL while active.';
COMMENT ON COLUMN tickets.cancellation_reason IS
    'AB-49: operator-supplied reason recorded at cancellation.';
COMMENT ON COLUMN tickets.refund_mode IS
    'AB-49: money decision taken at cancellation: none | manual | automatic. '
    'manual is an OUTSTANDING obligation handled outside the platform — it '
    'must never read as done. Never gates inventory or admission.';
COMMENT ON COLUMN tickets.refund_id IS
    'AB-49: refunds row created for refund_mode=automatic. NULL otherwise.';
COMMENT ON COLUMN tickets.refund_date IS
    'AB-49: when the money moved (or was recorded as owed). Bil24 export '
    'shape: ticketList[].refundDate.';
COMMENT ON COLUMN tickets.refund_price IS
    'AB-49: refunded amount in minor units. Bil24 export shape: '
    'ticketList[].refundPrice.';
COMMENT ON COLUMN tickets.review_hold IS
    'AB-49: order received a PARTIAL inbound provider refund that cannot be '
    'attributed to specific tickets automatically. Flags for human review — '
    'admission stays allowed, MACS is never told status 3 for this.';

-- Admin surfacing of outstanding review holds.
CREATE INDEX tickets_review_hold_idx
    ON tickets (session_id)
    WHERE review_hold;

-- ReleaseSoldSessionSeat guard: "no other ACTIVE ticket references this
-- seat" — probe by (session_id, seat_key) over active rows only.
CREATE INDEX tickets_active_seat_idx
    ON tickets (session_id, seat_key)
    WHERE status = 'active' AND seat_key IS NOT NULL;

-- 0020 promised "refunds handled via separate domain events" for
-- capacity_sold decrements; that consumer is AB-49's cancellation flow
-- (RestoreSoldCapacity on every cancellation, GA and seated alike).
COMMENT ON COLUMN inventory_ledger.capacity_sold IS
    'Units converted from held to sold (purchase confirmed). Decremented '
    'by ticket cancellation / revocation via RestoreSoldCapacity (AB-49).';

-- +goose Down

DROP INDEX IF EXISTS tickets_active_seat_idx;
DROP INDEX IF EXISTS tickets_review_hold_idx;

ALTER TABLE tickets
    DROP CONSTRAINT IF EXISTS tickets_refund_mode_requires_cancel,
    DROP CONSTRAINT IF EXISTS tickets_refund_price_check,
    DROP CONSTRAINT IF EXISTS tickets_refund_mode_check;

ALTER TABLE tickets
    DROP COLUMN IF EXISTS review_hold_reason,
    DROP COLUMN IF EXISTS review_hold,
    DROP COLUMN IF EXISTS refund_price,
    DROP COLUMN IF EXISTS refund_date,
    DROP COLUMN IF EXISTS refund_id,
    DROP COLUMN IF EXISTS refund_mode,
    DROP COLUMN IF EXISTS cancellation_reason,
    DROP COLUMN IF EXISTS cancelled_at;

COMMENT ON COLUMN inventory_ledger.capacity_sold IS
    'Units converted from held to sold (purchase confirmed). Never '
    'decremented (refunds handled via separate domain events).';
