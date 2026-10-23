-- billing_credits.sql — credit lots and their journal (billing wave B-B,
-- task B3-1, migration 0159).
--
-- The Go side is the hand-maintained gen/billing_credits.sql.go, which holds
-- the authoritative copy of every statement; this file lists them for review
-- and for grep. Money is bigint minor units of the account currency.
--
-- credit_ledger is APPEND-ONLY: this file has no UPDATE or DELETE of it, and
-- the static guard (tests/staticanalysis/billing_append_only_test.go) fails if
-- one appears. credit_lots.remaining_minor is updated in place, always with a
-- journal entry and under LockBillingAccount.

-- name: LockBillingAccount :one
SELECT id FROM billing_accounts WHERE id = $1 FOR UPDATE;

-- name: InsertCreditLot :one
INSERT INTO credit_lots (account_id, org_id, currency, kind, source, source_ref, source_event_id,
       tier, unseated_rate_minor, seated_surcharge_minor, price_list_id, pack_id,
       purchased_at, face_minor, remaining_minor, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, $15)
ON CONFLICT (account_id, source, source_ref) WHERE source_ref IS NOT NULL DO NOTHING
RETURNING *;

-- name: GetCreditLotBySource :one
SELECT * FROM credit_lots WHERE account_id = $1 AND source = $2 AND source_ref = $3;

-- name: GetCreditLot :one
SELECT * FROM credit_lots WHERE id = $1;

-- name: ListAvailableCreditLots :many
SELECT * FROM credit_lots
WHERE  account_id = $1 AND kind = $2 AND remaining_minor > 0
  AND  purchased_at <= $3 AND (expires_at IS NULL OR expires_at > $3)
ORDER  BY expires_at ASC NULLS LAST, purchased_at, id;

-- name: SetCreditLotRemaining :exec
UPDATE credit_lots SET remaining_minor = $2, updated_at = now() WHERE id = $1;

-- name: GrowCreditLot :one
UPDATE credit_lots
SET    face_minor = face_minor + $2, remaining_minor = remaining_minor + $2, updated_at = now()
WHERE  id = $1
RETURNING *;

-- name: ListCreditLotsByAccount :many
SELECT * FROM credit_lots WHERE account_id = $1 ORDER BY purchased_at DESC, id DESC;

-- name: ListExpiringCreditLots :many
SELECT * FROM credit_lots
WHERE  expires_at IS NOT NULL AND expires_at <= $1 AND remaining_minor > 0
ORDER  BY expires_at, id
LIMIT  $2;

-- name: AppendCreditLedger :one
INSERT INTO credit_ledger (account_id, lot_id, entry_type, amount_minor, currency, remaining_after,
       charge_id, allocation_id, invoice_id, cause_event_id, actor_type, actor_id, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE(NULLIF($11, ''), 'system'), $12, $13)
RETURNING *;

-- name: ListCreditLedgerByAccount :many
SELECT * FROM credit_ledger
WHERE  account_id = $1 AND ($2::bigint IS NULL OR seq < $2)
ORDER  BY seq DESC
LIMIT  $3;

-- name: ListCreditLotMismatches :many
-- Lots whose remaining_minor is not the signed sum of their journal, or not
-- the remaining_after of their last entry (41 section 6.10).
WITH j AS (
    SELECT l.lot_id, SUM(CASE l.entry_type
           WHEN 'purchase' THEN l.amount_minor WHEN 'bonus_grant' THEN l.amount_minor
           WHEN 'reversal' THEN l.amount_minor WHEN 'release' THEN l.amount_minor
           WHEN 'adjustment_credit' THEN l.amount_minor
           WHEN 'consume' THEN -l.amount_minor WHEN 'expire' THEN -l.amount_minor
           WHEN 'hold' THEN -l.amount_minor WHEN 'adjustment_debit' THEN -l.amount_minor
           ELSE 0 END)::bigint AS journal_minor
    FROM   credit_ledger l GROUP BY l.lot_id
), last AS (
    SELECT DISTINCT ON (l.lot_id) l.lot_id, l.remaining_after
    FROM   credit_ledger l ORDER BY l.lot_id, l.seq DESC
)
SELECT c.id, c.account_id, c.org_id, c.currency, c.remaining_minor,
       COALESCE(j.journal_minor, 0), COALESCE(last.remaining_after, 0)
FROM   credit_lots c
LEFT JOIN j ON j.lot_id = c.id
LEFT JOIN last ON last.lot_id = c.id
WHERE  ($1::uuid[] IS NULL OR c.org_id = ANY($1))
  AND  (c.remaining_minor <> COALESCE(j.journal_minor, 0)
        OR c.remaining_minor <> COALESCE(last.remaining_after, 0))
ORDER  BY c.id
LIMIT  $2;

-- name: ListConsumeAllocationMismatches :many
-- Lots whose consume entries differ from the charge allocations naming them.
WITH consumed AS (
    SELECT lot_id,
           SUM(amount_minor) FILTER (WHERE entry_type = 'consume')::bigint  AS consumed_minor,
           SUM(amount_minor) FILTER (WHERE entry_type = 'reversal')::bigint AS returned_minor
    FROM   credit_ledger WHERE charge_id IS NOT NULL GROUP BY lot_id
), allocated AS (
    SELECT lot_id, SUM(amount_minor)::bigint AS allocated_minor
    FROM   charge_allocations WHERE lot_id IS NOT NULL GROUP BY lot_id
)
SELECT c.id, c.org_id, c.currency,
       COALESCE(consumed.consumed_minor, 0), COALESCE(allocated.allocated_minor, 0),
       COALESCE(consumed.returned_minor, 0)
FROM   credit_lots c
LEFT JOIN consumed ON consumed.lot_id = c.id
LEFT JOIN allocated ON allocated.lot_id = c.id
WHERE  ($1::uuid[] IS NULL OR c.org_id = ANY($1))
  AND  COALESCE(consumed.consumed_minor, 0) <> COALESCE(allocated.allocated_minor, 0)
ORDER  BY c.id
LIMIT  $2;

-- name: GetCreditBalance :one
SELECT a.currency,
       COALESCE(SUM(c.remaining_minor) FILTER (WHERE c.kind = 'purchased'), 0)::bigint,
       COALESCE(SUM(c.remaining_minor) FILTER (WHERE c.kind = 'bonus'), 0)::bigint,
       MIN(c.expires_at) FILTER (WHERE c.kind = 'bonus')
FROM   billing_accounts a
LEFT JOIN credit_lots c
       ON c.account_id = a.id AND c.remaining_minor > 0
      AND (c.expires_at IS NULL OR c.expires_at > $2)
WHERE  a.id = $1
GROUP  BY a.currency;

-- name: ListCreditAllocationsByCharge :many
SELECT * FROM charge_allocations WHERE charge_id = $1 ORDER BY created_at, id FOR UPDATE;
