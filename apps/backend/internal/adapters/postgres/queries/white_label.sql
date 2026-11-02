-- White label periods (migration 0187, spec 08_architecture/53).
-- The hand-maintained wrappers live in gen/white_label.sql.go.

-- name: LockWhiteLabelBrand :exec
SELECT pg_advisory_xact_lock(hashtextextended('white_label:' || $1::text || ':' || COALESCE($2::text, ''), 0));

-- name: CountOverlappingWhiteLabelPeriods :one
SELECT count(*)
FROM   white_label_periods w
WHERE  w.org_id = $1
  AND  w.promoter_id IS NOT DISTINCT FROM $2
  AND  (w.ends_on IS NULL OR w.ends_on >= w.starts_on)
  AND  w.starts_on <= COALESCE($4::date, 'infinity'::date)
  AND  COALESCE(w.ends_on, 'infinity'::date) >= $3::date;

-- name: InsertWhiteLabelPeriod :one
INSERT INTO white_label_periods AS w (org_id, promoter_id, starts_on, ends_on, monthly_price_minor, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at;

-- name: GetWhiteLabelPeriod :one
SELECT w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at
FROM   white_label_periods w
WHERE  w.id = $1;

-- name: EndWhiteLabelPeriod :one
UPDATE white_label_periods AS w
SET    ends_on = $2, ended_by = $3, ended_at = now()
WHERE  w.id = $1
  AND  (w.ends_on IS NULL OR w.ends_on > $2::date)
RETURNING w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at;

-- name: ListWhiteLabelPeriods :many
SELECT w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at, o.name, p.name
FROM   white_label_periods w
JOIN   organizations o ON o.id = w.org_id
LEFT   JOIN org_promoters p ON p.id = w.promoter_id
WHERE  ($1::uuid IS NULL OR w.org_id = $1)
ORDER  BY o.name, p.name NULLS FIRST, w.starts_on DESC, w.id
LIMIT  1000;

-- name: ListWhiteLabelPeriodsForMonth :many
SELECT w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at
FROM   white_label_periods w
WHERE  w.starts_on <= $2::date
  AND  (w.ends_on IS NULL OR w.ends_on >= $1::date)
  AND  (w.ends_on IS NULL OR w.ends_on >= w.starts_on)
  AND  ($3::uuid IS NULL OR w.org_id = $3)
ORDER  BY w.id;

-- name: ListWhiteLabelEvents :many
SELECT e.id
FROM   events e
LEFT   JOIN event_promoters ep ON ep.event_id = e.id
WHERE  e.id = ANY($1::uuid[])
  AND  EXISTS (
    SELECT 1 FROM white_label_periods w
    WHERE  w.org_id = e.org_id
      AND  w.promoter_id IS NOT DISTINCT FROM ep.promoter_id
      AND  w.starts_on <= $2::date
      AND  (w.ends_on IS NULL OR w.ends_on >= $2::date));

-- name: EventIDForTicket :one
SELECT e.id
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events e   ON e.id = s.event_id
WHERE  t.id = $1;

-- name: WhiteLabelActiveForOrg :one
SELECT EXISTS (
    SELECT 1 FROM white_label_periods w
    WHERE  w.org_id = $1
      AND  w.promoter_id IS NULL
      AND  w.starts_on <= $2::date
      AND  (w.ends_on IS NULL OR w.ends_on >= $2::date));

