-- +goose Up
-- =====================================================================
-- arena_new — Reservation GA line items (review defect fix, 2026-07)
--
-- Persists the general-admission portion of a reservation as one row
-- per (tier, quantity) so mixed (seats[] + ga_items[]) and multi-tier
-- GA holds survive past the hold transaction. Before this table the GA
-- breakdown existed only in the inventory ledger deltas:
--
--   * the anonymous order-status endpoint (WID-0b) could not name the
--     GA tiers held under a mixed cart (it showed seats only), and
--   * the hold-expiry recovery endpoint (WID-0c) silently dropped the
--     GA units of a mixed hold because it only re-held the seats.
--
-- Design notes:
--
--   * Child table (mirrors the reservation_seats precedent) rather
--     than a JSONB column on reservations: the rows carry FK
--     integrity to ticket_tiers, are queryable per tier, and are
--     written inside the same hold transaction as the reservation.
--   * unit_price is the per-ticket price snapshot (smallest currency
--     unit) taken when the hold was priced. Status displays use it so
--     the line prices always match the pricing snapshot stored on the
--     checkout session; recovery re-prices through the platform
--     pipeline and writes fresh rows for the replacement reservation.
--   * Composite PK (reservation_id, tier_id): a reservation holds at
--     most one GA line per tier (quantities are aggregated app-side).
--   * ON DELETE CASCADE from reservations: GA lines are meaningless
--     without their parent hold.
-- =====================================================================

CREATE TABLE reservation_ga_items (
    reservation_id uuid        NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    tier_id        uuid        NOT NULL REFERENCES ticket_tiers(id) ON DELETE RESTRICT,
    quantity       integer     NOT NULL CHECK (quantity > 0),
    unit_price     bigint      NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (reservation_id, tier_id)
);

COMMENT ON TABLE reservation_ga_items IS
    'General-admission line items of a reservation: one row per (tier, quantity) '
    'held by a GA or mixed (seats + GA) cart. Written in the same transaction as '
    'the hold; read by the anonymous order-status endpoint (WID-0b) and re-captured '
    'by the hold-expiry recovery endpoint (WID-0c).';

COMMENT ON COLUMN reservation_ga_items.unit_price IS
    'Per-ticket price snapshot in the smallest currency unit, taken when the hold '
    'was priced through the platform pricing pipeline.';

-- Reverse lookup: all GA lines that reference a tier (rebind guardrails,
-- tier deletion RESTRICT support).
CREATE INDEX reservation_ga_items_tier_id ON reservation_ga_items (tier_id);

-- +goose Down
DROP TABLE IF EXISTS reservation_ga_items;
