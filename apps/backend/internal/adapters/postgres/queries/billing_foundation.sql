-- billing_foundation.sql — queries over the billing foundation tables
-- (billing wave B-A, task B1-1, migration 0157): issuers, country routes,
-- price lists with their rules and packs, billing accounts and their dated
-- terms, individual organization rates.
--
-- The Go side is the hand-maintained gen/billing_foundation.sql.go. Money is
-- bigint minor units. price_lists.bonus_share_cap (numeric) is read as
-- integer basis points so no float reaches Go.

-- name: GetBillingIssuerByCode :one
SELECT id, code, name, country, tax_id, address, invoice_series, timezone, vat_texts, created_at
FROM   billing_issuers
WHERE  code = $1;

-- name: ListBillingCountryRoutes :many
SELECT country, currency, issuer_id
FROM   billing_country_routes
ORDER BY country;

-- name: GetBillingCountryRoute :one
SELECT country, currency, issuer_id
FROM   billing_country_routes
WHERE  country = $1;

-- name: GetPriceListVersionAt :one
-- The version of a price list in force at $3: the latest effective_from <= $3
-- that was not revoked.
SELECT id, code, version, currency, effective_from, revoked_at, locked,
       free_tickets_annual_limit, floor_per_ticket_minor,
       (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
       bonus_covers_surcharge, created_by, reason, created_at
FROM   price_lists
WHERE  code = $1 AND currency = $2 AND effective_from <= $3 AND revoked_at IS NULL
ORDER BY effective_from DESC, version DESC
LIMIT  1;

-- name: ListPriceListRules :many
SELECT id, price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order, created_at
FROM   price_list_rules
WHERE  price_list_id = $1
ORDER BY line, sort_order, id;

-- name: ListPriceListPacks :many
SELECT p.id, p.price_list_id, pl.currency, p.tier, p.face_minor, p.unseated_rate_minor, p.seated_surcharge_minor, p.active, p.sort_order
FROM   price_list_packs p
JOIN   price_lists pl ON pl.id = p.price_list_id
WHERE  p.price_list_id = $1
ORDER BY p.sort_order, p.tier;

-- name: GetBillingAccount :one
SELECT id, org_id, currency, issuer_id, account_kind, payer, tax_status,
       tax_id_verified_at, auto_topup, created_at, updated_at
FROM   billing_accounts
WHERE  org_id = $1 AND currency = $2 AND account_kind = $3;

-- name: InsertBillingAccountIfMissing :exec
-- Followed by GetBillingAccount in a NEW statement, so a row committed by a
-- concurrent caller is seen.
INSERT INTO billing_accounts (org_id, currency, issuer_id, account_kind)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, currency, account_kind) DO NOTHING;

-- name: ListBillingAccountsByOrg :many
SELECT id, org_id, currency, issuer_id, account_kind, payer, tax_status,
       tax_id_verified_at, auto_topup, created_at, updated_at
FROM   billing_accounts
WHERE  org_id = $1
ORDER BY account_kind, currency;

-- name: InsertBillingAccountTerms :one
INSERT INTO billing_account_terms (
    account_id, effective_from, price_list_code, plan_kind, billing_cycle,
    shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
    created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, account_id, effective_from, price_list_code, plan_kind, billing_cycle,
          shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
          created_by, reason, created_at;

-- name: InsertBillingAccountTermsIfNone :exec
-- The first terms of an account, written only when it has none at all.
INSERT INTO billing_account_terms (
    account_id, effective_from, price_list_code, plan_kind, billing_cycle,
    shortfall_policy, issuer_id, created_by, reason)
SELECT $1::uuid, $2::timestamptz, $3::text, 'standard', 'monthly', 'invoice_postpaid', $4::uuid, $5::text, $6::text
WHERE  NOT EXISTS (SELECT 1 FROM billing_account_terms WHERE account_id = $1::uuid)
ON CONFLICT (account_id, effective_from) DO NOTHING;

-- name: GetBillingAccountTermsAt :one
-- The terms in force at $2: the last row with effective_from <= $2.
SELECT id, account_id, effective_from, price_list_code, plan_kind, billing_cycle,
       shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
       created_by, reason, created_at
FROM   billing_account_terms
WHERE  account_id = $1 AND effective_from <= $2
ORDER BY effective_from DESC
LIMIT  1;

-- name: GetOrgRateOverrideAt :one
-- The individual rate in force at $3 for one currency.
SELECT id, org_id, currency, unseated_minor, seated_minor, effective_from,
       effective_to, note, created_by, created_at
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2 AND effective_from <= $3
  AND  (effective_to IS NULL OR effective_to > $3)
ORDER BY effective_from DESC
LIMIT  1;
