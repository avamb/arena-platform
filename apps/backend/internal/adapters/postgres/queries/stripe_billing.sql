-- stripe_billing.sql — queries for Stripe Billing adapter (feature #162).
--
-- Supports the stripe_customers table and the invoices.stripe_invoice_id column
-- added by migration 0037_stripe_billing.sql.

-- name: UpsertStripeCustomer :one
-- Creates or updates the Stripe customer mapping for an org.
-- On conflict (org_id already mapped), updates stripe_customer_id, email, name
-- and refreshes updated_at. Returns the current row.
INSERT INTO stripe_customers (org_id, stripe_customer_id, email, name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id) DO UPDATE
    SET stripe_customer_id = EXCLUDED.stripe_customer_id,
        email              = EXCLUDED.email,
        name               = EXCLUDED.name,
        updated_at         = now()
RETURNING id, org_id, stripe_customer_id, email, name, created_at, updated_at;

-- name: GetStripeCustomerByOrgID :one
-- Returns the Stripe customer mapping for the given org.
-- Returns pgx.ErrNoRows when no mapping exists yet.
SELECT id, org_id, stripe_customer_id, email, name, created_at, updated_at
FROM   stripe_customers
WHERE  org_id = $1;

-- name: UpdateInvoiceStripeID :one
-- Stores the Stripe invoice ID on the local invoice after a successful push.
-- Returns the full invoice row so callers can inspect the current state.
UPDATE invoices
SET    stripe_invoice_id = $2,
       updated_at        = now()
WHERE  id = $1
RETURNING id, org_id, billing_period, state, total_amount_minor, currency,
          issued_at, paid_at, voided_at, created_at, updated_at, stripe_invoice_id;

-- name: GetInvoiceByStripeID :one
-- Looks up a local invoice by its Stripe invoice ID.
-- Used by the webhook handler to find the invoice to mark as paid/failed.
-- Returns pgx.ErrNoRows when no matching invoice is found.
SELECT id, org_id, billing_period, state, total_amount_minor, currency,
       issued_at, paid_at, voided_at, created_at, updated_at, stripe_invoice_id
FROM   invoices
WHERE  stripe_invoice_id = $1;
