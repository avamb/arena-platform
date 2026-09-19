-- +goose Up
-- =====================================================================
-- arena_new — refunds settled outside arena (functional run 2026-09-19)
--
-- arena is the source of truth for tickets and money, but a WordPress site
-- selling through the gateway takes the buyer's money itself (its own
-- Stripe/AllPay account) and gives it back itself. Until now a REFUND_TICKET
-- from such a site only stamped tickets.refund_price: the refunds registry
-- (the admin «Refunds» page, reports) never learned about it, because every
-- refunds row had to hang off an arena payment_intent that gateway orders do
-- not have.
--
--   refunds.settlement  'provider' — arena drives the refund through its own
--                                    payment provider (the existing
--                                    requested → approved → provider flow);
--                       'external' — the money was already returned by the
--                                    selling site; arena records the fact,
--                                    the row is born `succeeded`.
--   refunds.order_id / refunds.ticket_id — what was refunded. An external
--                       refund always names its ticket; one ticket has at
--                       most one external refund (replays are no-ops).
--   refunds.payment_intent_id — now nullable, required only for 'provider'.
-- =====================================================================

ALTER TABLE refunds
    ALTER COLUMN payment_intent_id DROP NOT NULL,
    ADD COLUMN settlement text NOT NULL DEFAULT 'provider'
        CONSTRAINT refunds_settlement_check CHECK (settlement IN ('provider', 'external')),
    ADD COLUMN order_id  uuid REFERENCES orders(id),
    ADD COLUMN ticket_id uuid REFERENCES tickets(id);

ALTER TABLE refunds
    ADD CONSTRAINT refunds_settlement_shape_check CHECK (
        (settlement = 'provider' AND payment_intent_id IS NOT NULL)
        OR (settlement = 'external' AND ticket_id IS NOT NULL)
    );

CREATE UNIQUE INDEX refunds_external_ticket_uq
    ON refunds (ticket_id) WHERE settlement = 'external';

CREATE INDEX refunds_order_id_idx ON refunds (order_id) WHERE order_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS refunds_order_id_idx;
DROP INDEX IF EXISTS refunds_external_ticket_uq;
ALTER TABLE refunds DROP CONSTRAINT IF EXISTS refunds_settlement_shape_check;
DELETE FROM refunds WHERE settlement = 'external';
ALTER TABLE refunds
    DROP COLUMN IF EXISTS ticket_id,
    DROP COLUMN IF EXISTS order_id,
    DROP COLUMN IF EXISTS settlement,
    ALTER COLUMN payment_intent_id SET NOT NULL;
