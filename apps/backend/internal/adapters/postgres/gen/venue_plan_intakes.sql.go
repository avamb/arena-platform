// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: venue_plan_intakes.sql (migration 0216, spec 08_architecture/64)

package gen

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Rows
// ─────────────────────────────────────────────────────────────────────────────

// VenuePlanIntakeRow is one venue_plan_intakes row. State, Channel and
// EscalationReason hold the values of package venueplan.
type VenuePlanIntakeRow struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	VenueID           uuid.UUID  `json:"venue_id"`
	CreatedBy         *uuid.UUID `json:"created_by"`
	Channel           string     `json:"channel"`
	State             string     `json:"state"`
	EscalationReason  *string    `json:"escalation_reason"`
	EscalationText    *string    `json:"escalation_text"`
	OperatorID        *uuid.UUID `json:"operator_id"`
	OperatorClaimedAt *time.Time `json:"operator_claimed_at"`
	CurrentDraft      *int32     `json:"current_draft"`
	PlanID            *uuid.UUID `json:"plan_id"`
	PlanVersion       *int32     `json:"plan_version"`
	RejectReason      *string    `json:"reject_reason"`
	ReturnNote        *string    `json:"return_note"`
	IdempotencyKey    *string    `json:"idempotency_key"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ClosedAt          *time.Time `json:"closed_at"`
}

const venuePlanIntakeColumns = `i.id, i.org_id, i.venue_id, i.created_by, i.channel, i.state,
       i.escalation_reason, i.escalation_text, i.operator_id, i.operator_claimed_at,
       i.current_draft, i.plan_id, i.plan_version, i.reject_reason, i.return_note,
       i.idempotency_key, i.created_at, i.updated_at, i.closed_at`

func venuePlanIntakeDest(r *VenuePlanIntakeRow) []any {
	return []any{&r.ID, &r.OrgID, &r.VenueID, &r.CreatedBy, &r.Channel, &r.State,
		&r.EscalationReason, &r.EscalationText, &r.OperatorID, &r.OperatorClaimedAt,
		&r.CurrentDraft, &r.PlanID, &r.PlanVersion, &r.RejectReason, &r.ReturnNote,
		&r.IdempotencyKey, &r.CreatedAt, &r.UpdatedAt, &r.ClosedAt}
}

func scanVenuePlanIntake(row interface{ Scan(...any) error }) (VenuePlanIntakeRow, error) {
	var r VenuePlanIntakeRow
	err := row.Scan(venuePlanIntakeDest(&r)...)
	return r, err
}

// VenuePlanIntakeAdminRow is an intake of the administrators' list, with the
// names an administrator needs to recognise it.
type VenuePlanIntakeAdminRow struct {
	VenuePlanIntakeRow
	OrgName   string  `json:"org_name"`
	VenueName string  `json:"venue_name"`
	VenueCity *string `json:"venue_city"`
}

// VenuePlanIntakeSourceRow is one venue_plan_intake_sources row.
type VenuePlanIntakeSourceRow struct {
	ID                uuid.UUID  `json:"id"`
	IntakeID          uuid.UUID  `json:"intake_id"`
	MediaID           uuid.UUID  `json:"media_id"`
	NormalizedMediaID *uuid.UUID `json:"normalized_media_id"`
	Sha256            string     `json:"sha256"`
	Kind              string     `json:"kind"`
	FileName          string     `json:"file_name"`
	ByteSize          int64      `json:"byte_size"`
	Pages             *int32     `json:"pages"`
	Width             *int32     `json:"width"`
	Height            *int32     `json:"height"`
	NeedsConversion   bool       `json:"needs_conversion"`
	Position          int32      `json:"position"`
	CreatedAt         time.Time  `json:"created_at"`
}

const venuePlanSourceColumns = `s.id, s.intake_id, s.media_id, s.normalized_media_id, s.sha256, s.kind,
       s.file_name, s.byte_size, s.pages, s.width, s.height, s.needs_conversion, s.position, s.created_at`

func scanVenuePlanSource(row interface{ Scan(...any) error }) (VenuePlanIntakeSourceRow, error) {
	var r VenuePlanIntakeSourceRow
	err := row.Scan(&r.ID, &r.IntakeID, &r.MediaID, &r.NormalizedMediaID, &r.Sha256, &r.Kind,
		&r.FileName, &r.ByteSize, &r.Pages, &r.Width, &r.Height, &r.NeedsConversion, &r.Position, &r.CreatedAt)
	return r, err
}

// VenuePlanDraftRow is one venue_plan_drafts row. Model is nil when the
// draft has no model (an SVG drawn by hand); Report is never nil.
type VenuePlanDraftRow struct {
	ID               uuid.UUID       `json:"id"`
	IntakeID         uuid.UUID       `json:"intake_id"`
	N                int32           `json:"n"`
	Author           string          `json:"author"`
	AuthorUserID     *uuid.UUID      `json:"author_user_id"`
	GeneratorVersion *string         `json:"generator_version"`
	Model            json.RawMessage `json:"model"`
	Report           json.RawMessage `json:"report"`
	SvgMediaID       uuid.UUID       `json:"svg_media_id"`
	PreviewMediaID   *uuid.UUID      `json:"preview_media_id"`
	Note             *string         `json:"note"`
	CreatedAt        time.Time       `json:"created_at"`
}

const venuePlanDraftColumns = `d.id, d.intake_id, d.n, d.author, d.author_user_id, d.generator_version,
       d.model, d.report, d.svg_media_id, d.preview_media_id, d.note, d.created_at`

func scanVenuePlanDraft(row interface{ Scan(...any) error }) (VenuePlanDraftRow, error) {
	var r VenuePlanDraftRow
	err := row.Scan(&r.ID, &r.IntakeID, &r.N, &r.Author, &r.AuthorUserID, &r.GeneratorVersion,
		&r.Model, &r.Report, &r.SvgMediaID, &r.PreviewMediaID, &r.Note, &r.CreatedAt)
	return r, err
}

// VenuePlanQuestionRow is one venue_plan_questions row; DraftN is the number
// of its draft.
type VenuePlanQuestionRow struct {
	ID         uuid.UUID       `json:"id"`
	DraftID    uuid.UUID       `json:"draft_id"`
	DraftN     int32           `json:"draft_n"`
	Kind       string          `json:"kind"`
	Target     json.RawMessage `json:"target"`
	Region     json.RawMessage `json:"region"`
	Options    json.RawMessage `json:"options"`
	Answer     json.RawMessage `json:"answer"`
	AnsweredBy *uuid.UUID      `json:"answered_by"`
	AnsweredAt *time.Time      `json:"answered_at"`
	CreatedAt  time.Time       `json:"created_at"`
}

const venuePlanQuestionColumns = `q.id, q.draft_id, d.n, q.kind, q.target, q.region, q.options,
       q.answer, q.answered_by, q.answered_at, q.created_at`

func scanVenuePlanQuestion(row interface{ Scan(...any) error }) (VenuePlanQuestionRow, error) {
	var r VenuePlanQuestionRow
	err := row.Scan(&r.ID, &r.DraftID, &r.DraftN, &r.Kind, &r.Target, &r.Region, &r.Options,
		&r.Answer, &r.AnsweredBy, &r.AnsweredAt, &r.CreatedAt)
	return r, err
}

// VenuePlanCorrectionRow is one venue_plan_corrections row.
type VenuePlanCorrectionRow struct {
	ID         uuid.UUID       `json:"id"`
	IntakeID   uuid.UUID       `json:"intake_id"`
	DraftID    *uuid.UUID      `json:"draft_id"`
	Kind       string          `json:"kind"`
	Text       *string         `json:"text"`
	Operations json.RawMessage `json:"operations"`
	Status     string          `json:"status"`
	CreatedBy  *uuid.UUID      `json:"created_by"`
	CreatedAt  time.Time       `json:"created_at"`
}

const venuePlanCorrectionColumns = `c.id, c.intake_id, c.draft_id, c.kind, c.text, c.operations,
       c.status, c.created_by, c.created_at`

func scanVenuePlanCorrection(row interface{ Scan(...any) error }) (VenuePlanCorrectionRow, error) {
	var r VenuePlanCorrectionRow
	err := row.Scan(&r.ID, &r.IntakeID, &r.DraftID, &r.Kind, &r.Text, &r.Operations,
		&r.Status, &r.CreatedBy, &r.CreatedAt)
	return r, err
}

// VenuePlanNoticeRow is one venue_plan_notices row.
type VenuePlanNoticeRow struct {
	ID        uuid.UUID  `json:"id"`
	IntakeID  uuid.UUID  `json:"intake_id"`
	Kind      string     `json:"kind"`
	Note      *string    `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	ClaimedAt *time.Time `json:"claimed_at"`
}

func collectRows[T any](rows pgx.Rows, err error, scan func(interface{ Scan(...any) error }) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Inserts
// ─────────────────────────────────────────────────────────────────────────────

const insertVenuePlanIntake = `-- name: InsertVenuePlanIntake :one
INSERT INTO venue_plan_intakes AS i (org_id, venue_id, created_by, channel, state,
       escalation_reason, escalation_text, idempotency_key, closed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
        CASE WHEN $5::text IN ('approved', 'rejected', 'cancelled') THEN now() END)
ON CONFLICT (org_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING ` + venuePlanIntakeColumns

// InsertVenuePlanIntakeParams are the columns of a new intake. State is the
// first state (received, or review for a clean source SVG, or operator).
type InsertVenuePlanIntakeParams struct {
	OrgID            uuid.UUID
	VenueID          uuid.UUID
	CreatedBy        *uuid.UUID
	Channel          string
	State            string
	EscalationReason *string
	EscalationText   *string
	IdempotencyKey   *string
}

// InsertVenuePlanIntake writes a new intake. When IdempotencyKey is set and
// the organization already has an intake with that key, nothing is written
// and it answers pgx.ErrNoRows: read the existing one with
// GetVenuePlanIntakeByIdempotencyKey.
func (q *Queries) InsertVenuePlanIntake(ctx context.Context, p InsertVenuePlanIntakeParams) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, insertVenuePlanIntake,
		p.OrgID, p.VenueID, p.CreatedBy, p.Channel, p.State,
		p.EscalationReason, p.EscalationText, p.IdempotencyKey))
}

const insertVenuePlanIntakeSource = `-- name: InsertVenuePlanIntakeSource :one
INSERT INTO venue_plan_intake_sources AS s (intake_id, media_id, normalized_media_id, sha256, kind,
       file_name, byte_size, pages, width, height, needs_conversion, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING ` + venuePlanSourceColumns

// InsertVenuePlanIntakeSourceParams are the columns of a new source file.
type InsertVenuePlanIntakeSourceParams struct {
	IntakeID          uuid.UUID
	MediaID           uuid.UUID
	NormalizedMediaID *uuid.UUID
	Sha256            string
	Kind              string
	FileName          string
	ByteSize          int64
	Pages             *int32
	Width             *int32
	Height            *int32
	NeedsConversion   bool
	Position          int32
}

// InsertVenuePlanIntakeSource writes one source file of an intake.
func (q *Queries) InsertVenuePlanIntakeSource(ctx context.Context, p InsertVenuePlanIntakeSourceParams) (VenuePlanIntakeSourceRow, error) {
	return scanVenuePlanSource(q.db.QueryRow(ctx, insertVenuePlanIntakeSource,
		p.IntakeID, p.MediaID, p.NormalizedMediaID, p.Sha256, p.Kind,
		p.FileName, p.ByteSize, p.Pages, p.Width, p.Height, p.NeedsConversion, p.Position))
}

const insertVenuePlanDraft = `-- name: InsertVenuePlanDraft :one
INSERT INTO venue_plan_drafts AS d (intake_id, n, author, author_user_id, generator_version,
       model, report, svg_media_id, preview_media_id, note)
VALUES ($1,
        (SELECT COALESCE(max(x.n), 0) + 1 FROM venue_plan_drafts x WHERE x.intake_id = $1),
        $2, $3, $4, $5, COALESCE($6::jsonb, '{}'::jsonb), $7, $8, $9)
RETURNING ` + venuePlanDraftColumns

// InsertVenuePlanDraftParams are the columns of a new draft; its number n is
// the next one of the intake. A nil Report is stored as {}.
type InsertVenuePlanDraftParams struct {
	IntakeID         uuid.UUID
	Author           string
	AuthorUserID     *uuid.UUID
	GeneratorVersion *string
	Model            json.RawMessage
	Report           json.RawMessage
	SvgMediaID       uuid.UUID
	PreviewMediaID   *uuid.UUID
	Note             *string
}

// InsertVenuePlanDraft writes the next draft of an intake (n = the highest n
// plus 1). Call it under LockVenuePlanIntake: two concurrent drafts would
// pick the same n and one would fail on venue_plan_drafts_n_uq.
func (q *Queries) InsertVenuePlanDraft(ctx context.Context, p InsertVenuePlanDraftParams) (VenuePlanDraftRow, error) {
	return scanVenuePlanDraft(q.db.QueryRow(ctx, insertVenuePlanDraft,
		p.IntakeID, p.Author, p.AuthorUserID, p.GeneratorVersion,
		p.Model, p.Report, p.SvgMediaID, p.PreviewMediaID, p.Note))
}

const insertVenuePlanQuestion = `-- name: InsertVenuePlanQuestion :one
WITH q AS (
    INSERT INTO venue_plan_questions (draft_id, kind, target, region, options)
    VALUES ($1, $2, COALESCE($3::jsonb, '{}'::jsonb), $4, COALESCE($5::jsonb, '[]'::jsonb))
    RETURNING *
)
SELECT ` + venuePlanQuestionColumns + `
FROM   q
JOIN   venue_plan_drafts d ON d.id = q.draft_id`

// InsertVenuePlanQuestionParams are the columns of a new question. A nil
// Target is stored as {}, a nil Options as [].
type InsertVenuePlanQuestionParams struct {
	DraftID uuid.UUID
	Kind    string
	Target  json.RawMessage
	Region  json.RawMessage
	Options json.RawMessage
}

// InsertVenuePlanQuestion writes one question of a draft (the generator job
// of wave 2; tests in wave 1).
func (q *Queries) InsertVenuePlanQuestion(ctx context.Context, p InsertVenuePlanQuestionParams) (VenuePlanQuestionRow, error) {
	return scanVenuePlanQuestion(q.db.QueryRow(ctx, insertVenuePlanQuestion,
		p.DraftID, p.Kind, p.Target, p.Region, p.Options))
}

const insertVenuePlanCorrection = `-- name: InsertVenuePlanCorrection :one
INSERT INTO venue_plan_corrections AS c (intake_id, draft_id, kind, text, operations, status, created_by)
VALUES ($1, $2, $3, $4, $5, COALESCE(NULLIF($6::text, ''), 'pending'), $7)
RETURNING ` + venuePlanCorrectionColumns

// InsertVenuePlanCorrectionParams are the columns of a new correction; an
// empty Status is pending.
type InsertVenuePlanCorrectionParams struct {
	IntakeID   uuid.UUID
	DraftID    *uuid.UUID
	Kind       string
	Text       *string
	Operations json.RawMessage
	Status     string
	CreatedBy  *uuid.UUID
}

// InsertVenuePlanCorrection writes one correction (answers, a text, an
// administrator's edit) for the training corpus.
func (q *Queries) InsertVenuePlanCorrection(ctx context.Context, p InsertVenuePlanCorrectionParams) (VenuePlanCorrectionRow, error) {
	return scanVenuePlanCorrection(q.db.QueryRow(ctx, insertVenuePlanCorrection,
		p.IntakeID, p.DraftID, p.Kind, p.Text, p.Operations, p.Status, p.CreatedBy))
}

const insertVenuePlanNotice = `-- name: InsertVenuePlanNotice :one
INSERT INTO venue_plan_notices (intake_id, kind, note)
VALUES ($1, $2, $3)
RETURNING id, intake_id, kind, note, created_at, claimed_at`

// InsertVenuePlanNotice queues a notice for the bot (kind of
// venueplan.NoticeKind).
func (q *Queries) InsertVenuePlanNotice(ctx context.Context, intakeID uuid.UUID, kind string, note *string) (VenuePlanNoticeRow, error) {
	var r VenuePlanNoticeRow
	err := q.db.QueryRow(ctx, insertVenuePlanNotice, intakeID, kind, note).
		Scan(&r.ID, &r.IntakeID, &r.Kind, &r.Note, &r.CreatedAt, &r.ClaimedAt)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Intake reads
// ─────────────────────────────────────────────────────────────────────────────

const getVenuePlanIntake = `-- name: GetVenuePlanIntake :one
SELECT ` + venuePlanIntakeColumns + `
FROM   venue_plan_intakes i
WHERE  i.id = $1 AND i.org_id = $2`

// GetVenuePlanIntake reads an intake of one organization; an intake of
// another organization answers pgx.ErrNoRows.
func (q *Queries) GetVenuePlanIntake(ctx context.Context, id, orgID uuid.UUID) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, getVenuePlanIntake, id, orgID))
}

const getVenuePlanIntakeByID = `-- name: GetVenuePlanIntakeByID :one
SELECT ` + venuePlanIntakeColumns + `
FROM   venue_plan_intakes i
WHERE  i.id = $1`

// GetVenuePlanIntakeByID reads an intake of any organization (administrator
// routes and worker jobs only).
func (q *Queries) GetVenuePlanIntakeByID(ctx context.Context, id uuid.UUID) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, getVenuePlanIntakeByID, id))
}

const getVenuePlanIntakeByIdempotencyKey = `-- name: GetVenuePlanIntakeByIdempotencyKey :one
SELECT ` + venuePlanIntakeColumns + `
FROM   venue_plan_intakes i
WHERE  i.org_id = $1 AND i.idempotency_key = $2`

// GetVenuePlanIntakeByIdempotencyKey reads the intake an earlier upload with
// the same Idempotency-Key made.
func (q *Queries) GetVenuePlanIntakeByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, getVenuePlanIntakeByIdempotencyKey, orgID, key))
}

const lockVenuePlanIntake = `-- name: LockVenuePlanIntake :one
SELECT ` + venuePlanIntakeColumns + `
FROM   venue_plan_intakes i
WHERE  i.id = $1
FOR UPDATE`

// LockVenuePlanIntake reads an intake and locks its row for the rest of the
// transaction: every change of an intake (state, draft, answers) runs under
// it. The caller checks the organization.
func (q *Queries) LockVenuePlanIntake(ctx context.Context, id uuid.UUID) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, lockVenuePlanIntake, id))
}

const listVenuePlanIntakesByOrg = `-- name: ListVenuePlanIntakesByOrg :many
SELECT ` + venuePlanIntakeColumns + `
FROM   venue_plan_intakes i
WHERE  i.org_id = $1
  AND  ($2::text IS NULL OR i.state = $2)
  AND  ($3::uuid IS NULL OR i.venue_id = $3)
ORDER  BY i.created_at DESC, i.id DESC
LIMIT  $4`

// ListVenuePlanIntakesByOrg lists the intakes of an organization, newest
// first, optionally of one state and one venue.
func (q *Queries) ListVenuePlanIntakesByOrg(ctx context.Context, orgID uuid.UUID, state *string, venueID *uuid.UUID, limit int32) ([]VenuePlanIntakeRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanIntakesByOrg, orgID, state, venueID, limit)
	return collectRows(rows, err, scanVenuePlanIntake)
}

const listVenuePlanIntakesAdmin = `-- name: ListVenuePlanIntakesAdmin :many
SELECT ` + venuePlanIntakeColumns + `,
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
LIMIT  $4`

// ListVenuePlanIntakesAdmin lists the intakes of every organization for the
// administrators, newest first: optionally of one state, of one
// organization, and only those nobody took yet (unclaimed). Each row carries
// the organization's name, the venue's name and its city (English name, else
// the city slug).
func (q *Queries) ListVenuePlanIntakesAdmin(ctx context.Context, state *string, orgID *uuid.UUID, unclaimed bool, limit int32) ([]VenuePlanIntakeAdminRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanIntakesAdmin, state, orgID, unclaimed, limit)
	return collectRows(rows, err, func(row interface{ Scan(...any) error }) (VenuePlanIntakeAdminRow, error) {
		var r VenuePlanIntakeAdminRow
		dest := append(venuePlanIntakeDest(&r.VenuePlanIntakeRow), &r.OrgName, &r.VenueName, &r.VenueCity)
		err := row.Scan(dest...)
		return r, err
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Children of an intake
// ─────────────────────────────────────────────────────────────────────────────

const listVenuePlanIntakeSources = `-- name: ListVenuePlanIntakeSources :many
SELECT ` + venuePlanSourceColumns + `
FROM   venue_plan_intake_sources s
WHERE  s.intake_id = $1
ORDER  BY s.position`

// ListVenuePlanIntakeSources lists the files of an intake in upload order.
func (q *Queries) ListVenuePlanIntakeSources(ctx context.Context, intakeID uuid.UUID) ([]VenuePlanIntakeSourceRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanIntakeSources, intakeID)
	return collectRows(rows, err, scanVenuePlanSource)
}

const getVenuePlanIntakeSource = `-- name: GetVenuePlanIntakeSource :one
SELECT ` + venuePlanSourceColumns + `
FROM   venue_plan_intake_sources s
WHERE  s.id = $1 AND s.intake_id = $2`

// GetVenuePlanIntakeSource reads one file of an intake; a file of another
// intake answers pgx.ErrNoRows.
func (q *Queries) GetVenuePlanIntakeSource(ctx context.Context, id, intakeID uuid.UUID) (VenuePlanIntakeSourceRow, error) {
	return scanVenuePlanSource(q.db.QueryRow(ctx, getVenuePlanIntakeSource, id, intakeID))
}

const listVenuePlanDrafts = `-- name: ListVenuePlanDrafts :many
SELECT ` + venuePlanDraftColumns + `
FROM   venue_plan_drafts d
WHERE  d.intake_id = $1
ORDER  BY d.n`

// ListVenuePlanDrafts lists the drafts of an intake, oldest first.
func (q *Queries) ListVenuePlanDrafts(ctx context.Context, intakeID uuid.UUID) ([]VenuePlanDraftRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanDrafts, intakeID)
	return collectRows(rows, err, scanVenuePlanDraft)
}

const getVenuePlanDraft = `-- name: GetVenuePlanDraft :one
SELECT ` + venuePlanDraftColumns + `
FROM   venue_plan_drafts d
WHERE  d.intake_id = $1 AND d.n = $2`

// GetVenuePlanDraft reads draft n of an intake.
func (q *Queries) GetVenuePlanDraft(ctx context.Context, intakeID uuid.UUID, n int32) (VenuePlanDraftRow, error) {
	return scanVenuePlanDraft(q.db.QueryRow(ctx, getVenuePlanDraft, intakeID, n))
}

const listVenuePlanQuestions = `-- name: ListVenuePlanQuestions :many
SELECT ` + venuePlanQuestionColumns + `
FROM   venue_plan_questions q
JOIN   venue_plan_drafts d ON d.id = q.draft_id
WHERE  d.intake_id = $1
ORDER  BY d.n, q.created_at, q.id`

// ListVenuePlanQuestions lists the questions of every draft of an intake,
// by draft, then in the order they were asked.
func (q *Queries) ListVenuePlanQuestions(ctx context.Context, intakeID uuid.UUID) ([]VenuePlanQuestionRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanQuestions, intakeID)
	return collectRows(rows, err, scanVenuePlanQuestion)
}

const listVenuePlanCorrections = `-- name: ListVenuePlanCorrections :many
SELECT ` + venuePlanCorrectionColumns + `
FROM   venue_plan_corrections c
WHERE  c.intake_id = $1
ORDER  BY c.created_at, c.id`

// ListVenuePlanCorrections lists the corrections of an intake, oldest first.
func (q *Queries) ListVenuePlanCorrections(ctx context.Context, intakeID uuid.UUID) ([]VenuePlanCorrectionRow, error) {
	rows, err := q.db.Query(ctx, listVenuePlanCorrections, intakeID)
	return collectRows(rows, err, scanVenuePlanCorrection)
}

// ─────────────────────────────────────────────────────────────────────────────
// Intake writes
// ─────────────────────────────────────────────────────────────────────────────

const updateVenuePlanIntakeState = `-- name: UpdateVenuePlanIntakeState :one
UPDATE venue_plan_intakes AS i
SET    state             = $3,
       escalation_reason = COALESCE($4, i.escalation_reason),
       escalation_text   = COALESCE($5, i.escalation_text),
       reject_reason     = COALESCE($6, i.reject_reason),
       return_note       = COALESCE($7, i.return_note),
       updated_at        = now(),
       closed_at         = CASE WHEN $3::text IN ('approved', 'rejected', 'cancelled') THEN now() END
WHERE  i.id = $1 AND i.state = $2
RETURNING ` + venuePlanIntakeColumns

// UpdateVenuePlanIntakeStateParams move an intake From one state To another.
// A nil text keeps the stored one.
type UpdateVenuePlanIntakeStateParams struct {
	ID               uuid.UUID
	From             string
	To               string
	EscalationReason *string
	EscalationText   *string
	RejectReason     *string
	ReturnNote       *string
}

// UpdateVenuePlanIntakeState moves an intake from p.From to p.To and sets
// closed_at when p.To is final. An intake no longer in p.From answers
// pgx.ErrNoRows, so two concurrent moves cannot both win. The caller checks
// venueplan.CanTransition; approval goes through ApproveVenuePlanIntake.
func (q *Queries) UpdateVenuePlanIntakeState(ctx context.Context, p UpdateVenuePlanIntakeStateParams) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, updateVenuePlanIntakeState,
		p.ID, p.From, p.To, p.EscalationReason, p.EscalationText, p.RejectReason, p.ReturnNote))
}

const claimVenuePlanIntake = `-- name: ClaimVenuePlanIntake :one
UPDATE venue_plan_intakes AS i
SET    operator_id = $2, operator_claimed_at = now(), updated_at = now()
WHERE  i.id = $1 AND i.closed_at IS NULL
RETURNING ` + venuePlanIntakeColumns

// ClaimVenuePlanIntake records that an administrator took the intake (a
// later claim by another administrator takes it over). A closed intake
// answers pgx.ErrNoRows.
func (q *Queries) ClaimVenuePlanIntake(ctx context.Context, id, operatorID uuid.UUID) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, claimVenuePlanIntake, id, operatorID))
}

const setVenuePlanIntakeCurrentDraft = `-- name: SetVenuePlanIntakeCurrentDraft :one
UPDATE venue_plan_intakes AS i
SET    current_draft = $2, updated_at = now()
WHERE  i.id = $1 AND i.closed_at IS NULL
  AND  EXISTS (SELECT 1 FROM venue_plan_drafts d WHERE d.intake_id = i.id AND d.n = $2)
RETURNING ` + venuePlanIntakeColumns

// SetVenuePlanIntakeCurrentDraft makes draft n the one the organizer sees. A
// closed intake, or an n that is not a draft of the intake, answers
// pgx.ErrNoRows.
func (q *Queries) SetVenuePlanIntakeCurrentDraft(ctx context.Context, id uuid.UUID, n int32) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, setVenuePlanIntakeCurrentDraft, id, n))
}

const answerVenuePlanQuestion = `-- name: AnswerVenuePlanQuestion :execrows
UPDATE venue_plan_questions
SET    answer = $3, answered_by = $4, answered_at = now()
WHERE  id = $1 AND draft_id = $2`

// AnswerVenuePlanQuestion stores the answer to a question of draft draftID
// (a later answer replaces an earlier one) and reports how many rows
// changed: 0 means the question is not one of that draft.
func (q *Queries) AnswerVenuePlanQuestion(ctx context.Context, questionID, draftID uuid.UUID, answer json.RawMessage, answeredBy *uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, answerVenuePlanQuestion, questionID, draftID, answer, answeredBy)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const approveVenuePlanIntake = `-- name: ApproveVenuePlanIntake :one
UPDATE venue_plan_intakes AS i
SET    state = 'approved', plan_id = $3, plan_version = $4,
       updated_at = now(), closed_at = now()
WHERE  i.id = $1 AND i.state = $2
RETURNING ` + venuePlanIntakeColumns

// ApproveVenuePlanIntake closes an intake as approved with the seating plan
// and version made from its draft. An intake no longer in state from answers
// pgx.ErrNoRows.
func (q *Queries) ApproveVenuePlanIntake(ctx context.Context, id uuid.UUID, from string, planID uuid.UUID, planVersion int32) (VenuePlanIntakeRow, error) {
	return scanVenuePlanIntake(q.db.QueryRow(ctx, approveVenuePlanIntake, id, from, planID, planVersion))
}

// ─────────────────────────────────────────────────────────────────────────────
// Notices and notifications
// ─────────────────────────────────────────────────────────────────────────────

// VenuePlanNoticeClaim is a notice the bot has to send: the intake it is
// about, and the live Telegram chats of the person who sent the intake
// (none when that person has no linked chat).
type VenuePlanNoticeClaim struct {
	ID           uuid.UUID `json:"id"`
	IntakeID     uuid.UUID `json:"intake_id"`
	Kind         string    `json:"kind"`
	Note         *string   `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
	OrgID        uuid.UUID `json:"org_id"`
	VenueID      uuid.UUID `json:"venue_id"`
	VenueName    string    `json:"venue_name"`
	CurrentDraft *int32    `json:"current_draft"`
	ChatIDs      []int64   `json:"chat_ids"`
	// DraftChanged is true for a returned notice whose current draft an
	// administrator uploaded after taking the intake.
	DraftChanged bool `json:"draft_changed"`
}

const claimVenuePlanNotices = `-- name: ClaimVenuePlanNotices :many
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
                 WHERE  l.user_id = i.created_by AND l.revoked_at IS NULL), '{}'::bigint[]),
       (c.kind = 'returned' AND i.operator_claimed_at IS NOT NULL AND EXISTS (
            SELECT 1 FROM venue_plan_drafts d
            WHERE  d.intake_id = i.id AND d.n = i.current_draft
              AND  d.author IN ('operator', 'operator_svg')
              AND  d.created_at >= i.operator_claimed_at)) AS draft_changed
FROM   claimed c
JOIN   venue_plan_intakes i ON i.id = c.intake_id
JOIN   venues v             ON v.id = i.venue_id
ORDER  BY c.created_at, c.id`

// ClaimVenuePlanNotices marks up to limit unclaimed notices as claimed and
// returns them, oldest first, with the creator's live Telegram chats. A
// notice is claimed at most once: a crash between the claim and the message
// loses one message, never repeats it (like ClaimWebLoginNotices).
func (q *Queries) ClaimVenuePlanNotices(ctx context.Context, limit int32) ([]VenuePlanNoticeClaim, error) {
	rows, err := q.db.Query(ctx, claimVenuePlanNotices, limit)
	return collectRows(rows, err, func(row interface{ Scan(...any) error }) (VenuePlanNoticeClaim, error) {
		var r VenuePlanNoticeClaim
		err := row.Scan(&r.ID, &r.IntakeID, &r.Kind, &r.Note, &r.CreatedAt,
			&r.OrgID, &r.VenueID, &r.VenueName, &r.CurrentDraft, &r.ChatIDs, &r.DraftChanged)
		return r, err
	})
}

const insertVenuePlanNotifyLog = `-- name: InsertVenuePlanNotifyLog :one
INSERT INTO venue_plan_notify_log (subject_id, notify_key)
VALUES ($1, $2)
ON CONFLICT DO NOTHING
RETURNING subject_id`

// InsertVenuePlanNotifyLog records that the notification key of a subject (an
// intake or a venue) was sent and reports whether it is new: false means it
// was already sent and must not be sent again.
func (q *Queries) InsertVenuePlanNotifyLog(ctx context.Context, subjectID uuid.UUID, key string) (bool, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertVenuePlanNotifyLog, subjectID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Seating plan verification
// ─────────────────────────────────────────────────────────────────────────────

const setSeatingPlanVerification = `-- name: SetSeatingPlanVerification :execrows
UPDATE seating_plans
SET    verification_state = $2,
       verification_note  = $3,
       verified_at        = CASE WHEN $2::text = 'verified' THEN now() END,
       verified_by        = CASE WHEN $2::text = 'verified' THEN $4::uuid END,
       visibility         = COALESCE($5, visibility),
       updated_at         = now()
WHERE  id = $1 AND deleted_at IS NULL`

// SetSeatingPlanVerificationParams set the verification of a plan. State is
// a venueplan.VerificationState; Note is stored as given (nil clears it);
// VerifiedBy is recorded only for verified; a non-nil Visibility replaces the
// plan's (operator_verified when verified).
type SetSeatingPlanVerificationParams struct {
	PlanID     uuid.UUID
	State      string
	Note       *string
	VerifiedBy *uuid.UUID
	Visibility *string
}

// SetSeatingPlanVerification sets the verification state of a live plan and
// reports how many rows changed (0: no such plan, or it was deleted).
// verified_at and verified_by are set for verified and cleared otherwise.
func (q *Queries) SetSeatingPlanVerification(ctx context.Context, p SetSeatingPlanVerificationParams) (int64, error) {
	tag, err := q.db.Exec(ctx, setSeatingPlanVerification, p.PlanID, p.State, p.Note, p.VerifiedBy, p.Visibility)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SeatingPlanVerificationRow is a plan of the verification queue: the plan,
// its organization and venue names, and the capacity of its current version
// (nil without a version).
type SeatingPlanVerificationRow struct {
	ID                uuid.UUID  `json:"id"`
	Name              string     `json:"name"`
	PlanType          string     `json:"plan_type"`
	Visibility        string     `json:"visibility"`
	Status            string     `json:"status"`
	OwnerOrgID        uuid.UUID  `json:"owner_org_id"`
	OrgName           string     `json:"org_name"`
	VenueID           uuid.UUID  `json:"venue_id"`
	VenueName         string     `json:"venue_name"`
	VerificationState *string    `json:"verification_state"`
	VerificationNote  *string    `json:"verification_note"`
	VerifiedAt        *time.Time `json:"verified_at"`
	VerifiedBy        *uuid.UUID `json:"verified_by"`
	CurrentVersionID  *uuid.UUID `json:"current_version_id"`
	VersionNumber     *int32     `json:"version_number"`
	CapacitySeated    *int32     `json:"capacity_seated"`
	CapacityStanding  *int32     `json:"capacity_standing"`
	CreatedAt         time.Time  `json:"created_at"`
}

const seatingPlanVerificationSelect = `SELECT p.id, p.name, p.plan_type, p.visibility, p.status, p.owner_org_id, o.name,
       p.venue_id, v.name, p.verification_state, p.verification_note, p.verified_at, p.verified_by,
       p.current_version_id, sv.version_number, sv.capacity_seated, sv.capacity_standing, p.created_at
FROM   seating_plans p
JOIN   organizations o ON o.id = p.owner_org_id
JOIN   venues v        ON v.id = p.venue_id
LEFT JOIN seating_plan_versions sv ON sv.id = p.current_version_id`

func scanSeatingPlanVerification(row interface{ Scan(...any) error }) (SeatingPlanVerificationRow, error) {
	var r SeatingPlanVerificationRow
	err := row.Scan(&r.ID, &r.Name, &r.PlanType, &r.Visibility, &r.Status, &r.OwnerOrgID, &r.OrgName,
		&r.VenueID, &r.VenueName, &r.VerificationState, &r.VerificationNote, &r.VerifiedAt, &r.VerifiedBy,
		&r.CurrentVersionID, &r.VersionNumber, &r.CapacitySeated, &r.CapacityStanding, &r.CreatedAt)
	return r, err
}

const listSeatingPlansByVerificationState = `-- name: ListSeatingPlansByVerificationState :many
` + seatingPlanVerificationSelect + `
WHERE  p.verification_state = $1 AND p.deleted_at IS NULL
ORDER  BY p.created_at, p.id
LIMIT  $2`

// ListSeatingPlansByVerificationState lists the live plans in one
// verification state, oldest first (the superadmin's queue for unverified).
func (q *Queries) ListSeatingPlansByVerificationState(ctx context.Context, state string, limit int32) ([]SeatingPlanVerificationRow, error) {
	rows, err := q.db.Query(ctx, listSeatingPlansByVerificationState, state, limit)
	return collectRows(rows, err, scanSeatingPlanVerification)
}

const listVerifiedSeatingPlansOfVenue = `-- name: ListVerifiedSeatingPlansOfVenue :many
` + seatingPlanVerificationSelect + `
WHERE  p.venue_id = $1
  AND  p.visibility = 'operator_verified'
  AND  p.verification_state = 'verified'
  AND  p.status = 'active'
  AND  p.deleted_at IS NULL
  AND  p.current_version_id IS NOT NULL
ORDER  BY p.name, p.id`

// ListVerifiedSeatingPlansOfVenue lists the active plans of a venue that are
// operator_verified AND in verification state verified and that have a
// current version: the templates another organization may fork. A plan that
// carries operator_verified with a NULL state is not listed.
func (q *Queries) ListVerifiedSeatingPlansOfVenue(ctx context.Context, venueID uuid.UUID) ([]SeatingPlanVerificationRow, error) {
	rows, err := q.db.Query(ctx, listVerifiedSeatingPlansOfVenue, venueID)
	return collectRows(rows, err, scanSeatingPlanVerification)
}
