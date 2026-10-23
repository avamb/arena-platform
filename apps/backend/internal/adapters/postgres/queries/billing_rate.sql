-- billing_rate.sql — reads the rating engine needs on top of
-- billing_foundation.sql (billing wave B-A, task B1-2). The Go side is the
-- hand-maintained gen/billing_rate.sql.go.

-- name: GetBillingAccountByID :one
SELECT id, org_id, currency, issuer_id, account_kind, payer, tax_status,
       tax_id_verified_at, auto_topup, created_at, updated_at
FROM   billing_accounts
WHERE  id = $1;
