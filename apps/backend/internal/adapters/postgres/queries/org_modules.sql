-- Organization module periods (migration 0198, spec 08_architecture/61).
-- The hand-maintained wrappers live in gen/org_modules.sql.go.
-- "Open shop order" is defined ONCE, as openShopOrderPredicate in the wrapper
-- (and mirrored by the partial index shop_orders_open_idx of migration 0198):
--   status IN ('pending_payment', 'manual_review')
--   OR (status IN ('paid', 'partially_refunded') AND shipped_at IS NULL AND delivery_kind <> 'none')

-- name: LockOrgModule :exec
SELECT pg_advisory_xact_lock(hashtextextended('org_module:' || $1::text || ':' || $2::text, 0));

-- name: ListOrgModulePeriodsOverlappingForUpdate :many
SELECT m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  m.module = $2
  AND  (m.ends_on IS NULL OR m.ends_on >= m.starts_on)
  AND  m.starts_on <= COALESCE($4::date, 'infinity'::date)
  AND  COALESCE(m.ends_on, 'infinity'::date) >= $3::date
  AND  ($5::uuid IS NULL OR m.id <> $5)
ORDER  BY m.starts_on, m.id
FOR UPDATE;

-- name: InsertOrgModulePeriod :one
INSERT INTO org_module_periods AS m (org_id, module, starts_on, ends_on, monthly_price_minor, currency, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at;

-- name: GetOrgModulePeriod :one
SELECT m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at
FROM   org_module_periods m
WHERE  m.id = $1;

-- name: GetOrgModulePeriodForUpdate :one
SELECT m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at
FROM   org_module_periods m
WHERE  m.id = $1
FOR UPDATE;

-- name: EndOrgModulePeriod :one
UPDATE org_module_periods AS m
SET    ends_on = $2, ended_by = $3, ended_at = now()
WHERE  m.id = $1
  AND  (m.ends_on IS NULL OR m.ends_on > $2::date)
RETURNING m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at;

-- name: ListOrgModulePeriods :many
SELECT m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  ($2::text IS NULL OR m.module = $2)
ORDER  BY m.module, m.starts_on, m.id;

-- name: OrgModuleActive :one
SELECT EXISTS (
    SELECT 1 FROM org_module_periods m
    WHERE  m.org_id = $1
      AND  m.module = $2
      AND  m.starts_on <= $3::date
      AND  (m.ends_on IS NULL OR m.ends_on >= $3::date));

-- name: ActiveOrgModules :many
SELECT DISTINCT m.module
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  m.starts_on <= $2::date
  AND  (m.ends_on IS NULL OR m.ends_on >= $2::date)
ORDER  BY m.module;

-- name: OrgModuleStates :many
-- One row per requested organization: the modules active on the day and
-- whether the organization has an open shop order.
SELECT o.id,
       COALESCE(array_agg(DISTINCT m.module ORDER BY m.module) FILTER (WHERE m.module IS NOT NULL), '{}')::text[],
       EXISTS (SELECT 1 FROM shop_orders so WHERE so.org_id = o.id AND (so.status IN ('pending_payment', 'manual_review')
       OR (so.status IN ('paid', 'partially_refunded') AND so.shipped_at IS NULL AND so.delivery_kind <> 'none')))
FROM   unnest($1::uuid[]) AS o(id)
LEFT   JOIN org_module_periods m
       ON  m.org_id = o.id AND m.starts_on <= $2::date AND (m.ends_on IS NULL OR m.ends_on >= $2::date)
GROUP  BY o.id;

-- name: CountOpenShopOrders :one
SELECT count(*) FROM shop_orders so WHERE so.org_id = $1 AND (so.status IN ('pending_payment', 'manual_review')
       OR (so.status IN ('paid', 'partially_refunded') AND so.shipped_at IS NULL AND so.delivery_kind <> 'none'));
