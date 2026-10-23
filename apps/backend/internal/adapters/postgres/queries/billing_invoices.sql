-- billing_invoices.sql — platform invoices to organizers, their gapless numbering,
-- payment fees, the platform Stripe webhook journal and incoming bank payments
-- (billing tasks B3-3, B4-1, B4-2, migration 0160). Money is bigint minor units
-- next to its currency, never a float.
-- The Go side is the hand-maintained gen/billing_invoices.sql.go, which holds the
-- authoritative copy of every statement; this file lists them for review and grep.

-- name: InsertBillingInvoice :one
-- Writes a draft invoice; a repeated idempotency key writes nothing.
INSERT INTO invoices (org_id, account_id, issuer_id, kind, state, billing_period, idempotency_key,
       payment_method, period_from, period_to, pack_id, related_invoice_id, currency, tax_status,
       created_by, subtotal_minor, card_fee_minor, tax_minor, total_amount_minor)
VALUES ($1, $2, $3, $4, 'draft', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 0, 0, 0, 0)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: GetBillingInvoice :one
-- Returns one invoice.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE id = $1;

-- name: LockBillingInvoice :one
-- Returns one invoice under a row lock.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE id = $1 FOR UPDATE;

-- name: GetBillingInvoiceByIdempotencyKey :one
-- Returns the invoice a key made.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE idempotency_key = $1;

-- name: GetBillingInvoiceByPaymentReference :one
-- Returns the invoice of a payment reference.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE payment_reference = $1;

-- name: GetBillingInvoiceByCheckoutSession :one
-- Returns the invoice a Stripe Checkout Session pays.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE stripe_checkout_session_id = $1;

-- name: GetBillingInvoiceByStripeInvoice :one
-- Returns the invoice a Stripe invoice collects.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM invoices WHERE stripe_invoice_id = $1;

-- name: ListBillingInvoicesByOrg :many
-- Returns an organization's billing-account invoices, newest first; empty state is every state.
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM   invoices
WHERE  org_id = $1 AND account_id IS NOT NULL AND ($2::text = '' OR state = $2)
ORDER  BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4;

-- name: ListOpenBillingInvoices :many
-- Returns billing-account invoices in one state, oldest due first (the operator's queue).
SELECT id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at
FROM   invoices
WHERE  account_id IS NOT NULL AND state = $1
ORDER  BY COALESCE(due_at, created_at), id
LIMIT  $2;

-- name: CountBillingInvoicesForPeriod :one
-- Counts an account's invoices of a kind for a period, void ones included.
SELECT count(*) FROM invoices
WHERE  account_id = $1 AND kind = $2 AND billing_period = $3;

-- name: SetBillingInvoiceTotals :one
-- Stores the money of a draft invoice; total is subtotal + card fee + tax.
UPDATE invoices
SET    subtotal_minor = $2, card_fee_minor = $3, tax_minor = $4,
       total_amount_minor = $2::bigint + $3::bigint + $4::bigint, updated_at = now()
WHERE  id = $1 AND state = 'draft'
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: IssueBillingInvoice :one
-- Moves a draft to issued with its number.
UPDATE invoices
SET    state = 'issued', number = $2, number_year = $3, number_seq = $4, payment_method = $5,
       payment_reference = $6, due_at = $7, issued_at = $8, updated_at = now()
WHERE  id = $1 AND state = 'draft'
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: MarkBillingInvoicePaid :one
-- Moves an issued invoice to paid.
UPDATE invoices
SET    state = 'paid', paid_at = $2, paid_amount_minor = $3, paid_via = $5, paid_reference = $6,
       stripe_payment_intent_id = COALESCE($7, stripe_payment_intent_id), updated_at = now()
WHERE  id = $1 AND state = 'issued' AND currency = $4
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: VoidBillingInvoice :one
-- Voids a draft or issued invoice (never a paid one).
UPDATE invoices
SET    state = 'void', voided_at = $2, void_reason = $3, updated_at = now()
WHERE  id = $1 AND state IN ('draft', 'issued')
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: SetBillingInvoiceCheckout :one
-- Stores the Stripe Checkout Session of an invoice and its payment page.
UPDATE invoices
SET    stripe_checkout_session_id = $2, hosted_payment_url = $3, updated_at = now()
WHERE  id = $1
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: SetBillingInvoiceStripeInvoice :one
-- Stores the Stripe invoice that collects an invoice and its hosted page.
UPDATE invoices
SET    stripe_invoice_id = $2, hosted_payment_url = COALESCE($3, hosted_payment_url), updated_at = now()
WHERE  id = $1
RETURNING id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at;

-- name: InsertBillingInvoiceLine :one
-- Writes one line of an invoice.
INSERT INTO invoice_lines (invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
       total_amount_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
          total_amount_minor, currency, created_at;

-- name: ListBillingInvoiceLines :many
-- Returns the lines of an invoice in order.
SELECT id, invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
       total_amount_minor, currency, created_at
FROM   invoice_lines WHERE invoice_id = $1 ORDER BY created_at, id;

-- name: DeleteBillingInvoiceCardFeeLines :exec
-- Removes the card fee line of a draft before it is recomputed.
DELETE FROM invoice_lines WHERE invoice_id = $1 AND kind = 'card_fee';

-- name: ListUninvoicedCharges :many
-- Returns the direct charges of an account whose event is in [from, to), not reversed,
-- with an uncovered amount left to bill.
SELECT c.id, c.rule_type, c.currency, c.amount_minor,
       (c.amount_minor - COALESCE((SELECT SUM(a.amount_minor) FROM charge_allocations a WHERE a.charge_id = c.id), 0))::bigint
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
WHERE  c.account_id = $1
  AND  c.reversal_of IS NULL
  AND  c.amount_minor > 0
  AND  be.occurred_at >= $2 AND be.occurred_at < $3
  AND  NOT EXISTS (SELECT 1 FROM charges rv WHERE rv.reversal_of = c.id)
  AND  c.amount_minor > COALESCE((SELECT SUM(a.amount_minor) FROM charge_allocations a WHERE a.charge_id = c.id), 0)
ORDER  BY c.rule_type, be.occurred_at, c.id;

-- name: DeleteInvoiceLineAllocations :execrows
-- Releases the charges a voided invoice had taken, so the next invoice bills them again.
DELETE FROM charge_allocations
WHERE  kind = 'invoice_line'
  AND  invoice_line_id IN (SELECT id FROM invoice_lines WHERE invoice_id = $1);

-- name: NextInvoiceNumber :one
-- Takes the next number of an issuer's series for a year; runs in the issuing transaction.
INSERT INTO invoice_number_series (issuer_id, year, last_seq)
VALUES ($1, $2, 1)
ON CONFLICT (issuer_id, year) DO UPDATE SET last_seq = invoice_number_series.last_seq + 1, updated_at = now()
RETURNING last_seq;

-- name: GetBillingIssuer :one
-- Returns an issuer.
SELECT id, code, name, country, tax_id, invoice_series, timezone FROM billing_issuers WHERE id = $1;

-- name: GetPriceListPackByID :one
-- Returns one pack with its list's currency.
SELECT p.id, p.price_list_id, pl.currency, p.tier, p.face_minor, p.unseated_rate_minor,
       p.seated_surcharge_minor, p.active, p.sort_order
FROM   price_list_packs p JOIN price_lists pl ON pl.id = p.price_list_id
WHERE  p.id = $1;

-- name: GetBillingPaymentFeeAt :one
-- Returns the fee of a method in force at a given time; no row when none.
SELECT id, method, currency, pct_bp, fixed_minor, effective_from, created_by, reason, created_at
FROM   billing_payment_fees
WHERE  method = $1 AND currency = $2 AND effective_from <= $3
ORDER  BY effective_from DESC
LIMIT  1;

-- name: InsertBillingPaymentFee :one
-- Writes a fee version.
INSERT INTO billing_payment_fees (method, currency, pct_bp, fixed_minor, effective_from, created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, method, currency, pct_bp, fixed_minor, effective_from, created_by, reason, created_at;

-- name: ListBillingPaymentFees :many
-- Returns every fee version.
SELECT id, method, currency, pct_bp, fixed_minor, effective_from, created_by, reason, created_at
FROM billing_payment_fees ORDER BY method, currency, effective_from DESC;

-- name: InsertBillingStripeEvent :one
-- Records an applied Stripe event; no row back when it was already applied.
INSERT INTO billing_stripe_events (event_id, event_type, invoice_id, outcome)
VALUES ($1, $2, $3, $4)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_id;

-- name: GetStripeCustomerIDByAccount :one
-- Returns the Stripe customer of a billing account; no row when none.
SELECT stripe_customer_id FROM stripe_customers WHERE account_id = $1;

-- name: InsertStripeCustomerForAccount :exec
-- Stores the Stripe customer of an account.
INSERT INTO stripe_customers (org_id, account_id, stripe_customer_id)
VALUES ($1, $2, $3)
ON CONFLICT (stripe_customer_id) DO NOTHING;

-- name: InsertBankTransaction :one
-- Stores an incoming payment; a repeat (same provider and external id) writes nothing.
INSERT INTO billing_bank_transactions (provider, external_id, booked_at, amount_minor, currency, reference,
       counterparty_name)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider, external_id) DO NOTHING
RETURNING id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at;

-- name: LockBankTransaction :one
-- Returns one incoming payment under a row lock.
SELECT id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at
FROM billing_bank_transactions WHERE id = $1 FOR UPDATE;

-- name: MatchBankTransaction :one
-- Ties an incoming payment to the invoice it paid.
UPDATE billing_bank_transactions
SET    status = 'matched', invoice_id = $2, matched_at = $3, matched_by = $4, unmatched_reason = NULL
WHERE  id = $1 AND status <> 'matched'
RETURNING id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at;

-- name: SetBankTransactionStatus :one
-- Marks an incoming payment as unmatched (with the reason) or ignored.
UPDATE billing_bank_transactions
SET    status = $2, unmatched_reason = $3
WHERE  id = $1 AND status <> 'matched'
RETURNING id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at;

-- name: ListBankTransactions :many
-- Returns incoming payments, newest first; empty status is all.
SELECT id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at
FROM   billing_bank_transactions
WHERE  ($1::text = '' OR status = $1)
ORDER  BY booked_at DESC, id DESC
LIMIT  $2;

-- name: InvoiceHasBillingAccount :one
-- Says whether an invoice belongs to the billing accounts (migration 0160) rather than the legacy tariff ledger.
SELECT account_id IS NOT NULL FROM invoices WHERE id = $1;

-- name: UpdateBillingAccountsPayer :execrows
-- Stores the payer details on every billing account of an organization.
UPDATE billing_accounts SET payer = $2, updated_at = now() WHERE org_id = $1;
