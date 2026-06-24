-- billing_ledger.sql — sqlc queries for service billing (feature #161).
--
-- Covers: tariffs (versioned), usage_records (monthly counters),
-- invoices (state machine), and invoice_lines.

-- name: InsertTariff :one
-- Creates a new tariff version. Pass orgID = nil for the global default tariff.
INSERT INTO tariffs (
    org_id, plan_name, effective_from,
    per_ticket_fee_minor, per_event_fee_minor, monthly_fee_minor,
    currency, notes, created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, org_id, plan_name, effective_from,
          per_ticket_fee_minor, per_event_fee_minor, monthly_fee_minor,
          currency, notes, created_at, created_by;

-- name: GetActiveTariff :one
-- Returns the most recent tariff for orgID whose effective_from <= asOfDate.
-- Falls back to the global default (org_id IS NULL) when no org-specific tariff exists.
SELECT id, org_id, plan_name, effective_from,
       per_ticket_fee_minor, per_event_fee_minor, monthly_fee_minor,
       currency, notes, created_at, created_by
FROM   tariffs
WHERE  (org_id = $1 OR org_id IS NULL)
  AND  effective_from <= $2
ORDER BY (org_id IS NULL) ASC, effective_from DESC
LIMIT  1;

-- name: GetTariffByID :one
SELECT id, org_id, plan_name, effective_from,
       per_ticket_fee_minor, per_event_fee_minor, monthly_fee_minor,
       currency, notes, created_at, created_by
FROM   tariffs
WHERE  id = $1;

-- name: ListTariffs :many
-- Lists all tariff versions. Pass orgID = nil to list global tariffs only.
SELECT id, org_id, plan_name, effective_from,
       per_ticket_fee_minor, per_event_fee_minor, monthly_fee_minor,
       currency, notes, created_at, created_by
FROM   tariffs
WHERE  org_id IS NOT DISTINCT FROM $1
ORDER BY effective_from DESC;

-- name: IncrementUsageRecord :one
-- Inserts a new usage record for (orgID, billingPeriod) or increments
-- the appropriate counter if a record already exists.
-- Pass deltaTickets, deltaComplimentary, deltaEvents as the increments.
INSERT INTO usage_records (org_id, billing_period, tickets_sold, complimentary_issued, events_published)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id, billing_period) DO UPDATE
    SET tickets_sold         = usage_records.tickets_sold         + EXCLUDED.tickets_sold,
        complimentary_issued = usage_records.complimentary_issued + EXCLUDED.complimentary_issued,
        events_published     = usage_records.events_published     + EXCLUDED.events_published,
        updated_at           = now()
RETURNING id, org_id, billing_period, tickets_sold, complimentary_issued, events_published, created_at, updated_at;

-- name: GetUsageRecord :one
SELECT id, org_id, billing_period, tickets_sold, complimentary_issued, events_published, created_at, updated_at
FROM   usage_records
WHERE  org_id        = $1
  AND  billing_period = $2;

-- name: ListUsageRecordsByPeriod :many
-- Returns all orgs that have usage in the given billing period. Used by
-- the month-end batch worker to determine which orgs need an invoice.
SELECT id, org_id, billing_period, tickets_sold, complimentary_issued, events_published, created_at, updated_at
FROM   usage_records
WHERE  billing_period = $1
ORDER BY org_id;

-- name: InsertInvoice :one
INSERT INTO invoices (org_id, billing_period, total_amount_minor, currency)
VALUES ($1, $2, $3, $4)
RETURNING id, org_id, billing_period, state, total_amount_minor, currency,
          issued_at, paid_at, voided_at, created_at, updated_at;

-- name: GetInvoiceByID :one
SELECT id, org_id, billing_period, state, total_amount_minor, currency,
       issued_at, paid_at, voided_at, created_at, updated_at
FROM   invoices
WHERE  id = $1;

-- name: GetInvoiceByOrgAndPeriod :one
SELECT id, org_id, billing_period, state, total_amount_minor, currency,
       issued_at, paid_at, voided_at, created_at, updated_at
FROM   invoices
WHERE  org_id = $1 AND billing_period = $2;

-- name: ListInvoicesByOrg :many
SELECT id, org_id, billing_period, state, total_amount_minor, currency,
       issued_at, paid_at, voided_at, created_at, updated_at
FROM   invoices
WHERE  org_id = $1
ORDER BY billing_period DESC, created_at DESC;

-- name: UpdateInvoiceState :one
-- Advances an invoice to a new state. Timestamps are set automatically based
-- on the new state. Returns pgx.ErrNoRows when the invoice does not exist.
UPDATE invoices
SET    state      = $2,
       updated_at = now(),
       issued_at  = CASE WHEN $2 = 'issued' THEN now() ELSE issued_at END,
       paid_at    = CASE WHEN $2 = 'paid'   THEN now() ELSE paid_at   END,
       voided_at  = CASE WHEN $2 = 'void'   THEN now() ELSE voided_at END
WHERE  id = $1
RETURNING id, org_id, billing_period, state, total_amount_minor, currency,
          issued_at, paid_at, voided_at, created_at, updated_at;

-- name: UpdateInvoiceTotal :one
UPDATE invoices
SET    total_amount_minor = $2,
       updated_at         = now()
WHERE  id = $1
RETURNING id, org_id, billing_period, state, total_amount_minor, currency,
          issued_at, paid_at, voided_at, created_at, updated_at;

-- name: InsertInvoiceLine :one
INSERT INTO invoice_lines (
    invoice_id, tariff_id, description, quantity,
    unit_amount_minor, total_amount_minor, currency
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, invoice_id, tariff_id, description, quantity,
          unit_amount_minor, total_amount_minor, currency, created_at;

-- name: ListInvoiceLines :many
SELECT id, invoice_id, tariff_id, description, quantity,
       unit_amount_minor, total_amount_minor, currency, created_at
FROM   invoice_lines
WHERE  invoice_id = $1
ORDER BY created_at ASC, id ASC;
