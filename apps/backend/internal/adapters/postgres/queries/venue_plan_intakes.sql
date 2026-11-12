-- Hall plan intakes (migration 0216, spec 08_architecture/64).
-- The hand-maintained wrappers live in gen/venue_plan_intakes.sql.go; the
-- column lists below are the venuePlan*Columns constants there.
--
-- Every change of an intake runs under LockVenuePlanIntake. State moves are
-- guarded by the previous state (UpdateVenuePlanIntakeState,
-- ApproveVenuePlanIntake): a stale move answers no row. The state machine
-- itself is venueplan.CanTransition.

-- ── inserts ─────────────────────────────────────────────────────────────────

-- name: InsertVenuePlanIntake :one
INSERT INTO venue_plan_intakes AS i (org_id, venue_id, created_by, channel, state,
       escalation_reason, escalation_text, idempotency_key, closed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
        CASE WHEN $5::text IN ('approved', 'rejected', 'cancelled') THEN now() END)
ON CONFLICT (org_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at;

-- name: InsertVenuePlanIntakeSource :one
INSERT INTO venue_plan_intake_sources AS s (intake_id, media_id, normalized_media_id, sha256, kind,
       file_name, byte_size, pages, width, height, needs_conversion, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING s.id, s.intake_id, s.media_id, s.normalized_media_id, s.sha256, s.kind,
       s.file_name, s.byte_size, s.pages, s.width, s.height, s.needs_conversion, s.position, s.created_at;

-- name: InsertVenuePlanDraft :one
INSERT INTO venue_plan_drafts AS d (intake_id, n, author, author_user_id, generator_version,
       model, report, svg_media_id, preview_media_id, note)
VALUES ($1,
        (SELECT COALESCE(max(x.n), 0) + 1 FROM venue_plan_drafts x WHERE x.intake_id = $1),
        $2, $3, $4, $5, COALESCE($6::jsonb, '{}'::jsonb), $7, $8, $9)
RETURNING d.id, d.intake_id, d.n, d.author, d.author_user_id, d.generator_version,
       d.model, d.report, d.svg_media_id, d.preview_media_id, d.note, d.created_at;

-- name: InsertVenuePlanQuestion :one
WITH q AS (
    INSERT INTO venue_plan_questions (draft_id, kind, target, region, options)
    VALUES ($1, $2, COALESCE($3::jsonb, '{}'::jsonb), $4, COALESCE($5::jsonb, '[]'::jsonb))
    RETURNING *
)
SELECT q.id, q.draft_id, d.n, q.kind, q.target, q.region, q.options,
       q.answer, q.answered_by, q.answered_at, q.created_at
FROM   q
JOIN   venue_plan_drafts d ON d.id = q.draft_id;

-- name: InsertVenuePlanCorrection :one
INSERT INTO venue_plan_corrections AS c (intake_id, draft_id, kind, text, operations, status, created_by)
VALUES ($1, $2, $3, $4, $5, COALESCE(NULLIF($6::text, ''), 'pending'), $7)
RETURNING c.id, c.intake_id, c.draft_id, c.kind, c.text, c.operations,
       c.status, c.created_by, c.created_at;

-- name: InsertVenuePlanNotice :one
INSERT INTO venue_plan_notices (intake_id, kind, note)
VALUES ($1, $2, $3)
RETURNING id, intake_id, kind, note, created_at, claimed_at;

-- ── intake reads ────────────────────────────────────────────────────────────

-- name: GetVenuePlanIntake :one
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at
FROM   venue_plan_intakes i
WHERE  i.id = $1 AND i.org_id = $2;

-- name: GetVenuePlanIntakeByID :one
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at
FROM   venue_plan_intakes i
WHERE  i.id = $1;

-- name: GetVenuePlanIntakeByIdempotencyKey :one
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at
FROM   venue_plan_intakes i
WHERE  i.org_id = $1 AND i.idempotency_key = $2;

-- name: LockVenuePlanIntake :one
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at
FROM   venue_plan_intakes i
WHERE  i.id = $1
FOR UPDATE;

-- name: ListVenuePlanIntakesByOrg :many
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at
FROM   venue_plan_intakes i
WHERE  i.org_id = $1
  AND  ($2::text IS NULL OR i.state = $2)
  AND  ($3::uuid IS NULL OR i.venue_id = $3)
ORDER  BY i.created_at DESC, i.id DESC
LIMIT  $4;

-- name: ListVenuePlanIntakesAdmin :many
SELECT i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at,
       o.name AS org_name,
       v.name AS venue_name,
       COALESCE(t_city.value, ci.slug) AS venue_city
FROM   venue_plan_intakes i
JOIN   organizations o ON o.id = i.org_id
JOIN   venues v        ON v.id = i.venue_id
LEFT JOIN cities ci    ON ci.id = v.city_id
LEFT JOIN i18n_text t_city ON t_city.namespace = 'geo.cities'
    AND t_city.key = ci.slug
    AND t_city.locale = 'en'
WHERE  ($1::text IS NULL OR i.state = $1)
  AND  ($2::uuid IS NULL OR i.org_id = $2)
  AND  (NOT $3::boolean OR i.operator_id IS NULL)
ORDER  BY i.created_at DESC, i.id DESC
LIMIT  $4;

-- ── children of an intake ───────────────────────────────────────────────────

-- name: ListVenuePlanIntakeSources :many
SELECT s.id, s.intake_id, s.media_id, s.normalized_media_id, s.sha256, s.kind,
       s.file_name, s.byte_size, s.pages, s.width, s.height, s.needs_conversion, s.position, s.created_at
FROM   venue_plan_intake_sources s
WHERE  s.intake_id = $1
ORDER  BY s.position;

-- name: GetVenuePlanIntakeSource :one
SELECT s.id, s.intake_id, s.media_id, s.normalized_media_id, s.sha256, s.kind,
       s.file_name, s.byte_size, s.pages, s.width, s.height, s.needs_conversion, s.position, s.created_at
FROM   venue_plan_intake_sources s
WHERE  s.id = $1 AND s.intake_id = $2;

-- name: ListVenuePlanDrafts :many
SELECT d.id, d.intake_id, d.n, d.author, d.author_user_id, d.generator_version,
       d.model, d.report, d.svg_media_id, d.preview_media_id, d.note, d.created_at
FROM   venue_plan_drafts d
WHERE  d.intake_id = $1
ORDER  BY d.n;

-- name: GetVenuePlanDraft :one
SELECT d.id, d.intake_id, d.n, d.author, d.author_user_id, d.generator_version,
       d.model, d.report, d.svg_media_id, d.preview_media_id, d.note, d.created_at
FROM   venue_plan_drafts d
WHERE  d.intake_id = $1 AND d.n = $2;

-- name: ListVenuePlanQuestions :many
SELECT q.id, q.draft_id, d.n, q.kind, q.target, q.region, q.options,
       q.answer, q.answered_by, q.answered_at, q.created_at
FROM   venue_plan_questions q
JOIN   venue_plan_drafts d ON d.id = q.draft_id
WHERE  d.intake_id = $1
ORDER  BY d.n, q.created_at, q.id;

-- name: ListVenuePlanCorrections :many
SELECT c.id, c.intake_id, c.draft_id, c.kind, c.text, c.operations,
       c.status, c.created_by, c.created_at
FROM   venue_plan_corrections c
WHERE  c.intake_id = $1
ORDER  BY c.created_at, c.id;

-- ── intake writes ───────────────────────────────────────────────────────────

-- name: UpdateVenuePlanIntakeState :one
UPDATE venue_plan_intakes AS i
SET    state             = $3,
       escalation_reason = COALESCE($4, i.escalation_reason),
       escalation_text   = COALESCE($5, i.escalation_text),
       reject_reason     = COALESCE($6, i.reject_reason),
       return_note       = COALESCE($7, i.return_note),
       updated_at        = now(),
       closed_at         = CASE WHEN $3::text IN ('approved', 'rejected', 'cancelled') THEN now() END
WHERE  i.id = $1 AND i.state = $2
RETURNING i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at;

-- name: ClaimVenuePlanIntake :one
UPDATE venue_plan_intakes AS i
SET    operator_id = $2, operator_claimed_at = now(), updated_at = now()
WHERE  i.id = $1 AND i.closed_at IS NULL
RETURNING i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at;

-- name: SetVenuePlanIntakeCurrentDraft :one
UPDATE venue_plan_intakes AS i
SET    current_draft = $2, updated_at = now()
WHERE  i.id = $1 AND i.closed_at IS NULL
  AND  EXISTS (SELECT 1 FROM venue_plan_drafts d WHERE d.intake_id = i.id AND d.n = $2)
RETURNING i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at;

-- name: AnswerVenuePlanQuestion :execrows
UPDATE venue_plan_questions
SET    answer = $3, answered_by = $4, answered_at = now()
WHERE  id = $1 AND draft_id = $2;

-- name: ApproveVenuePlanIntake :one
UPDATE venue_plan_intakes AS i
SET    state = 'approved', plan_id = $3, plan_version = $4,
       updated_at = now(), closed_at = now()
WHERE  i.id = $1 AND i.state = $2
RETURNING i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at;

-- ── notices and notifications ───────────────────────────────────────────────

-- name: ClaimVenuePlanNotices :many
WITH claimed AS (
    UPDATE venue_plan_notices n
    SET    claimed_at = now()
    WHERE  n.id IN (
               SELECT x.id FROM venue_plan_notices x
               WHERE  x.claimed_at IS NULL
               ORDER  BY x.created_at
               LIMIT  $1
               FOR UPDATE SKIP LOCKED)
    RETURNING n.id, n.intake_id, n.kind, n.note, n.created_at
)
SELECT c.id, c.intake_id, c.kind, c.note, c.created_at,
       i.org_id, i.venue_id, v.name, i.current_draft,
       COALESCE((SELECT array_agg(l.telegram_user_id ORDER BY l.telegram_user_id)
                 FROM   bot_telegram_links l
                 WHERE  l.user_id = i.created_by AND l.revoked_at IS NULL), '{}'::bigint[])
FROM   claimed c
JOIN   venue_plan_intakes i ON i.id = c.intake_id
JOIN   venues v             ON v.id = i.venue_id
ORDER  BY c.created_at, c.id;

-- name: InsertVenuePlanNotifyLog :one
INSERT INTO venue_plan_notify_log (subject_id, notify_key)
VALUES ($1, $2)
ON CONFLICT DO NOTHING
RETURNING subject_id;

-- ── seating plan verification ───────────────────────────────────────────────

-- name: SetSeatingPlanVerification :execrows
UPDATE seating_plans
SET    verification_state = $2,
       verification_note  = $3,
       verified_at        = CASE WHEN $2::text = 'verified' THEN now() END,
       verified_by        = CASE WHEN $2::text = 'verified' THEN $4::uuid END,
       visibility         = COALESCE($5, visibility),
       updated_at         = now()
WHERE  id = $1 AND deleted_at IS NULL;

-- name: ListSeatingPlansByVerificationState :many
SELECT p.id, p.name, p.plan_type, p.visibility, p.status, p.owner_org_id, o.name,
       p.venue_id, v.name, p.verification_state, p.verification_note, p.verified_at, p.verified_by,
       p.current_version_id, sv.version_number, sv.capacity_seated, sv.capacity_standing, p.created_at
FROM   seating_plans p
JOIN   organizations o ON o.id = p.owner_org_id
JOIN   venues v        ON v.id = p.venue_id
LEFT JOIN seating_plan_versions sv ON sv.id = p.current_version_id
WHERE  p.verification_state = $1 AND p.deleted_at IS NULL
ORDER  BY p.created_at, p.id
LIMIT  $2;

-- name: ListVerifiedSeatingPlansOfVenue :many
SELECT p.id, p.name, p.plan_type, p.visibility, p.status, p.owner_org_id, o.name,
       p.venue_id, v.name, p.verification_state, p.verification_note, p.verified_at, p.verified_by,
       p.current_version_id, sv.version_number, sv.capacity_seated, sv.capacity_standing, p.created_at
FROM   seating_plans p
JOIN   organizations o ON o.id = p.owner_org_id
JOIN   venues v        ON v.id = p.venue_id
LEFT JOIN seating_plan_versions sv ON sv.id = p.current_version_id
WHERE  p.venue_id = $1
  AND  p.visibility = 'operator_verified'
  AND  p.verification_state = 'verified'
  AND  p.status = 'active'
  AND  p.deleted_at IS NULL
  AND  p.current_version_id IS NOT NULL
ORDER  BY p.name, p.id;
