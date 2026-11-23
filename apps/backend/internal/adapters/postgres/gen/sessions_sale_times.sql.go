// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: sessions.sql (sales end / doors open, migration 0128)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SetSessionSaleTimes
// ─────────────────────────────────────────────────────────────────────────────

const setSessionSaleTimes = `-- name: SetSessionSaleTimes :one
WITH released AS (
    UPDATE ticket_tiers tt
    SET    sale_window_end = NULL,
           updated_at      = now()
    FROM   sessions s
    WHERE  s.id               = $1
      AND  s.event_id         = $2
      AND  $3::timestamptz    IS NOT NULL
      AND  tt.session_id      = s.id
      AND  tt.deleted_at      IS NULL
      AND  tt.sale_window_end = s.sales_end_at
    RETURNING tt.id
)
UPDATE sessions
SET    sales_end_at  = CASE WHEN $3::timestamptz IS NOT NULL THEN $3::timestamptz ELSE sales_end_at END,
       doors_open_at = CASE WHEN $5::boolean THEN $4::timestamptz ELSE doors_open_at END,
       updated_at    = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING ` + sessionColumns

// SetSessionSaleTimes writes a session's sales end and doors-open times.
// Moving the sales end also drops every category window that merely
// repeated the OLD sales end (the session limit covers those), so an
// extended sale is not cut short by a stale copy on the categories.
// A nil salesEndAt keeps the stored value (the column is never NULL).
// doorsOpenAt is applied only when setDoors is true, so nil with setDoors
// clears it and setDoors=false leaves it alone — the same tri-state the
// channel TTL uses. Run it AFTER InsertSession / UpdateSession in the same
// transaction: those leave the times to the 0128 trigger (start_at by
// default, carried along when the start moves). Neither time is
// buyer-visible in the sessionchange sense, so it needs no journal row.
// Returns pgx.ErrNoRows for an unknown, foreign or deleted session.
func (q *Queries) SetSessionSaleTimes(ctx context.Context, id, eventID uuid.UUID, salesEndAt, doorsOpenAt *time.Time, setDoors bool) (SessionRow, error) {
	row := q.db.QueryRow(ctx, setSessionSaleTimes, id, eventID, salesEndAt, doorsOpenAt, setDoors)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSalesEnd
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSalesEnd = `-- name: GetSessionSalesEnd :one
SELECT sales_end_at
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetSessionSalesEnd returns when ticket sales for the session close. The
// hold gate (hcheckout.CheckGALinesSellable and siblings) refuses every NEW
// hold after it, on top of each category's own sale window.
// Returns pgx.ErrNoRows for an unknown or deleted session.
func (q *Queries) GetSessionSalesEnd(ctx context.Context, id uuid.UUID) (time.Time, error) {
	var t time.Time
	err := q.db.QueryRow(ctx, getSessionSalesEnd, id).Scan(&t)
	return t, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSalesState
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSalesState = `-- name: GetSessionSalesState :one
SELECT s.sales_end_at, s.status, COALESCE(st.state, ''), s.sales_mode
FROM   sessions s
LEFT   JOIN session_sales_states st ON st.session_id = s.id
WHERE  s.id = $1
  AND  s.deleted_at IS NULL`

// GetSessionSalesState returns when ticket sales for the session close, the
// session's own status, the organizer's sales state (migration 0183:
// "soon" / "sold_out", "" when none is set) AND who sells the tickets
// (sessions.sales_mode, migration 0221: "platform" / "external"). The hold
// gate (hcheckout.CheckSessionSalesOpen) refuses every NEW hold on an
// external session, once the sales end has passed, for a 'cancelled'
// session (a cancelled date keeps its row and its tiers) and while a sales
// state is set.
// Returns pgx.ErrNoRows for an unknown or deleted session.
func (q *Queries) GetSessionSalesState(ctx context.Context, id uuid.UUID) (salesEnd time.Time, status, manual, salesMode string, err error) {
	err = q.db.QueryRow(ctx, getSessionSalesState, id).Scan(&salesEnd, &status, &manual, &salesMode)
	return salesEnd, status, manual, salesMode, err
}

// ─────────────────────────────────────────────────────────────────────────────
// LockSessionSalesMode
// ─────────────────────────────────────────────────────────────────────────────

// FOR NO KEY UPDATE, not FOR UPDATE: it conflicts with the row lock the
// PATCH of a session takes (its UPDATE, which may switch sales_mode), but a
// hold inserting a reservation that references the session takes FOR KEY
// SHARE on it and must not wait.
const lockSessionSalesMode = `-- name: LockSessionSalesMode :one
SELECT sales_mode
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL
FOR NO KEY UPDATE`

// LockSessionSalesMode locks the session row and returns who sells its
// tickets (sessions.sales_mode, migration 0221). A transaction that creates
// tiers or seats calls it FIRST (lock order: sessions first), so a
// concurrent switch to 'external' cannot slip between its check and its
// INSERT. MUST be called inside a transaction. pgx.ErrNoRows for an unknown
// or deleted session.
func (q *Queries) LockSessionSalesMode(ctx context.Context, id uuid.UUID) (string, error) {
	var mode string
	err := q.db.QueryRow(ctx, lockSessionSalesMode, id).Scan(&mode)
	return mode, err
}
