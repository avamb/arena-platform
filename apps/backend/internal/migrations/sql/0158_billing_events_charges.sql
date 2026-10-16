-- +goose Up
SET LOCAL lock_timeout = '5s';

-- =====================================================================
-- arena_new — billable events, charges and the collection indexes
-- (billing wave B-A, task B2-1).
--
-- Spec: 08_architecture/41_billing_credits_architecture_ru.md (sections 6.1,
-- 6.2, 6.3, 6.10) and 42_billing_execution_spec_ru.md (cards B2-1..B2-3).
--
-- What this migration adds, and what it does NOT do:
--
--   * billable_events   — append-only FACT: a ticket sold / refunded /
--                         cancelled, a session cancelled, an event published,
--                         an invitation issued, a generator run. Idempotent by
--                         (kind, subject_id): the collect job (B2-2) may scan
--                         the same sale a hundred times and write it once.
--   * charges           — append-only RESULT of rating one event: amount, the
--                         rule, the price-list version and the inputs, so a
--                         bill is reproducible. A reversal is a PAIRED row
--                         with a negative amount (reversal_of), never an
--                         UPDATE.
--   * charge_reversals  — the journal of reversals: cause event, reason,
--                         actor (41 section 6.3).
--   * charge_allocations— what closed a charge: a credit lot, an invoice
--                         line, an agreement discount (41 sections 6.4-6.5).
--                         lot_id has no foreign key yet: credit_lots arrive
--                         in block B3.
--   * billing_job_cursors — high-water marks of the collect / rate jobs.
--   * indexes on the sales tables the collect queries scan by time window,
--     so a one-day pass never reads order_items or orders end to end.
--
-- Nothing here collects, rates or charges. The jobs are B2-2 and B2-3.
--
-- Money is bigint MINOR units with the currency next to it, never a float.
--
-- Append-only: billable_events, charges and charge_reversals are never
-- UPDATEd or DELETEd (static guard tests/staticanalysis/billing_*): a
-- correction is a compensating row. billing_job_cursors is the one table here
-- that is updated in place.
--
-- The org_id / account_id foreign keys carry NO cascade on purpose, so billing
-- history cannot vanish with a hard delete. event_id, session_id, order_id and
-- ticket_id are plain uuids WITHOUT foreign keys: the fact survives the row it
-- was read from (a deleted order item must not erase a charge). A test
-- fixture that hard-deletes an organization or a billing account must delete
-- its billing_account_terms, charge_allocations, charges, charge_reversals
-- and billable_events rows first (a test file may DELETE those tables).
-- =====================================================================

-- ── billable_events ─────────────────────────────────────────────────────
CREATE TABLE billable_events (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    kind                text        NOT NULL
                        CONSTRAINT billable_events_kind_check CHECK (kind IN (
                            'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
                            'session_cancelled', 'event_published',
                            'ticket_complimentary', 'generator_run')),
    -- The id of the thing the event is about: the order item, the refund, the
    -- ticket, the session change, the event, the job. With kind it is the
    -- idempotency key.
    subject_id          uuid        NOT NULL,
    occurred_at         timestamptz NOT NULL,
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    account_id          uuid        NOT NULL REFERENCES billing_accounts(id),

    -- Attributes lifted out of the jsonb for indexes and filters. All NULL
    -- when they do not apply to the kind.
    event_id            uuid        NULL,
    session_id          uuid        NULL,
    order_id            uuid        NULL,
    ticket_id           uuid        NULL,
    seated              boolean     NULL,
    pricing_class       text        NULL
                        CONSTRAINT billable_events_pricing_class_check CHECK (pricing_class IS NULL OR pricing_class IN ('standard', 'low_price')),
    sale_currency       char(3)     NULL
                        CONSTRAINT billable_events_sale_currency_check CHECK (sale_currency IS NULL OR sale_currency ~ '^[A-Z]{3}$'),
    unit_price_minor    bigint      NULL,
    discount_minor      bigint      NULL,
    service_charge_minor bigint     NULL,
    venue_country       char(2)     NULL
                        CONSTRAINT billable_events_venue_country_check CHECK (venue_country IS NULL OR venue_country ~ '^[A-Z]{2}$'),

    -- The rest: order source, the link to the original ticket_sold of a
    -- refund / cancellation, low_price_unresolved, the cancellation reason.
    attributes          jsonb       NOT NULL DEFAULT '{}'::jsonb,

    source              text        NOT NULL DEFAULT 'collect'
                        CONSTRAINT billable_events_source_check CHECK (source IN ('collect', 'outbox', 'backfill')),
    collected_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT billable_events_kind_subject_uq UNIQUE (kind, subject_id)
);

CREATE INDEX billable_events_account_occurred_idx ON billable_events (account_id, occurred_at);
CREATE INDEX billable_events_event_idx            ON billable_events (event_id)   WHERE event_id   IS NOT NULL;
CREATE INDEX billable_events_session_idx          ON billable_events (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX billable_events_kind_occurred_idx    ON billable_events (kind, occurred_at);
CREATE INDEX billable_events_ticket_idx           ON billable_events (ticket_id)  WHERE ticket_id  IS NOT NULL;
-- The rate job reads events in occurred_at order.
CREATE INDEX billable_events_occurred_idx         ON billable_events (occurred_at, id);

COMMENT ON TABLE billable_events IS
    'Append-only billing facts, idempotent by (kind, subject_id). Never UPDATE or DELETE: a correction is a new row (41 sections 6.1-6.2).';

-- ── charges ─────────────────────────────────────────────────────────────
CREATE TABLE charges (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    -- The event the charge belongs to. A reversal points at the ORIGINAL
    -- event (the ticket_sold) so the sums per event net to zero; the event
    -- that caused the reversal is in charge_reversals.cause_event_id.
    billable_event_id   uuid        NOT NULL REFERENCES billable_events(id),
    account_id          uuid        NOT NULL REFERENCES billing_accounts(id),
    currency            char(3)     NOT NULL
                        CONSTRAINT charges_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    -- Minor units of the account currency. A direct charge is >= 0 (0 for a
    -- journal-only event), a reversal <= 0.
    amount_minor        bigint      NOT NULL,
    rule_type           text        NOT NULL,
    rule_id             uuid        NULL,
    price_list_id       uuid        NULL REFERENCES price_lists(id),
    price_list_version  int         NULL,
    override_id         uuid        NULL REFERENCES org_rate_overrides(id),
    inputs              jsonb       NOT NULL DEFAULT '{}'::jsonb,
    reason              text        NULL,
    reversal_of         uuid        NULL REFERENCES charges(id),
    rated_at            timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT charges_direct_amount_check   CHECK (reversal_of IS NOT NULL OR amount_minor >= 0),
    CONSTRAINT charges_reversal_amount_check CHECK (reversal_of IS NULL OR amount_minor <= 0)
);

-- One direct charge per event: the rate job is idempotent. An event that
-- costs nothing (a refund, a cancellation, a free ticket) still gets a
-- zero-amount direct charge with a reason, so "rated" is always "has a
-- direct charge" and the append-only event row needs no marker column.
CREATE UNIQUE INDEX charges_one_direct_per_event_uq ON charges (billable_event_id) WHERE reversal_of IS NULL;
-- One reversal per charge.
CREATE UNIQUE INDEX charges_one_reversal_per_charge_uq ON charges (reversal_of) WHERE reversal_of IS NOT NULL;
CREATE INDEX charges_account_rated_idx ON charges (account_id, rated_at);
CREATE INDEX charges_event_idx ON charges (billable_event_id);

COMMENT ON TABLE charges IS
    'Append-only result of rating one billable event. A reversal is a paired row with a negative amount and reversal_of, never an UPDATE (41 section 6.3).';

-- ── charge_reversals ────────────────────────────────────────────────────
CREATE TABLE charge_reversals (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    charge_id           uuid        NOT NULL UNIQUE REFERENCES charges(id),
    reversal_charge_id  uuid        NOT NULL UNIQUE REFERENCES charges(id),
    cause_event_id      uuid        NOT NULL REFERENCES billable_events(id),
    reason              text        NOT NULL,
    actor_type          text        NOT NULL DEFAULT 'system',
    actor_id            text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT charge_reversals_distinct_check CHECK (charge_id <> reversal_charge_id)
);

CREATE INDEX charge_reversals_cause_idx ON charge_reversals (cause_event_id);

COMMENT ON TABLE charge_reversals IS
    'Journal of charge reversals: cause (a session_cancelled event or an operator correction), reason, actor. Append-only (41 section 6.3).';

-- ── charge_allocations ──────────────────────────────────────────────────
CREATE TABLE charge_allocations (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    charge_id           uuid        NOT NULL REFERENCES charges(id),
    kind                text        NOT NULL
                        CONSTRAINT charge_allocations_kind_check CHECK (kind IN (
                            'purchased_lot', 'bonus_lot', 'invoice_line', 'agreement_discount')),
    -- credit_lots arrive in block B3, which adds the foreign key.
    lot_id              uuid        NULL,
    invoice_line_id     uuid        NULL REFERENCES invoice_lines(id),
    amount_minor        bigint      NOT NULL
                        CONSTRAINT charge_allocations_amount_check CHECK (amount_minor > 0),
    currency            char(3)     NOT NULL
                        CONSTRAINT charge_allocations_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT charge_allocations_target_check CHECK (
        (kind IN ('purchased_lot', 'bonus_lot') AND lot_id IS NOT NULL)
        OR (kind = 'invoice_line' AND invoice_line_id IS NOT NULL)
        OR kind = 'agreement_discount')
);

CREATE INDEX charge_allocations_charge_idx ON charge_allocations (charge_id);

COMMENT ON TABLE charge_allocations IS
    'What closed a charge: a credit lot, an invoice line or an agreement discount (41 sections 6.4-6.5).';

-- ── billing_job_cursors ─────────────────────────────────────────────────
CREATE TABLE billing_job_cursors (
    key         text        PRIMARY KEY,
    cursor_at   timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE billing_job_cursors IS
    'High-water marks of the billing collect and rate jobs. Updated in place (the only non-append-only table of the billing event store).';

-- ── indexes for the collect queries (41 section 6.2) ────────────────────
-- Each scans one day window of a sales table. Without them the planner reads
-- orders and order_items end to end for every pass (EXPLAIN in the PR).
CREATE INDEX orders_paid_at_idx ON orders (paid_at) WHERE paid_at IS NOT NULL;
CREATE INDEX refunds_settled_at_idx ON refunds (settled_at) WHERE settled_at IS NOT NULL;
CREATE INDEX tickets_cancelled_at_idx ON tickets (cancelled_at) WHERE cancelled_at IS NOT NULL;
CREATE INDEX tickets_complimentary_issued_at_idx ON tickets (issued_at) WHERE complimentary_issuance_id IS NOT NULL;
CREATE INDEX event_publications_published_at_idx ON event_publications (published_at);
CREATE INDEX session_changes_cancelled_created_idx ON session_changes (created_at) WHERE 'cancelled' = ANY (kinds);

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS session_changes_cancelled_created_idx;
DROP INDEX IF EXISTS event_publications_published_at_idx;
DROP INDEX IF EXISTS tickets_complimentary_issued_at_idx;
DROP INDEX IF EXISTS tickets_cancelled_at_idx;
DROP INDEX IF EXISTS refunds_settled_at_idx;
DROP INDEX IF EXISTS orders_paid_at_idx;

DROP TABLE IF EXISTS billing_job_cursors;
DROP TABLE IF EXISTS charge_allocations;
DROP TABLE IF EXISTS charge_reversals;
DROP TABLE IF EXISTS charges;
DROP TABLE IF EXISTS billable_events;
