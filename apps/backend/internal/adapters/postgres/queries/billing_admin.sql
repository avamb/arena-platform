-- billing_admin.sql — queries of the superadmin screen "Price lists and rates"
-- (billing wave B-A, task B1-5). The Go side is the hand-maintained
-- gen/billing_admin.sql.go. Money is bigint minor units.
--
-- Rules of the screen: a price-list version is only ever INSERTED (a new
-- price is a new version); the single UPDATE of a version is revoked_at, and
-- only before its effective_from. An individual rate is inserted, and the
-- rate it replaces gets effective_to; a rate that has not started can be
-- removed, one that started can only be closed for the future.

-- name: ListPriceListVersions :many
-- Every version with the number of its rules and packs. $1/$2 narrow by code
-- and currency when not empty.
SELECT id, code, version, currency, effective_from, revoked_at, locked,
       free_tickets_annual_limit, floor_per_ticket_minor,
       (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
       bonus_covers_surcharge, created_by, reason, created_at,
       (SELECT count(*) FROM price_list_rules r WHERE r.price_list_id = price_lists.id)::int AS rules_count,
       (SELECT count(*) FROM price_list_packs p WHERE p.price_list_id = price_lists.id)::int AS packs_count
FROM   price_lists
WHERE  ($1::text = '' OR code = $1) AND ($2::text = '' OR currency = $2)
ORDER BY code, currency, effective_from, version;

-- name: GetPriceListByID :one
SELECT id, code, version, currency, effective_from, revoked_at, locked,
       free_tickets_annual_limit, floor_per_ticket_minor,
       (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
       bonus_covers_surcharge, created_by, reason, created_at
FROM   price_lists
WHERE  id = $1;

-- name: LockPriceListSeries :exec
-- Serializes writers of one (code, currency) series for the transaction.
SELECT pg_advisory_xact_lock(hashtextextended('price_list:' || $1::text || ':' || $2::text, 0));

-- name: GetPriceListSeriesState :one
-- The next version number (over ALL versions, revoked ones included) and the
-- latest effective_from among versions that were not revoked (NULL when none).
SELECT (COALESCE(max(version), 0) + 1)::int AS next_version,
       max(effective_from) FILTER (WHERE revoked_at IS NULL) AS latest_effective_from
FROM   price_lists
WHERE  code = $1 AND currency = $2;

-- name: InsertPriceListVersion :one
INSERT INTO price_lists (
    code, version, currency, effective_from, locked, free_tickets_annual_limit,
    floor_per_ticket_minor, bonus_share_cap, bonus_covers_surcharge, created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, ($8::int / 10000.0)::numeric(4,3), $9, $10, $11)
RETURNING id, code, version, currency, effective_from, revoked_at, locked,
          free_tickets_annual_limit, floor_per_ticket_minor,
          (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
          bonus_covers_surcharge, created_by, reason, created_at;

-- name: InsertPriceListRule :one
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order, created_at;

-- name: InsertPriceListPack :one
INSERT INTO price_list_packs (
    price_list_id, tier, face_minor, unseated_rate_minor, seated_surcharge_minor, active, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, price_list_id, tier, face_minor, unseated_rate_minor, seated_surcharge_minor, active, sort_order;

-- name: LockPriceListByID :one
SELECT id, code, version, currency, effective_from, revoked_at, locked,
       free_tickets_annual_limit, floor_per_ticket_minor,
       (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
       bonus_covers_surcharge, created_by, reason, created_at
FROM   price_lists
WHERE  id = $1
FOR UPDATE;

-- name: RevokePriceListVersion :one
-- The only UPDATE of a version. The WHERE repeats the rule "only before
-- effective_from" so that a race with the clock cannot revoke a version in
-- force: zero rows means the caller loses (pgx.ErrNoRows).
UPDATE price_lists
SET    revoked_at = $2::timestamptz
WHERE  id = $1 AND revoked_at IS NULL AND effective_from > $2::timestamptz
RETURNING id, code, version, currency, effective_from, revoked_at, locked,
          free_tickets_annual_limit, floor_per_ticket_minor,
          (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
          bonus_covers_surcharge, created_by, reason, created_at;

-- name: PriceListRevokeLeavesAccountsUncovered :one
-- True when revoking $1 would leave accounts that point at its code with no
-- version in force at the revoked version's date: the version is the
-- earliest one that was not revoked and some terms row names the code on an
-- account in its currency.
SELECT EXISTS (
    SELECT 1 FROM price_lists self
    WHERE  self.id = $1
      AND  NOT EXISTS (SELECT 1 FROM price_lists o
                       WHERE  o.code = self.code AND o.currency = self.currency
                         AND  o.id <> self.id AND o.revoked_at IS NULL
                         AND  o.effective_from <= self.effective_from)
      AND  EXISTS (SELECT 1 FROM billing_account_terms t
                   JOIN   billing_accounts a ON a.id = t.account_id
                   WHERE  t.price_list_code = self.code AND a.currency = self.currency)
);

-- name: ListOrgRateOverridesAdmin :many
-- $1 = organization (zero uuid = all), $2 = currency ('' = all).
SELECT r.id, r.org_id, r.currency, r.unseated_minor, r.seated_minor, r.effective_from,
       r.effective_to, r.note, r.created_by, r.created_at, o.name AS org_name
FROM   org_rate_overrides r
JOIN   organizations o ON o.id = r.org_id
WHERE  ($1::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR r.org_id = $1)
  AND  ($2::text = '' OR r.currency = $2)
ORDER BY o.name, r.currency, r.effective_from DESC;

-- name: LockOrgRateSeries :exec
SELECT pg_advisory_xact_lock(hashtextextended('org_rate:' || $1::text || ':' || $2::text, 0));

-- name: GetOrgRateSeriesLatestFrom :one
-- The latest effective_from of the (organization, currency) series, NULL when
-- it has no rows.
SELECT max(effective_from) AS latest_effective_from
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2;

-- name: CloseOrgRateOverridesAt :many
-- Ends the individual rates that are still open at $3 (the start of the new
-- one). Called only with $3 in the future, so no past period is touched.
UPDATE org_rate_overrides
SET    effective_to = $3::timestamptz
WHERE  org_id = $1 AND currency = $2 AND effective_from < $3::timestamptz
  AND  (effective_to IS NULL OR effective_to > $3::timestamptz)
RETURNING id;

-- name: InsertOrgRateOverride :one
INSERT INTO org_rate_overrides (
    org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at;

-- name: LockOrgRateOverrideByID :one
SELECT id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at
FROM   org_rate_overrides
WHERE  id = $1
FOR UPDATE;

-- name: NextOrgRateOverrideFrom :one
-- The start of the next individual rate of the same series after $3, NULL when
-- there is none.
SELECT min(effective_from) AS next_effective_from
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2 AND effective_from > $3;

-- name: SetOrgRateOverrideEnd :one
-- Closing for the future ($2 = the new end, $3 = now). Repeats the guard rules
-- of the handler; zero rows means the caller loses (pgx.ErrNoRows).
UPDATE org_rate_overrides
SET    effective_to = $2::timestamptz
WHERE  id = $1 AND $2::timestamptz > effective_from AND $2::timestamptz > $3::timestamptz
  AND  (effective_to IS NULL OR (effective_to > $3::timestamptz AND $2::timestamptz < effective_to))
RETURNING id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at;

-- name: DeleteNotStartedOrgRateOverride :execrows
DELETE FROM org_rate_overrides
WHERE  id = $1 AND effective_from > $2;

-- name: ListBillingAccountsAdmin :many
-- $1 = organization (zero uuid = all).
SELECT a.id, a.org_id, a.currency, a.issuer_id, a.account_kind, a.payer, a.tax_status,
       a.tax_id_verified_at, a.auto_topup, a.created_at, a.updated_at, o.name AS org_name
FROM   billing_accounts a
JOIN   organizations o ON o.id = a.org_id
WHERE  ($1::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR a.org_id = $1)
ORDER BY o.name, a.account_kind, a.currency;

-- name: ListBillingAccountTermsForAccounts :many
SELECT id, account_id, effective_from, price_list_code, plan_kind, billing_cycle,
       shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
       created_by, reason, created_at
FROM   billing_account_terms
WHERE  account_id = ANY($1::uuid[])
ORDER BY account_id, effective_from;

-- name: LockBillingAccountTermsSeries :exec
SELECT pg_advisory_xact_lock(hashtextextended('account_terms:' || $1::text, 0));

-- name: GetBillingAccountTermsLatestFrom :one
SELECT max(effective_from) AS latest_effective_from
FROM   billing_account_terms
WHERE  account_id = $1;

-- name: OrganizationExistsForBilling :one
SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1 AND deleted_at IS NULL);
