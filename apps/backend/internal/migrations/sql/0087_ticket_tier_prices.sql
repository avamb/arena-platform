-- +goose Up
-- AB-48: scheduled (dynamic) pricing — price windows per tier.
--
-- The organizer sets prices per category FOR DATE RANGES: early-bird
-- until a date, then the standard price, switching automatically with no
-- operator action. Orthogonal to ticket_tiers.sale_window_start/end,
-- which governs WHEN a tier is sellable, not what it costs.
--
-- Resolution contract (ONE resolver — internal/domain/pricing — used by
-- the pricing quote, checkout, widget and public feed alike):
--   effective price at t = the window containing t,
--   falling back to ticket_tiers.price_amount when none matches.
--
-- GAP POLICY — decided once (AB-48 step 12): a schedule that leaves a
-- period uncovered FALLS BACK TO THE BASE PRICE (ticket_tiers.
-- price_amount). Windows are never required to tile the timeline, and a
-- silently-zero price is impossible: the base price is NOT NULL and the
-- window CHECK forbids negative amounts.
--
-- Overlap safety is DATABASE-level: a GiST exclusion constraint over
-- tstzrange (btree_gist), because overlap resolved in application code
-- will eventually produce two prices for one moment.

CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE ticket_tier_prices (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    tier_id      uuid        NOT NULL REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    valid_from   timestamptz NOT NULL,
    -- NULL = open-ended (applies until superseded or forever).
    valid_to     timestamptz NULL,
    price_amount bigint      NOT NULL CHECK (price_amount >= 0),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ticket_tier_prices_window_order
        CHECK (valid_to IS NULL OR valid_to > valid_from),
    -- Two windows of one tier can never cover the same moment. '[)'
    -- semantics: valid_to is exclusive, so back-to-back windows
    -- (a.valid_to = b.valid_from) are legal — the boundary instant
    -- belongs to the later window.
    CONSTRAINT ticket_tier_prices_no_overlap
        EXCLUDE USING gist (
            tier_id WITH =,
            tstzrange(valid_from, COALESCE(valid_to, 'infinity'::timestamptz), '[)') WITH &&
        )
);

CREATE INDEX ticket_tier_prices_by_tier_from
    ON ticket_tier_prices (tier_id, valid_from);

COMMENT ON TABLE ticket_tier_prices IS
    'AB-48 scheduled pricing: non-overlapping price windows per tier '
    '(GiST exclusion). Effective price at t = containing window, else '
    'ticket_tiers.price_amount (the documented gap policy).';
COMMENT ON COLUMN ticket_tier_prices.valid_to IS
    'Exclusive end of the window; NULL = open-ended.';

-- AB-48 step 9: the quoted price is locked at reservation creation.
-- reservation_ga_items (0063) becomes the lock record for EVERY
-- reservation shape — seated and single-tier GA reservations now write
-- per-tier lines at creation too, and checkout reads the stored
-- unit_price instead of re-resolving. A cart held across a price-window
-- boundary keeps the price it was quoted; issued tickets are never
-- repriced.
COMMENT ON TABLE reservation_ga_items IS
    'Per-tier price lines of a reservation, written in the hold/create '
    'transaction. Since AB-48 this is the PRICE LOCK for all reservation '
    'shapes (GA multi-tier, GA single-tier, seated): checkout charges '
    'the stored unit_price, never a re-resolved one. Also read by the '
    'anonymous order-status endpoint (WID-0b) and the hold-expiry '
    'recovery endpoint (WID-0c).';

-- +goose Down

DROP TABLE IF EXISTS ticket_tier_prices;

COMMENT ON TABLE reservation_ga_items IS
    'General-admission line items of a reservation: one row per (tier, quantity) '
    'held by a GA or mixed (seats + GA) cart. Written in the same transaction as '
    'the hold; read by the anonymous order-status endpoint (WID-0b) and re-captured '
    'by the hold-expiry recovery endpoint (WID-0c).';
