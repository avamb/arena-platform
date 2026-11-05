-- Organization module periods for the monthly charge (spec 08_architecture/61
-- section 3.2). The hand-maintained wrapper lives in
-- gen/org_modules_billing.sql.go.

-- name: ListOrgModulePeriodsForMonth :many
-- The non-empty periods of one module that cover at least one day of
-- [firstDay, lastDay]; orgID narrows to one organization (tests). A period of
-- an organization deleted BEFORE the month starts is not charged for it: an
-- open period of an organization that left would otherwise be charged
-- forever. The month of the deletion is still charged for its days, like
-- white label.
SELECT m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at
FROM   org_module_periods m
WHERE  m.module = $1
  AND  m.starts_on <= $3::date
  AND  (m.ends_on IS NULL OR m.ends_on >= $2::date)
  AND  (m.ends_on IS NULL OR m.ends_on >= m.starts_on)
  AND  ($4::uuid IS NULL OR m.org_id = $4)
  AND  NOT EXISTS (SELECT 1 FROM organizations o
                   WHERE o.id = m.org_id AND o.deleted_at < ($2::date)::timestamp AT TIME ZONE 'UTC')
ORDER  BY m.id;
