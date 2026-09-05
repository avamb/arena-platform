-- +goose Up
-- W1-A6a (feature #486): Order aggregate — orders/order_items/order_events,
-- Bil24-compat wave 1, spec 08_architecture/18_bil24_compat_wave1_specification_ru.md
-- section 3.3.
--
-- Today "the order" is implicit: a checkout_sessions row plus the tickets it
-- issued. That is not enough to answer GET_ORDER_INFO / GET_TICKETS_BY_ORDER
-- on the wire, to search buyers by name/phone/email, or to enforce "one open
-- order per customer+session". This migration introduces an explicit orders
-- aggregate:
--
--   orders       — one row per purchase attempt, 1:1 with the checkout
--                   session and reservation that produced it. total is
--                   generated-column-checked as subtotal - discount + charge
--                   so the money invariant lives in the schema, not just
--                   application code.
--   order_items  — one row PER UNIT (ticket or GA seat unit), not per
--                   category: this is what GET_CART.seatList and
--                   CREATE_ORDER_EXT.ticketList need on the wire (spec §7.5/
--                   §7.7). ticket_id is null until IssueTicketsForCheckout
--                   runs, then gets backfilled.
--   order_events — append-only audit trail (created/paid/cancelled/etc.)
--                   keyed by a free-form actor string
--                   ('gateway:<channel display_number>' | 'user:<uuid>' |
--                   'system') per spec §14.1.
--
-- pg_trgm powers fuzzy buyer search (buyer_name/buyer_email/buyer_phone) for
-- the future org-scoped Orders admin page; it is a trusted extension already
-- used the same way btree_gist was added in 0087.
--
-- Backfill of existing checkout_sessions.state='completed' rows into orders
-- is deliberately NOT done here (spec §3.3 decision #4): those are stand
-- fixture data, not production history worth reconstructing.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE orders (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    system_id           bigint      NOT NULL UNIQUE DEFAULT nextval('compatibility_system_id_seq'),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    channel_id          uuid        NOT NULL REFERENCES sales_channels(id),
    event_id            uuid        NOT NULL REFERENCES events(id),
    session_id          uuid        NOT NULL REFERENCES sessions(id),
    customer_id         uuid        REFERENCES customers(id),
    checkout_session_id uuid        NOT NULL UNIQUE REFERENCES checkout_sessions(id),
    reservation_id      uuid        NOT NULL REFERENCES reservations(id),
    external_ref        text,
    source              text        NOT NULL CHECK (source IN
                          ('bil24_gateway','public_feed','checkout_api','complimentary')),
    status              text        NOT NULL CHECK (status IN
                          ('pending_payment','paid','cancelled','expired','abandoned',
                           'refunded','partially_refunded','manual_review')),
    currency            char(3)     NOT NULL,
    subtotal            bigint      NOT NULL DEFAULT 0,
    discount            bigint      NOT NULL DEFAULT 0,
    charge              bigint      NOT NULL DEFAULT 0,
    total               bigint      NOT NULL DEFAULT 0,
    charge_percent_bp   int         NOT NULL DEFAULT 0,
    promo_code_id       uuid        REFERENCES promo_codes(id),
    buyer_name          text,
    buyer_email         text,
    buyer_phone         text,
    payment_method      text,
    paid_at             timestamptz,
    cancelled_at        timestamptz,
    expires_at          timestamptz,
    metadata            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (total = subtotal - discount + charge)
);

COMMENT ON TABLE orders IS
    'W1-A6a: explicit order aggregate (spec §3.3). One row per purchase '
    'attempt, 1:1 with the checkout_sessions/reservations rows that produced '
    'it. system_id draws from compatibility_system_id_seq so it can be '
    'surfaced as a Bil24-wire orderId. external_ref carries the WooCommerce '
    'order number written by CREATE_ORDER_EXT.orderId.';

COMMENT ON COLUMN orders.charge_percent_bp IS
    'Channel fee_percent snapshotted in basis points at order time (125 = '
    '1.25%). Kept alongside the exact charge amount so a rounded display '
    'value never needs to be recomputed from float fee_percent later.';

CREATE INDEX orders_org_created_idx     ON orders (org_id, created_at DESC);
CREATE INDEX orders_customer_idx        ON orders (customer_id);
CREATE INDEX orders_session_status_idx  ON orders (session_id, status);
CREATE UNIQUE INDEX orders_channel_external_ref_uq
    ON orders (channel_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX orders_buyer_email_trgm ON orders USING gin (buyer_email gin_trgm_ops);
CREATE INDEX orders_buyer_name_trgm  ON orders USING gin (buyer_name  gin_trgm_ops);
CREATE INDEX orders_buyer_phone_trgm ON orders USING gin (buyer_phone gin_trgm_ops);

-- "One open (pending_payment) order per customer+session" (spec §3.3 /
-- §7.7 CREATE_ORDER_EXT step 4). A partial unique index only over the
-- pending_payment status enforces this without blocking repeat completed
-- purchases by the same customer for the same session.
CREATE UNIQUE INDEX orders_one_pending_per_customer_session_uq
    ON orders (customer_id, session_id) WHERE status = 'pending_payment';

CREATE TABLE order_items (
    id              uuid    PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid    NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ordinal         int     NOT NULL,
    kind            text    NOT NULL DEFAULT 'ticket' CHECK (kind IN ('ticket')),
    tier_id         uuid    NOT NULL REFERENCES ticket_tiers(id),
    session_seat_id uuid    REFERENCES session_seats(id),
    ticket_id       uuid    REFERENCES tickets(id),
    unit_price      bigint  NOT NULL,
    discount        bigint  NOT NULL DEFAULT 0,
    charge          bigint  NOT NULL DEFAULT 0,
    total           bigint  NOT NULL,
    UNIQUE (order_id, ordinal)
);

COMMENT ON TABLE order_items IS
    'W1-A6a: one row per unit (ticket or GA unit), never per category — this '
    'is what GET_CART.seatList / ticketList need to answer per-seat prices '
    '(spec §3.3). session_seat_id is null for pure GA units minted without a '
    'seat row; ticket_id is backfilled once IssueTicketsForCheckout runs.';

CREATE TABLE order_events (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    order_id   uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    type       text        NOT NULL,
    actor      text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN order_events.type IS
    'Free-form event kind, spec §3.3: created|lines_reconciled|paid|'
    'amount_mismatch|hold_expired|hold_reacquired|cancelled|ticket_refunded|'
    'note.';

COMMENT ON COLUMN order_events.actor IS
    'Free-form actor string: ''gateway:<channel display_number>'' | '
    '''user:<uuid>'' | ''system''.';

CREATE INDEX order_events_order_idx ON order_events (order_id, created_at);

ALTER TABLE tickets ADD COLUMN order_id uuid REFERENCES orders(id);
CREATE INDEX tickets_order_idx ON tickets (order_id);

-- permissions.name (not "code") is the canonical column — see 0008_rbac.sql;
-- the spec uses "code" prose but the schema settled on "name" long ago.
INSERT INTO permissions (name, description) VALUES
    ('order.read',  'Read orders of the organization'),
    ('order.write', 'Cancel / annotate orders of the organization')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM permissions WHERE name IN ('order.read','order.write');

DROP INDEX IF EXISTS tickets_order_idx;
ALTER TABLE tickets DROP COLUMN IF EXISTS order_id;

DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS order_items;

DROP INDEX IF EXISTS orders_one_pending_per_customer_session_uq;
DROP INDEX IF EXISTS orders_buyer_phone_trgm;
DROP INDEX IF EXISTS orders_buyer_name_trgm;
DROP INDEX IF EXISTS orders_buyer_email_trgm;
DROP INDEX IF EXISTS orders_channel_external_ref_uq;
DROP INDEX IF EXISTS orders_session_status_idx;
DROP INDEX IF EXISTS orders_customer_idx;
DROP INDEX IF EXISTS orders_org_created_idx;
DROP TABLE IF EXISTS orders;
