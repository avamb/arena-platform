// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: macs_system_ids.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// AB-50a: MACS system id resolution queries
// ─────────────────────────────────────────────────────────────────────────────

// SystemIDForTicketRow is the narrow projection for resolving system integer
// ids from a ticket UUID — the minimal fetch the MACS adapter needs before
// building its export payload.
type SystemIDForTicketRow struct {
	ID             uuid.UUID `json:"id"`
	SystemTicketID int64     `json:"system_ticket_id"`
}

const getSystemIDForTicket = `-- name: GetSystemIDForTicket :one
SELECT id, system_ticket_id
FROM   tickets
WHERE  id = $1`

// GetSystemIDForTicket returns the MACS stable integer id for a single ticket.
// Returns pgx.ErrNoRows when not found.
func (q *Queries) GetSystemIDForTicket(ctx context.Context, ticketID uuid.UUID) (SystemIDForTicketRow, error) {
	var r SystemIDForTicketRow
	err := q.db.QueryRow(ctx, getSystemIDForTicket, ticketID).Scan(&r.ID, &r.SystemTicketID)
	return r, err
}

// SystemIDForSeatRow is the narrow projection for resolving the MACS seat
// integer id from a session_seats UUID.
type SystemIDForSeatRow struct {
	ID           uuid.UUID `json:"id"`
	SystemSeatID int64     `json:"system_seat_id"`
}

const getSystemIDForSeat = `-- name: GetSystemIDForSeat :one
SELECT id, system_seat_id
FROM   session_seats
WHERE  id = $1`

// GetSystemIDForSeat returns the MACS stable integer seat id for a single
// session_seats row. Returns pgx.ErrNoRows when not found.
func (q *Queries) GetSystemIDForSeat(ctx context.Context, seatID uuid.UUID) (SystemIDForSeatRow, error) {
	var r SystemIDForSeatRow
	err := q.db.QueryRow(ctx, getSystemIDForSeat, seatID).Scan(&r.ID, &r.SystemSeatID)
	return r, err
}

const listSystemIDsForTickets = `-- name: ListSystemIDsForTickets :many
SELECT id, system_ticket_id
FROM   tickets
WHERE  id = ANY($1::uuid[])`

// ListSystemIDsForTickets batch-resolves MACS integer ids for a slice of ticket
// UUIDs. Returns one row per UUID that exists in the table; missing UUIDs are
// silently omitted. Use for pre-computing the id map before building a MACS
// export envelope.
func (q *Queries) ListSystemIDsForTickets(ctx context.Context, ticketIDs []uuid.UUID) ([]SystemIDForTicketRow, error) {
	rows, err := q.db.Query(ctx, listSystemIDsForTickets, ticketIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SystemIDForTicketRow
	for rows.Next() {
		var r SystemIDForTicketRow
		if err := rows.Scan(&r.ID, &r.SystemTicketID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getTicketBySystemTicketID = `-- name: GetTicketBySystemTicketID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal,
       cancelled_at, cancellation_reason, refund_mode, refund_id,
       refund_date, refund_price, review_hold, review_hold_reason,
       system_ticket_id
FROM   tickets
WHERE  system_ticket_id = $1`

// GetTicketBySystemTicketID looks up a ticket by its MACS stable integer id.
// Returns pgx.ErrNoRows when not found. Used by the MACS adapter round-trip
// validation: after export, verify the integer maps back to the expected ticket.
func (q *Queries) GetTicketBySystemTicketID(ctx context.Context, systemTicketID int64) (TicketRow, error) {
	row := q.db.QueryRow(ctx, getTicketBySystemTicketID, systemTicketID)
	return scanTicketRow(row)
}

// TicketSystemRow represents a registered external ticket-scanning integration.
type TicketSystemRow struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

const getTicketSystemBySlug = `-- name: GetTicketSystemBySlug :one
SELECT id, slug, name
FROM   ticket_systems
WHERE  slug = $1`

// GetTicketSystemBySlug fetches a ticket_systems row by its machine slug
// (e.g. "macs"). Returns pgx.ErrNoRows when not registered.
func (q *Queries) GetTicketSystemBySlug(ctx context.Context, slug string) (TicketSystemRow, error) {
	var r TicketSystemRow
	err := q.db.QueryRow(ctx, getTicketSystemBySlug, slug).Scan(&r.ID, &r.Slug, &r.Name)
	return r, err
}
