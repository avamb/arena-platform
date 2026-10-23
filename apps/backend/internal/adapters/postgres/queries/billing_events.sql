-- billing_events.sql — queries over the billing event store (billing wave B-A,
-- task B2-1, migration 0158): billable_events, charges, charge_reversals,
-- charge_allocations and the collect/rate job cursors.
--
-- The Go side is the hand-maintained gen/billing_events.sql.go. Money is
-- bigint minor units with the currency next to it.
--
-- billable_events, charges and charge_reversals are APPEND-ONLY: this file has
-- no UPDATE or DELETE of them, and the static guard
-- (tests/staticanalysis/billing_append_only_test.go) fails if one appears.
-- Only billing_job_cursors is updated in place.

-- name: InsertBillableEvent :one
-- Idempotent by (kind, subject_id): a repeat writes nothing and returns no row.
INSERT INTO billable_events (
    kind, subject_id, occurred_at, org_id, account_id,
    event_id, session_id, order_id, ticket_id, seated, pricing_class,
    sale_currency, unit_price_minor, discount_minor, service_charge_minor,
    venue_country, attributes, source)
VALUES ($1, $2, $3, $4, $5,
        $6, $7, $8, $9, $10, $11,
        $12, $13, $14, $15,
        $16, COALESCE($17::jsonb, '{}'::jsonb), $18)
ON CONFLICT (kind, subject_id) DO NOTHING
RETURNING id;

-- name: GetBillableEvent :one
SELECT id, kind, subject_id, occurred_at, org_id, account_id,
       event_id, session_id, order_id, ticket_id, seated, pricing_class,
       sale_currency, unit_price_minor, discount_minor, service_charge_minor,
       venue_country, attributes, source, collected_at
FROM   billable_events
WHERE  kind = $1 AND subject_id = $2;

-- name: ListUnratedBillableEvents :many
-- Events without a DIRECT charge, oldest first. An event that costs nothing
-- is rated with a zero-amount direct charge, so "unrated" is exactly "no
-- direct charge".
SELECT e.id, e.kind, e.subject_id, e.occurred_at, e.org_id, e.account_id,
       e.event_id, e.session_id, e.order_id, e.ticket_id, e.seated, e.pricing_class,
       e.sale_currency, e.unit_price_minor, e.discount_minor, e.service_charge_minor,
       e.venue_country, e.attributes, e.source, e.collected_at
FROM   billable_events e
WHERE  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)
ORDER BY e.occurred_at, e.id
LIMIT  $1;

-- name: ListUnratedBillableEventsSince :many
-- Same as ListUnratedBillableEvents but only events that occurred at or after
-- $1, so a rate job that keeps a watermark does not walk the whole rated past.
SELECT e.id, e.kind, e.subject_id, e.occurred_at, e.org_id, e.account_id,
       e.event_id, e.session_id, e.order_id, e.ticket_id, e.seated, e.pricing_class,
       e.sale_currency, e.unit_price_minor, e.discount_minor, e.service_charge_minor,
       e.venue_country, e.attributes, e.source, e.collected_at
FROM   billable_events e
WHERE  e.occurred_at >= $1
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)
ORDER BY e.occurred_at, e.id
LIMIT  $2;

-- name: ListBillableEventsForSession :many
SELECT id, kind, subject_id, occurred_at, org_id, account_id,
       event_id, session_id, order_id, ticket_id, seated, pricing_class,
       sale_currency, unit_price_minor, discount_minor, service_charge_minor,
       venue_country, attributes, source, collected_at
FROM   billable_events
WHERE  session_id = $1 AND kind = $2
ORDER BY occurred_at, id;

-- name: CountUnratedBillableEventsForSession :one
-- How many events of one kind of a session still have no direct charge: a
-- session_cancelled event waits while this is not zero.
SELECT count(*)
FROM   billable_events e
WHERE  e.session_id = $1 AND e.kind = $2
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL);

-- name: InsertCharge :one
-- A second direct charge for the same event, or a second reversal of the same
-- charge, fails with 23505 (charges_one_direct_per_event_uq,
-- charges_one_reversal_per_charge_uq).
INSERT INTO charges (
    billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
    price_list_id, price_list_version, override_id, inputs, reason, reversal_of)
VALUES ($1, $2, $3, $4, $5, $6,
        $7, $8, $9, COALESCE($10::jsonb, '{}'::jsonb), $11, $12)
RETURNING id, billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
          price_list_id, price_list_version, override_id, inputs, reason, reversal_of, rated_at;

-- name: GetChargeByEvent :one
-- The DIRECT charge of an event, pgx.ErrNoRows when it is not rated yet.
SELECT id, billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
       price_list_id, price_list_version, override_id, inputs, reason, reversal_of, rated_at
FROM   charges
WHERE  billable_event_id = $1 AND reversal_of IS NULL;

-- name: ListReversibleChargesForSession :many
-- Direct charges of the ticket_sold events of a session that have no reversal
-- yet: what a session_cancelled event reverses.
SELECT c.id, c.billable_event_id, c.account_id, c.currency, c.amount_minor, c.rule_type, c.rule_id,
       c.price_list_id, c.price_list_version, c.override_id, c.inputs, c.reason, c.reversal_of, c.rated_at
FROM   charges c
JOIN   billable_events e ON e.id = c.billable_event_id
WHERE  e.session_id = $1 AND e.kind = 'ticket_sold'
  AND  c.reversal_of IS NULL
  AND  NOT EXISTS (SELECT 1 FROM charges r WHERE r.reversal_of = c.id)
ORDER BY e.occurred_at, c.id;

-- name: InsertChargeReversal :one
INSERT INTO charge_reversals (charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id, created_at;

-- name: GetChargeReversalByCharge :one
SELECT id, charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id, created_at
FROM   charge_reversals
WHERE  charge_id = $1;

-- name: ListChargesByAccountPeriod :many
-- Charges and reversals of an account whose EVENT occurred in [$2, $3): the
-- period is the period of the sale, a reversal counts in the period of the
-- charge it reverses.
SELECT c.id, c.billable_event_id, c.account_id, c.currency, c.amount_minor, c.rule_type, c.rule_id,
       c.price_list_id, c.price_list_version, c.override_id, c.inputs, c.reason, c.reversal_of, c.rated_at
FROM   billable_events e
JOIN   charges c ON c.billable_event_id = e.id
WHERE  e.account_id = $1 AND e.occurred_at >= $2 AND e.occurred_at < $3
ORDER BY e.occurred_at, c.rated_at, c.id;

-- name: SumChargesByAccountPeriod :many
-- Net amount (charges plus the negative reversals) and the number of charge
-- rows per currency, for the same period as ListChargesByAccountPeriod.
SELECT c.currency, COALESCE(sum(c.amount_minor), 0)::bigint AS amount_minor, count(*) AS charge_count
FROM   billable_events e
JOIN   charges c ON c.billable_event_id = e.id
WHERE  e.account_id = $1 AND e.occurred_at >= $2 AND e.occurred_at < $3
GROUP BY c.currency
ORDER BY c.currency;

-- name: InsertChargeAllocation :one
INSERT INTO charge_allocations (charge_id, kind, lot_id, invoice_line_id, amount_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, charge_id, kind, lot_id, invoice_line_id, amount_minor, currency, created_at;

-- name: ListChargeAllocations :many
SELECT id, charge_id, kind, lot_id, invoice_line_id, amount_minor, currency, created_at
FROM   charge_allocations
WHERE  charge_id = $1
ORDER BY created_at, id;

-- name: GetBillingJobCursor :one
SELECT key, cursor_at, updated_at
FROM   billing_job_cursors
WHERE  key = $1;

-- name: UpsertBillingJobCursor :exec
-- billing_job_cursors is the one table of this store that is updated in place.
INSERT INTO billing_job_cursors (key, cursor_at, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET cursor_at = EXCLUDED.cursor_at, updated_at = now();
