// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: seat_layouts.sql (task S1d, migration 0171)
//
// Storage of saved seat layouts, the allocator annotations of a seating plan
// and the colour of a session category. None of the writes here touches
// session_seats: applying a layout is seatlayout.ApplyLayout, which goes
// through the seat batch (sessions row locked first).
//
// ticket_tiers.color is read and written by queries of its own on purpose:
// TicketTierRow is fed by a dozen SELECT lists in several files and widening
// it would break every one of them at once (AGENTS.md, scan helpers).

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SeatLayoutRow is one row of seat_layouts. Payload is the raw jsonb;
// seatlayout.LayoutPayload owns its shape.
type SeatLayoutRow struct {
	ID             uuid.UUID       `json:"id"`
	OrgID          uuid.UUID       `json:"org_id"`
	SeatingPlanID  uuid.UUID       `json:"seating_plan_id"`
	Name           string          `json:"name"`
	Source         string          `json:"source"`
	Algorithm      *string         `json:"algorithm"`
	Params         json.RawMessage `json:"params"`
	Payload        json.RawMessage `json:"payload"`
	CreatedBy      *uuid.UUID      `json:"created_by"`
	CreatedByLabel string          `json:"created_by_label"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const seatLayoutColumns = `id, org_id, seating_plan_id, name, source, algorithm, params,
       payload, created_by, created_by_label, created_at, updated_at`

func scanSeatLayoutRow(row interface {
	Scan(dest ...any) error
}) (SeatLayoutRow, error) {
	var l SeatLayoutRow
	var params, payload []byte
	err := row.Scan(
		&l.ID, &l.OrgID, &l.SeatingPlanID, &l.Name, &l.Source, &l.Algorithm, &params,
		&payload, &l.CreatedBy, &l.CreatedByLabel, &l.CreatedAt, &l.UpdatedAt,
	)
	if len(params) > 0 {
		l.Params = json.RawMessage(params)
	}
	l.Payload = json.RawMessage(payload)
	return l, err
}

// ─────────────────────────────────────────────────────────────────────────────
// LockSeatLayoutsForPlan
// ─────────────────────────────────────────────────────────────────────────────

const lockSeatLayoutsForPlan = `-- name: LockSeatLayoutsForPlan :exec
SELECT pg_advisory_xact_lock(hashtextextended('seat_layouts:' || $1::text || ':' || $2::text, 0))`

// LockSeatLayoutsForPlan serializes the creators of layouts of one
// organization on one plan until the transaction ends, so the per-plan cap
// is exact. It must be the first statement of such a transaction.
func (q *Queries) LockSeatLayoutsForPlan(ctx context.Context, orgID, planID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockSeatLayoutsForPlan, orgID, planID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// CountSeatLayouts
// ─────────────────────────────────────────────────────────────────────────────

const countSeatLayouts = `-- name: CountSeatLayouts :one
SELECT count(*) FROM seat_layouts WHERE org_id = $1 AND seating_plan_id = $2`

// CountSeatLayouts counts the layouts of an organization on a plan.
func (q *Queries) CountSeatLayouts(ctx context.Context, orgID, planID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSeatLayouts, orgID, planID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSeatLayout
// ─────────────────────────────────────────────────────────────────────────────

// InsertSeatLayoutParams are the columns InsertSeatLayout writes.
type InsertSeatLayoutParams struct {
	OrgID          uuid.UUID
	SeatingPlanID  uuid.UUID
	Name           string
	Source         string
	Algorithm      *string
	Params         json.RawMessage
	Payload        json.RawMessage
	CreatedBy      *uuid.UUID
	CreatedByLabel string
}

const insertSeatLayout = `-- name: InsertSeatLayout :one
INSERT INTO seat_layouts (org_id, seating_plan_id, name, source, algorithm, params,
                          payload, created_by, created_by_label)
VALUES ($1, $2, btrim($3), $4, $5, $6::jsonb, $7::jsonb, $8, $9)
RETURNING ` + seatLayoutColumns

// InsertSeatLayout stores a layout. A name already used by the organization
// on this plan (ignoring case and outer spaces) is a unique violation on
// seat_layouts_org_plan_name_uq.
func (q *Queries) InsertSeatLayout(ctx context.Context, p InsertSeatLayoutParams) (SeatLayoutRow, error) {
	var params any
	if len(p.Params) > 0 {
		params = []byte(p.Params)
	}
	row := q.db.QueryRow(ctx, insertSeatLayout,
		p.OrgID, p.SeatingPlanID, p.Name, p.Source, p.Algorithm, params,
		[]byte(p.Payload), p.CreatedBy, p.CreatedByLabel,
	)
	return scanSeatLayoutRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSeatLayout
// ─────────────────────────────────────────────────────────────────────────────

const getSeatLayout = `-- name: GetSeatLayout :one
SELECT ` + seatLayoutColumns + `
FROM   seat_layouts
WHERE  id = $1 AND org_id = $2 AND seating_plan_id = $3`

// GetSeatLayout returns a layout of the organization on the plan; pgx.ErrNoRows
// for an unknown, foreign or other-plan id.
func (q *Queries) GetSeatLayout(ctx context.Context, id, orgID, planID uuid.UUID) (SeatLayoutRow, error) {
	return scanSeatLayoutRow(q.db.QueryRow(ctx, getSeatLayout, id, orgID, planID))
}

const getSeatLayoutByID = `-- name: GetSeatLayoutByID :one
SELECT ` + seatLayoutColumns + `
FROM   seat_layouts
WHERE  id = $1 AND org_id = $2`

// GetSeatLayoutByID returns a layout of the organization whatever its plan
// (the apply route learns the plan from the layout); pgx.ErrNoRows for an
// unknown or foreign id.
func (q *Queries) GetSeatLayoutByID(ctx context.Context, id, orgID uuid.UUID) (SeatLayoutRow, error) {
	return scanSeatLayoutRow(q.db.QueryRow(ctx, getSeatLayoutByID, id, orgID))
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSeatLayoutSummaries
// ─────────────────────────────────────────────────────────────────────────────

// SeatLayoutSummaryRow is a layout without its seat map: the slots and the
// counts are enough to draw a list.
type SeatLayoutSummaryRow struct {
	ID              uuid.UUID       `json:"id"`
	OrgID           uuid.UUID       `json:"org_id"`
	SeatingPlanID   uuid.UUID       `json:"seating_plan_id"`
	Name            string          `json:"name"`
	Source          string          `json:"source"`
	Algorithm       *string         `json:"algorithm"`
	Params          json.RawMessage `json:"params"`
	Slots           json.RawMessage `json:"slots"`
	AssignmentCount int32           `json:"assignment_count"`
	ClosedCount     int32           `json:"closed_count"`
	CreatedBy       *uuid.UUID      `json:"created_by"`
	CreatedByLabel  string          `json:"created_by_label"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const listSeatLayoutSummaries = `-- name: ListSeatLayoutSummaries :many
SELECT id, org_id, seating_plan_id, name, source, algorithm, params,
       COALESCE(payload -> 'slots', '[]'::jsonb),
       (SELECT count(*) FROM jsonb_object_keys(COALESCE(payload -> 'assignments', '{}'::jsonb)))::int,
       COALESCE(jsonb_array_length(payload -> 'closed_seat_keys'), 0),
       created_by, created_by_label, created_at, updated_at
FROM   seat_layouts
WHERE  org_id = $1 AND seating_plan_id = $2
ORDER  BY created_at DESC, id DESC`

// ListSeatLayoutSummaries lists the layouts of an organization on a plan,
// newest first. A plan holds at most seatlayout.MaxLayoutsPerPlan, so there
// is no paging.
func (q *Queries) ListSeatLayoutSummaries(ctx context.Context, orgID, planID uuid.UUID) ([]SeatLayoutSummaryRow, error) {
	rows, err := q.db.Query(ctx, listSeatLayoutSummaries, orgID, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeatLayoutSummaryRow{}
	for rows.Next() {
		var r SeatLayoutSummaryRow
		var params, slots []byte
		if err := rows.Scan(&r.ID, &r.OrgID, &r.SeatingPlanID, &r.Name, &r.Source, &r.Algorithm,
			&params, &slots, &r.AssignmentCount, &r.ClosedCount, &r.CreatedBy, &r.CreatedByLabel,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if len(params) > 0 {
			r.Params = json.RawMessage(params)
		}
		r.Slots = json.RawMessage(slots)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// RenameSeatLayout / DeleteSeatLayout
// ─────────────────────────────────────────────────────────────────────────────

const renameSeatLayout = `-- name: RenameSeatLayout :one
UPDATE seat_layouts
SET    name = btrim($4), updated_at = now()
WHERE  id = $1 AND org_id = $2 AND seating_plan_id = $3
RETURNING ` + seatLayoutColumns

// RenameSeatLayout changes the name of a layout; pgx.ErrNoRows when it is not
// the organization's on this plan, a unique violation on a taken name.
func (q *Queries) RenameSeatLayout(ctx context.Context, id, orgID, planID uuid.UUID, name string) (SeatLayoutRow, error) {
	return scanSeatLayoutRow(q.db.QueryRow(ctx, renameSeatLayout, id, orgID, planID, name))
}

const deleteSeatLayout = `-- name: DeleteSeatLayout :execrows
DELETE FROM seat_layouts WHERE id = $1 AND org_id = $2 AND seating_plan_id = $3`

// DeleteSeatLayout removes a layout and reports how many rows went (0 =
// not the organization's on this plan). A layout is derived data: sessions
// that took it keep the seats as the batch left them.
func (q *Queries) DeleteSeatLayout(ctx context.Context, id, orgID, planID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteSeatLayout, id, orgID, planID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Seating plan annotations
// ─────────────────────────────────────────────────────────────────────────────

// SeatingPlanAnnotationsRow is the plan columns the annotation routes need.
type SeatingPlanAnnotationsRow struct {
	ID               uuid.UUID       `json:"id"`
	OwnerOrgID       uuid.UUID       `json:"owner_org_id"`
	Visibility       string          `json:"visibility"`
	CurrentVersionID *uuid.UUID      `json:"current_version_id"`
	Annotations      json.RawMessage `json:"annotations"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

const getSeatingPlanAnnotations = `-- name: GetSeatingPlanAnnotations :one
SELECT id, owner_org_id, visibility, current_version_id, annotations, updated_at
FROM   seating_plans
WHERE  id = $1 AND deleted_at IS NULL`

// GetSeatingPlanAnnotations returns the plan's owner, visibility and
// annotations; pgx.ErrNoRows for an unknown or deleted plan.
func (q *Queries) GetSeatingPlanAnnotations(ctx context.Context, planID uuid.UUID) (SeatingPlanAnnotationsRow, error) {
	var r SeatingPlanAnnotationsRow
	var ann []byte
	err := q.db.QueryRow(ctx, getSeatingPlanAnnotations, planID).Scan(
		&r.ID, &r.OwnerOrgID, &r.Visibility, &r.CurrentVersionID, &ann, &r.UpdatedAt)
	r.Annotations = json.RawMessage(ann)
	return r, err
}

const setSeatingPlanAnnotations = `-- name: SetSeatingPlanAnnotations :one
UPDATE seating_plans
SET    annotations = $3::jsonb, updated_at = now()
WHERE  id = $1 AND owner_org_id = $2 AND deleted_at IS NULL
RETURNING id, owner_org_id, visibility, current_version_id, annotations, updated_at`

// SetSeatingPlanAnnotations replaces the annotations of a plan its
// organization owns; pgx.ErrNoRows otherwise. The geometry and its checksum
// are untouched.
func (q *Queries) SetSeatingPlanAnnotations(ctx context.Context, planID, ownerOrgID uuid.UUID, annotations json.RawMessage) (SeatingPlanAnnotationsRow, error) {
	var r SeatingPlanAnnotationsRow
	var ann []byte
	err := q.db.QueryRow(ctx, setSeatingPlanAnnotations, planID, ownerOrgID, []byte(annotations)).Scan(
		&r.ID, &r.OwnerOrgID, &r.Visibility, &r.CurrentVersionID, &ann, &r.UpdatedAt)
	r.Annotations = json.RawMessage(ann)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Session category colour
// ─────────────────────────────────────────────────────────────────────────────

const listSessionTierColors = `-- name: ListSessionTierColors :many
SELECT id, color FROM ticket_tiers
WHERE  session_id = $1 AND deleted_at IS NULL AND color IS NOT NULL`

// ListSessionTierColors maps the live categories of a session that have a
// colour of their own to it. A category absent from the map has none.
func (q *Queries) ListSessionTierColors(ctx context.Context, sessionID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.db.Query(ctx, listSessionTierColors, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var color string
		if err := rows.Scan(&id, &color); err != nil {
			return nil, err
		}
		out[id] = color
	}
	return out, rows.Err()
}

const getTicketTierColor = `-- name: GetTicketTierColor :one
SELECT color FROM ticket_tiers WHERE id = $1 AND session_id = $2`

// GetTicketTierColor returns the colour of one category (nil = none).
func (q *Queries) GetTicketTierColor(ctx context.Context, id, sessionID uuid.UUID) (*string, error) {
	var c *string
	err := q.db.QueryRow(ctx, getTicketTierColor, id, sessionID).Scan(&c)
	return c, err
}

const setTicketTierColor = `-- name: SetTicketTierColor :execrows
UPDATE ticket_tiers
SET    color = $3, updated_at = now()
WHERE  id = $1 AND session_id = $2 AND deleted_at IS NULL`

// SetTicketTierColor sets (or, with nil, clears) the colour of a live
// category of the session. 0 rows = not a live category of this session.
func (q *Queries) SetTicketTierColor(ctx context.Context, id, sessionID uuid.UUID, color *string) (int64, error) {
	tag, err := q.db.Exec(ctx, setTicketTierColor, id, sessionID, color)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Seats of a session as a layout
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatAssignmentRow is the category and status of one seat of a
// session.
type SessionSeatAssignmentRow struct {
	SeatKey string     `json:"seat_key"`
	TierID  *uuid.UUID `json:"tier_id"`
	Status  string     `json:"status"`
}

const listSessionSeatAssignments = `-- name: ListSessionSeatAssignments :many
SELECT seat_key, tier_id, status
FROM   session_seats
WHERE  session_id = $1 AND kind = 'seat'
ORDER  BY seat_key`

// ListSessionSeatAssignments lists the seats (not the General Admission
// places) of a session with their category and status. Used to save the
// session's current layout.
func (q *Queries) ListSessionSeatAssignments(ctx context.Context, sessionID uuid.UUID) ([]SessionSeatAssignmentRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatAssignments, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSeatAssignmentRow{}
	for rows.Next() {
		var r SessionSeatAssignmentRow
		if err := rows.Scan(&r.SeatKey, &r.TierID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getSessionPlanID = `-- name: GetSessionPlanID :one
SELECT v.seating_plan_id
FROM   sessions s
JOIN   seating_plan_versions v ON v.id = s.seating_plan_version_id
WHERE  s.id = $1`

// GetSessionPlanID returns the seating plan the session's plan version
// belongs to; pgx.ErrNoRows for a session with no plan.
func (q *Queries) GetSessionPlanID(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, getSessionPlanID, sessionID).Scan(&id)
	return id, err
}
