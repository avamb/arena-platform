-- 0037_stripe_billing.sql — Stripe Billing adapter for SaaS invoices (feature #162).
--
-- This migration adds the persistence layer required by the Stripe Billing
-- adapter that pushes platform service invoices to the platform's Estonia
-- Stripe account for collection, and syncs payment status back.
--
-- Tables / column additions:
--   stripe_customers          — maps org_id → Stripe customer_id on the platform account
--   invoices.stripe_invoice_id — links a local invoice to its Stripe Invoice object
--
-- Flow:
--   1. POST /v1/billing/stripe/push-invoice/{id}
--      • Ensure a Stripe customer exists for the org (upsert stripe_customers).
--      • Create Stripe invoice items for each invoice line.
--      • Create + finalize Stripe invoice.
--      • Store stripe_invoice_id on the local invoices row.
--   2. POST /v1/billing/stripe/webhook
--      • Verify Stripe-Signature header.
--      • On invoice.paid         → transition local invoice to 'paid'.
--      • On invoice.payment_failed → log; leave local invoice in 'issued'.

-- +goose Up

-- ── stripe_customers ──────────────────────────────────────────────────────────
--
-- Maps each platform org to a Stripe Customer on the platform's Estonia Stripe
-- account. One customer per org (UPSERT on push).

CREATE TABLE stripe_customers (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id              uuid        NOT NULL UNIQUE,
    stripe_customer_id  text        NOT NULL UNIQUE,
    email               text,
    name                text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- ── invoices.stripe_invoice_id ────────────────────────────────────────────────
--
-- Links the local invoice to the Stripe Invoice object after pushing.
-- NULL until the invoice has been pushed to Stripe.
-- UNIQUE ensures the same Stripe invoice can only be linked to one local invoice.

ALTER TABLE invoices ADD COLUMN stripe_invoice_id text UNIQUE;

-- Index for webhook handler: look up local invoice by stripe_invoice_id.
CREATE INDEX invoices_stripe_invoice_id ON invoices (stripe_invoice_id)
    WHERE stripe_invoice_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS invoices_stripe_invoice_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS stripe_invoice_id;
DROP TABLE IF EXISTS stripe_customers;
