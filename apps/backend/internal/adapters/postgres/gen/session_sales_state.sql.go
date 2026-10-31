// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: session sales state (migration 0182, package salesstate)

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SessionSalesFactsRow is everything salesstate.Effective needs about one
// session, plus the organizer's state and when it was set.
type SessionSalesFactsRow struct {
	SessionID       uuid.UUID
	Status          string
	SalesEndAt      time.Time
	Manual          string // "soon" / "sold_out" / ""
	ManualChangedAt *time.Time
	ManualChangeID  *uuid.UUID
	Categories      int64 // live categories of the session
	TotalPlaces     int64
	FreePlaces      int64
	HeldPlaces      int64
	Selling         bool
	OpensLater      bool
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSalesFacts
// ─────────────────────────────────────────────────────────────────────────────

// The categories' rule is the hold gate's (hcheckout.CategorySellable): open
// and inside the sale window at $2. opens_later is a free place that may
// still sell: a window that has not started, or a closed category whose
// window has not ended.
const listSessionSalesFacts = `-- name: ListSessionSalesFacts :many
SELECT s.id,
       s.status,
       s.sales_end_at,
       COALESCE(st.state, ''),
       st.changed_at,
       st.change_id,
       (SELECT count(*) FROM ticket_tiers t
         WHERE t.session_id = s.id AND t.deleted_at IS NULL)::bigint,
       COALESCE(p.total, 0)::bigint,
       COALESCE(p.free, 0)::bigint,
       COALESCE(p.held, 0)::bigint,
       COALESCE(p.selling, false),
       COALESCE(p.opens_later, false)
FROM   sessions s
LEFT   JOIN session_sales_states st ON st.session_id = s.id
LEFT   JOIN LATERAL (
    SELECT count(*)                                        AS total,
           count(*) FILTER (WHERE ss.status = 'available') AS free,
           count(*) FILTER (WHERE ss.status = 'held')      AS held,
           bool_or(ss.status = 'available'
                   AND tt.is_open
                   AND (tt.sale_window_start IS NULL OR tt.sale_window_start <= $2)
                   AND (tt.sale_window_end   IS NULL OR tt.sale_window_end   >= $2)) AS selling,
           bool_or(ss.status = 'available'
                   AND (tt.sale_window_end IS NULL OR tt.sale_window_end >= $2)
                   AND ((tt.sale_window_start IS NOT NULL AND tt.sale_window_start > $2)
                        OR NOT tt.is_open))                                          AS opens_later
    FROM   session_seats ss
    JOIN   ticket_tiers  tt ON tt.id = ss.tier_id
    WHERE  ss.session_id = s.id
      AND  tt.deleted_at IS NULL
) p ON true
WHERE  s.id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL`

// ListSessionSalesFacts loads the sales facts of the given sessions in one
// round trip, keyed by session id. A deleted or unknown session is absent.
func (q *Queries) ListSessionSalesFacts(ctx context.Context, sessionIDs []uuid.UUID, now time.Time) (map[uuid.UUID]SessionSalesFactsRow, error) {
	out := make(map[uuid.UUID]SessionSalesFactsRow, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listSessionSalesFacts, sessionIDs, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r SessionSalesFactsRow
		if err := rows.Scan(
			&r.SessionID, &r.Status, &r.SalesEndAt, &r.Manual, &r.ManualChangedAt, &r.ManualChangeID,
			&r.Categories, &r.TotalPlaces, &r.FreePlaces, &r.HeldPlaces, &r.Selling, &r.OpensLater,
		); err != nil {
			return nil, err
		}
		out[r.SessionID] = r
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListManualSalesStatesByOrg
// ─────────────────────────────────────────────────────────────────────────────

const listManualSalesStatesByOrg = `-- name: ListManualSalesStatesByOrg :many
SELECT st.session_id, st.state
FROM   session_sales_states st
JOIN   sessions s ON s.id = st.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  e.org_id = $1
  AND  s.deleted_at IS NULL`

// ListManualSalesStatesByOrg returns the organizer's sales state of every
// session of the organization that has one (the gateway catalog reads it
// once per GET_ALL_ACTIONS).
func (q *Queries) ListManualSalesStatesByOrg(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.db.Query(ctx, listManualSalesStatesByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var (
			id    uuid.UUID
			state string
		)
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// LockSessionForSalesState
// ─────────────────────────────────────────────────────────────────────────────

// SessionSalesStateTarget is the session a change is about, read under a
// row lock so two concurrent changes of one session serialize.
type SessionSalesStateTarget struct {
	SessionID uuid.UUID
	EventID   uuid.UUID
	OrgID     uuid.UUID
	Status    string
	Manual    string
}

// FOR NO KEY UPDATE, not FOR UPDATE: a hold inserting a reservation that
// references the session takes FOR KEY SHARE on it and must not wait.
const lockSessionForSalesState = `-- name: LockSessionForSalesState :one
SELECT s.id, s.event_id, e.org_id, s.status, COALESCE(st.state, '')
FROM   sessions s
JOIN   events   e  ON e.id = s.event_id
LEFT   JOIN session_sales_states st ON st.session_id = s.id
WHERE  s.id = $1
  AND  s.deleted_at IS NULL
FOR NO KEY UPDATE OF s`

// LockSessionForSalesState locks the session row and returns it with its
// current organizer state. pgx.ErrNoRows for an unknown or deleted session.
func (q *Queries) LockSessionForSalesState(ctx context.Context, sessionID uuid.UUID) (SessionSalesStateTarget, error) {
	var t SessionSalesStateTarget
	err := q.db.QueryRow(ctx, lockSessionForSalesState, sessionID).Scan(
		&t.SessionID, &t.EventID, &t.OrgID, &t.Status, &t.Manual,
	)
	return t, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSalesStateOutboxEvent
// ─────────────────────────────────────────────────────────────────────────────

// The same row hscanner.PublishCatalogEvent writes (v1.session.updated with
// the canonical catalog payload), but inside the change's own transaction
// and returning its id, so the journal can tell whether the sites got it.
const insertSalesStateOutboxEvent = `-- name: InsertSalesStateOutboxEvent :one
INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
VALUES ($1, $2, $3, $4::jsonb)
RETURNING id`

// InsertSalesStateOutboxEvent appends one outbox_events row and returns its id.
func (q *Queries) InsertSalesStateOutboxEvent(ctx context.Context, aggregateType, aggregateID, eventType string, payload map[string]any) (uuid.UUID, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = q.db.QueryRow(ctx, insertSalesStateOutboxEvent, aggregateType, aggregateID, eventType, string(body)).Scan(&id)
	return id, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSalesStateChange / SetSessionSalesState / ClearSessionSalesState
// ─────────────────────────────────────────────────────────────────────────────

// InsertSalesStateChangeParams is one journal row.
type InsertSalesStateChangeParams struct {
	SessionID     uuid.UUID
	EventID       uuid.UUID
	OrgID         uuid.UUID
	OldState      string
	NewState      string
	Source        string
	ActorType     string
	ActorID       string
	ActorName     string
	ActorRole     string
	OutboxEventID *uuid.UUID
}

const insertSalesStateChange = `-- name: InsertSalesStateChange :one
INSERT INTO session_sales_state_changes
    (session_id, event_id, org_id, old_state, new_state, source,
     actor_type, actor_id, actor_name, actor_role, outbox_event_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, created_at`

// InsertSalesStateChange writes one journal row.
func (q *Queries) InsertSalesStateChange(ctx context.Context, p InsertSalesStateChangeParams) (uuid.UUID, time.Time, error) {
	var (
		id uuid.UUID
		at time.Time
	)
	err := q.db.QueryRow(ctx, insertSalesStateChange,
		p.SessionID, p.EventID, p.OrgID, p.OldState, p.NewState, p.Source,
		p.ActorType, p.ActorID, p.ActorName, p.ActorRole, p.OutboxEventID,
	).Scan(&id, &at)
	return id, at, err
}

const setSessionSalesState = `-- name: SetSessionSalesState :exec
INSERT INTO session_sales_states (session_id, state, changed_at, change_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE
SET state = EXCLUDED.state, changed_at = EXCLUDED.changed_at, change_id = EXCLUDED.change_id`

// SetSessionSalesState stores the organizer's state of a session.
func (q *Queries) SetSessionSalesState(ctx context.Context, sessionID uuid.UUID, state string, at time.Time, changeID uuid.UUID) error {
	_, err := q.db.Exec(ctx, setSessionSalesState, sessionID, state, at, changeID)
	return err
}

const clearSessionSalesState = `-- name: ClearSessionSalesState :exec
DELETE FROM session_sales_states WHERE session_id = $1`

// ClearSessionSalesState lifts the organizer's state: sales follow the
// categories again.
func (q *Queries) ClearSessionSalesState(ctx context.Context, sessionID uuid.UUID) error {
	_, err := q.db.Exec(ctx, clearSessionSalesState, sessionID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSalesStateChanges
// ─────────────────────────────────────────────────────────────────────────────

// SalesStateChangeRow is one journal row with the delivery of its outbox
// event to the sites.
type SalesStateChangeRow struct {
	ID              uuid.UUID
	OldState        string
	NewState        string
	Source          string
	ActorType       string
	ActorID         string
	ActorName       string
	ActorRole       string
	CreatedAt       time.Time
	OutboxEventID   *uuid.UUID
	OutboxProcessed *time.Time
	OutboxDead      *time.Time
	OutboxAttempts  int32
	OutboxLastError *string
}

const listSalesStateChanges = `-- name: ListSalesStateChanges :many
SELECT c.id, c.old_state, c.new_state, c.source, c.actor_type, c.actor_id,
       c.actor_name, c.actor_role, c.created_at, c.outbox_event_id,
       o.processed_at, o.dead_lettered_at, COALESCE(o.attempts, 0), o.last_error
FROM   session_sales_state_changes c
LEFT   JOIN outbox_events o ON o.id = c.outbox_event_id
WHERE  c.session_id = $1
ORDER  BY c.created_at DESC, c.id DESC
LIMIT  $2`

// ListSalesStateChanges returns the latest journal rows of a session, newest
// first.
func (q *Queries) ListSalesStateChanges(ctx context.Context, sessionID uuid.UUID, limit int32) ([]SalesStateChangeRow, error) {
	rows, err := q.db.Query(ctx, listSalesStateChanges, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesStateChangeRow
	for rows.Next() {
		var r SalesStateChangeRow
		if err := rows.Scan(
			&r.ID, &r.OldState, &r.NewState, &r.Source, &r.ActorType, &r.ActorID,
			&r.ActorName, &r.ActorRole, &r.CreatedAt, &r.OutboxEventID,
			&r.OutboxProcessed, &r.OutboxDead, &r.OutboxAttempts, &r.OutboxLastError,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSalesStateActor
// ─────────────────────────────────────────────────────────────────────────────

// The person's name (first and last, else the e-mail) and their membership
// role in the organization at this moment: the role the journal records. A
// platform operator acting without a membership is "platform_staff".
const getSalesStateActor = `-- name: GetSalesStateActor :one
SELECT COALESCE(NULLIF(btrim(concat_ws(' ', u.first_name, u.last_name)), ''), u.email),
       COALESCE((SELECT m.role FROM memberships m
                  WHERE m.user_id = u.id AND m.org_id = $2
                  ORDER BY (m.status = 'active') DESC,
                           CASE m.role WHEN 'org_admin' THEN 0 WHEN 'organizer' THEN 1 ELSE 2 END
                  LIMIT 1),
                CASE WHEN EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                                   WHERE ur.user_id = u.id AND ` + PlatformRoleRowSQL + `)
                     THEN 'platform_staff' ELSE '' END)
FROM   users u
WHERE  u.id = $1`

// GetSalesStateActor returns the display name and the role of a user for
// the sales state journal. pgx.ErrNoRows for an unknown user.
func (q *Queries) GetSalesStateActor(ctx context.Context, userID, orgID uuid.UUID) (name, role string, err error) {
	err = q.db.QueryRow(ctx, getSalesStateActor, userID, orgID).Scan(&name, &role)
	return name, role, err
}

// ─────────────────────────────────────────────────────────────────────────────
// CountLiveExternalAllocations
// ─────────────────────────────────────────────────────────────────────────────

const countLiveExternalAllocations = `-- name: CountLiveExternalAllocations :one
SELECT count(*)::bigint
FROM   external_allocations
WHERE  session_id = $1
  AND  status IN ('pending', 'active', 'disputed')`

// CountLiveExternalAllocations counts the partner quotas of a session that
// may still be selling outside arena: a sales state cannot close them, so
// the event center warns the organizer to tell those partners by hand.
func (q *Queries) CountLiveExternalAllocations(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countLiveExternalAllocations, sessionID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSalesStateSessionScope
// ─────────────────────────────────────────────────────────────────────────────

const getSalesStateSessionScope = `-- name: GetSalesStateSessionScope :one
SELECT s.event_id, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id = $1
  AND  s.deleted_at IS NULL`

// GetSalesStateSessionScope returns the event and the organization of a live
// session. pgx.ErrNoRows for an unknown or deleted one.
func (q *Queries) GetSalesStateSessionScope(ctx context.Context, sessionID uuid.UUID) (eventID, orgID uuid.UUID, err error) {
	err = q.db.QueryRow(ctx, getSalesStateSessionScope, sessionID).Scan(&eventID, &orgID)
	return eventID, orgID, err
}
