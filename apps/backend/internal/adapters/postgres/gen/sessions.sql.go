// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: sessions.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SessionRow — shared result type for all sessions queries
// ─────────────────────────────────────────────────────────────────────────────

// SessionRow is the result type returned by all sessions queries.
// deleted_at is nil for active sessions and non-nil for soft-deleted ones.
// capacity_total is always positive (> 0) per the table CHECK constraint and
// is a DERIVED value (bound plan version -> capacity_override ->
// venues.capacity_default, AB-36). venue_id is NOT NULL — every session has
// a venue (AB-36). currency is the ISO 4217 code every price of the session
// is denominated in; currency_source records whether it was 'derived' from
// the venue geography or set as an 'override' (AB-38).
// status follows the lifecycle: draft → scheduled → completed|cancelled.
type SessionRow struct {
	ID                   uuid.UUID  `json:"id"`
	EventID              uuid.UUID  `json:"event_id"`
	VenueID              uuid.UUID  `json:"venue_id"`
	StartAt              time.Time  `json:"start_at"`
	EndAt                time.Time  `json:"end_at"`
	CapacityTotal        int32      `json:"capacity_total"`
	CapacityOverride     *int32     `json:"capacity_override"`
	Status               string     `json:"status"`
	AdmissionMode        string     `json:"admission_mode"`
	SeatingPlanVersionID *uuid.UUID `json:"seating_plan_version_id"`
	PosterMediaID        *uuid.UUID `json:"poster_media_id"`
	Currency             string     `json:"currency"`
	CurrencySource       string     `json:"currency_source"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	DeletedAt            *time.Time `json:"deleted_at"`
}

// scanSessionRow scans a single sessions row into a SessionRow.
func scanSessionRow(row interface {
	Scan(dest ...any) error
}) (SessionRow, error) {
	var s SessionRow
	err := row.Scan(
		&s.ID,
		&s.EventID,
		&s.VenueID,
		&s.StartAt,
		&s.EndAt,
		&s.CapacityTotal,
		&s.CapacityOverride,
		&s.Status,
		&s.AdmissionMode,
		&s.SeatingPlanVersionID,
		&s.PosterMediaID,
		&s.Currency,
		&s.CurrencySource,
		&s.CreatedAt,
		&s.UpdatedAt,
		&s.DeletedAt,
	)
	return s, err
}

// sessionColumns is the canonical RETURNING / SELECT column list shared by
// every query that yields a full SessionRow.
const sessionColumns = `id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, poster_media_id, currency, currency_source, created_at, updated_at, deleted_at`

// ─────────────────────────────────────────────────────────────────────────────
// InsertSession
// ─────────────────────────────────────────────────────────────────────────────

const insertSession = `-- name: InsertSession :one
INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, poster_media_id, currency, currency_source)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7, ''), 'scheduled'), $8, $9, $10)
RETURNING ` + sessionColumns

// InsertSession creates a new session for the given event at the given venue.
// status defaults to 'scheduled' when empty. capacityTotal is the resolved
// derived capacity; capacityOverride is the operator knob it may have come
// from (nil when not supplied). currency / currencySource are resolved by the
// handler before the insert (AB-38). posterMediaID is the optional session-level
// poster artwork (AB-47).
// Returns the created row including the uuidv7 PK assigned by the database.
func (q *Queries) InsertSession(ctx context.Context, eventID, venueID uuid.UUID, startAt, endAt time.Time, capacityTotal int32, capacityOverride *int32, status string, posterMediaID *uuid.UUID, currency, currencySource string) (SessionRow, error) {
	row := q.db.QueryRow(ctx, insertSession,
		eventID, venueID, startAt, endAt, capacityTotal, capacityOverride, status, posterMediaID, currency, currencySource,
	)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionByID
// ─────────────────────────────────────────────────────────────────────────────

const getSessionByID = `-- name: GetSessionByID :one
SELECT ` + sessionColumns + `
FROM   sessions
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL`

// GetSessionByID fetches an active session by its UUID primary key scoped to the event.
// Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different event.
func (q *Queries) GetSessionByID(ctx context.Context, id, eventID uuid.UUID) (SessionRow, error) {
	row := q.db.QueryRow(ctx, getSessionByID, id, eventID)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionsByEvent
// ─────────────────────────────────────────────────────────────────────────────

const listSessionsByEvent = `-- name: ListSessionsByEvent :many
SELECT ` + sessionColumns + `
FROM   sessions
WHERE  event_id = $1
  AND  deleted_at IS NULL
ORDER BY start_at ASC, id ASC`

// ListSessionsByEvent returns all active sessions for the given event.
// Ordered by start_at ASC so the earliest session is first.
func (q *Queries) ListSessionsByEvent(ctx context.Context, eventID uuid.UUID) ([]SessionRow, error) {
	rows, err := q.db.Query(ctx, listSessionsByEvent, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionRow
	for rows.Next() {
		s, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateSession
// ─────────────────────────────────────────────────────────────────────────────

const updateSession = `-- name: UpdateSession :one
UPDATE sessions
SET    venue_id          = CASE WHEN $3::uuid        IS NOT NULL THEN $3::uuid        ELSE venue_id END,
       start_at          = CASE WHEN $4::timestamptz IS NOT NULL THEN $4::timestamptz ELSE start_at END,
       end_at            = CASE WHEN $5::timestamptz IS NOT NULL THEN $5::timestamptz ELSE end_at END,
       capacity_total    = CASE WHEN $6::integer     IS NOT NULL THEN $6::integer     ELSE capacity_total END,
       capacity_override = CASE WHEN $7::integer     IS NOT NULL THEN $7::integer     ELSE capacity_override END,
       status            = COALESCE(NULLIF($8, ''), status),
       poster_media_id   = CASE WHEN $9::uuid        IS NOT NULL THEN $9::uuid        ELSE poster_media_id END,
       currency          = CASE WHEN $10::text       IS NOT NULL THEN $10::text       ELSE currency END,
       currency_source   = COALESCE(NULLIF($11, ''), currency_source),
       updated_at        = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING ` + sessionColumns

// UpdateSession applies a partial update to an active session scoped by event_id.
// Nil optional fields keep the existing values. capacityTotal carries the
// re-derived capacity when the handler recomputed it. Setting currency
// cascades to the session's ticket_tiers via the composite FK
// ticket_tiers_currency_matches_session (ON UPDATE CASCADE), so tiers can
// never diverge from the session currency. posterMediaID is the optional
// session-level poster artwork (AB-47).
// Returns pgx.ErrNoRows when the session does not exist, belongs to a different
// event, or has been soft-deleted.
func (q *Queries) UpdateSession(ctx context.Context, id, eventID uuid.UUID, venueID *uuid.UUID, startAt, endAt *time.Time, capacityTotal, capacityOverride *int32, status string, posterMediaID *uuid.UUID, currency *string, currencySource string) (SessionRow, error) {
	row := q.db.QueryRow(ctx, updateSession,
		id, eventID, venueID, startAt, endAt, capacityTotal, capacityOverride, status, posterMediaID, currency, currencySource,
	)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SoftDeleteSession
// ─────────────────────────────────────────────────────────────────────────────

const softDeleteSession = `-- name: SoftDeleteSession :one
UPDATE sessions
SET    deleted_at = now(),
       updated_at = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING ` + sessionColumns

// SoftDeleteSession marks a session as deleted by setting deleted_at.
// Scoped by event_id to enforce owner-gated mutation policy.
// Returns pgx.ErrNoRows when the session does not exist, belongs to a different
// event, or has already been deleted.
func (q *Queries) SoftDeleteSession(ctx context.Context, id, eventID uuid.UUID) (SessionRow, error) {
	row := q.db.QueryRow(ctx, softDeleteSession, id, eventID)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SessionSeatingBindingRow — narrow result shape for the seating-binding
// endpoint (feature #306, Wave SEAT-B2).
//
// Kept separate from SessionRow so the plain sessions CRUD queries remain
// backward-compatible and callers reading the seating columns get a shape
// that only surfaces those fields. AdmissionMode is one of
// 'general_admission'|'assigned_seats'|'hybrid' (§5.1 constraint).
// SeatingPlanVersionID is non-nil only when a seated version has been bound.
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatingBindingRow is the projection returned by
// GetSessionSeatingBinding and BindSessionSeatingPlan.
type SessionSeatingBindingRow struct {
	ID                   uuid.UUID  `json:"id"`
	EventID              uuid.UUID  `json:"event_id"`
	AdmissionMode        string     `json:"admission_mode"`
	SeatingPlanVersionID *uuid.UUID `json:"seating_plan_version_id"`
	SeatStatusVersion    int64      `json:"seat_status_version"`
	CapacityTotal        int32      `json:"capacity_total"`
}

// scanSessionSeatingBindingRow scans a single sessions row projection.
func scanSessionSeatingBindingRow(row interface {
	Scan(dest ...any) error
}) (SessionSeatingBindingRow, error) {
	var s SessionSeatingBindingRow
	err := row.Scan(
		&s.ID,
		&s.EventID,
		&s.AdmissionMode,
		&s.SeatingPlanVersionID,
		&s.SeatStatusVersion,
		&s.CapacityTotal,
	)
	return s, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatingBinding
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatingBinding = `-- name: GetSessionSeatingBinding :one
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL`

// GetSessionSeatingBinding fetches the seating-related columns for a session
// scoped by event. Returns pgx.ErrNoRows when not found, already soft-deleted,
// or belongs to a different event. Used by the bind endpoint (feature #306,
// Wave SEAT-B2) to decide first-bind vs rebind.
func (q *Queries) GetSessionSeatingBinding(ctx context.Context, id, eventID uuid.UUID) (SessionSeatingBindingRow, error) {
	row := q.db.QueryRow(ctx, getSessionSeatingBinding, id, eventID)
	return scanSessionSeatingBindingRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatingBindingForUpdate
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatingBindingForUpdate = `-- name: GetSessionSeatingBindingForUpdate :one
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL
FOR UPDATE`

// GetSessionSeatingBindingForUpdate is the row-locking variant of
// GetSessionSeatingBinding. Taken at the top of the seating-bind transaction
// (feature #306, Wave SEAT-B2) so binds serialize against any concurrent
// transaction that mutates the session's seat inventory — the seated
// reservation path locks the same sessions row via
// IncrementSessionSeatStatusVersion, which closes the TOCTOU window between
// the rebind zero-reservations check and the session_seats wipe. MUST be
// called inside a transaction; the lock releases on commit / rollback.
func (q *Queries) GetSessionSeatingBindingForUpdate(ctx context.Context, id, eventID uuid.UUID) (SessionSeatingBindingRow, error) {
	row := q.db.QueryRow(ctx, getSessionSeatingBindingForUpdate, id, eventID)
	return scanSessionSeatingBindingRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// BindSessionSeatingPlan
// ─────────────────────────────────────────────────────────────────────────────

const bindSessionSeatingPlan = `-- name: BindSessionSeatingPlan :one
UPDATE sessions
SET    admission_mode          = $3,
       seating_plan_version_id = $4,
       capacity_total          = $5,
       updated_at              = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, admission_mode, seating_plan_version_id,
          seat_status_version, capacity_total`

// BindSessionSeatingPlan flips a session onto the (admissionMode, planVersionID)
// tuple and recomputes capacity_total from the seat count computed by the
// caller. seat_status_version is left untouched — bind is a metadata change,
// not a seat-status transition. Returns pgx.ErrNoRows when the session does
// not exist, belongs to a different event, or has been soft-deleted.
func (q *Queries) BindSessionSeatingPlan(
	ctx context.Context,
	id, eventID uuid.UUID,
	admissionMode string,
	planVersionID *uuid.UUID,
	capacityTotal int32,
) (SessionSeatingBindingRow, error) {
	row := q.db.QueryRow(ctx, bindSessionSeatingPlan,
		id, eventID, admissionMode, planVersionID, capacityTotal,
	)
	return scanSessionSeatingBindingRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountOverlappingSessions
// ─────────────────────────────────────────────────────────────────────────────

const countOverlappingSessions = `-- name: CountOverlappingSessions :one
SELECT COUNT(*)::int
FROM   sessions
WHERE  event_id    = $1
  AND  id         <> $2
  AND  deleted_at  IS NULL
  AND  start_at    < $4
  AND  end_at      > $3`

// CountOverlappingSessions counts active sessions for the given event whose
// time range overlaps with [startAt, endAt). The session with id=excludeID
// is excluded so an update can check against its siblings without counting itself.
// Pass uuid.Nil as excludeID when creating a new session (no exclusion needed,
// since all existing IDs are uuidv7 and will never equal uuid.Nil).
func (q *Queries) CountOverlappingSessions(ctx context.Context, eventID, excludeID uuid.UUID, startAt, endAt time.Time) (int32, error) {
	row := q.db.QueryRow(ctx, countOverlappingSessions, eventID, excludeID, startAt, endAt)
	var count int32
	err := row.Scan(&count)
	return count, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionOrgContext
// ─────────────────────────────────────────────────────────────────────────────

// SessionOrgContextRow is the narrow projection returned by
// GetSessionOrgContext: the session id and the owning organization resolved
// via the sessions → events join.
type SessionOrgContextRow struct {
	SessionID uuid.UUID `json:"session_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

const getSessionOrgContext = `-- name: GetSessionOrgContext :one
SELECT s.id AS session_id, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id         = $1
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL`

// GetSessionOrgContext resolves the owning organization of a session by
// walking the sessions → events join. Used by the Bil24 compatibility
// gateway (RESERVATION wiring) to anchor a reservation to the correct
// tenant. Returns pgx.ErrNoRows when the session or its event does not
// exist or has been soft-deleted.
func (q *Queries) GetSessionOrgContext(ctx context.Context, sessionID uuid.UUID) (SessionOrgContextRow, error) {
	row := q.db.QueryRow(ctx, getSessionOrgContext, sessionID)
	var r SessionOrgContextRow
	err := row.Scan(&r.SessionID, &r.OrgID)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionCurrency
// ─────────────────────────────────────────────────────────────────────────────

const getSessionCurrency = `-- name: GetSessionCurrency :one
SELECT trim(currency)::text AS currency
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetSessionCurrency returns the ISO 4217 currency of an active session.
// Used by the seating bind's tier auto-creation and by the ticket-tier
// create/update path so every tier is denominated in the session currency
// (AB-38 — one currency per session). Returns pgx.ErrNoRows when the
// session does not exist or is soft-deleted.
func (q *Queries) GetSessionCurrency(ctx context.Context, id uuid.UUID) (string, error) {
	row := q.db.QueryRow(ctx, getSessionCurrency, id)
	var currency string
	err := row.Scan(&currency)
	return currency, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionEventID
// ─────────────────────────────────────────────────────────────────────────────

const getSessionEventID = `-- name: GetSessionEventID :one
SELECT event_id
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetSessionEventID returns the owning event of a session. Order creation
// needs orders.event_id but reservations only carry session_id, so the
// checkout confirm and public-feed paths resolve it through this one-column
// lookup (W1-A6c, feature #488). Returns pgx.ErrNoRows when the session does
// not exist or is soft-deleted.
func (q *Queries) GetSessionEventID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	row := q.db.QueryRow(ctx, getSessionEventID, id)
	var eventID uuid.UUID
	err := row.Scan(&eventID)
	return eventID, err
}
