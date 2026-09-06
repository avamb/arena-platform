// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: tickets.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// TicketRow — shared result type for all tickets queries
// ─────────────────────────────────────────────────────────────────────────────

// TicketRow is the result type returned by all tickets queries.
//
// One TicketRow represents a single unit of entitlement issued after
// payment.succeeded or free-checkout completion.
//
// State machine: active → cancelled | transferred. Terminal states are
// cancelled and transferred.
//
// TierID is nil for GA / untiered reservations.
// HolderEmail is nil for anonymous purchases or when email is not yet known.
//
// SEAT-C3 (feature #311): SeatKey / SeatSector / SeatRow / SeatNumber
// are denormalized copies of session_seats.{seat_key,sector_name,row_name,
// seat_number} taken at issuance time. All four are nil for
// general-admission tickets; all four are populated together for tickets
// issued from an assigned-seats reservation.
//
// Ordinal (feature #366): 0-based sequence number of this ticket within the
// checkout session. Together with CheckoutSessionID it forms a unique pair
// (backed by UNIQUE constraint tickets_checkout_ordinal_uq) that prevents
// concurrent double-issuance and enables partial-issue recovery on retry.
type TicketRow struct {
	ID                uuid.UUID  `json:"id"`
	CheckoutSessionID uuid.UUID  `json:"checkout_session_id"`
	SessionID         uuid.UUID  `json:"session_id"`
	TierID            *uuid.UUID `json:"tier_id"`
	HolderEmail       *string    `json:"holder_email"`
	Status            string     `json:"status"`
	IssuedAt          time.Time  `json:"issued_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	SeatKey           *string    `json:"seat_key"`
	SeatSector        *string    `json:"seat_sector"`
	SeatRow           *string    `json:"seat_row"`
	SeatNumber        *string    `json:"seat_number"`
	// Ordinal is the 0-based ticket index within the checkout session.
	// Added in migration 0066 (feature #366) for idempotent concurrent issuance.
	Ordinal int32 `json:"ordinal"`
	// AB-49 (migration 0086): cancellation + refund record. All nil/false
	// while the ticket is active. RefundMode is 'none' | 'manual' |
	// 'automatic'; 'manual' is an OUTSTANDING obligation, never "done".
	CancelledAt        *time.Time `json:"cancelled_at"`
	CancellationReason *string    `json:"cancellation_reason"`
	RefundMode         *string    `json:"refund_mode"`
	RefundID           *uuid.UUID `json:"refund_id"`
	RefundDate         *time.Time `json:"refund_date"`
	RefundPrice        *int64     `json:"refund_price"`
	ReviewHold         bool       `json:"review_hold"`
	ReviewHoldReason   *string    `json:"review_hold_reason"`
	// AB-50a (migration 0088): stable bigint identity for MACS scanning integration.
	SystemTicketID int64 `json:"system_ticket_id"`
}

// scanTicketRow scans a single tickets row into a TicketRow.
func scanTicketRow(row interface {
	Scan(dest ...any) error
}) (TicketRow, error) {
	var r TicketRow
	err := row.Scan(
		&r.ID,
		&r.CheckoutSessionID,
		&r.SessionID,
		&r.TierID,
		&r.HolderEmail,
		&r.Status,
		&r.IssuedAt,
		&r.CreatedAt,
		&r.UpdatedAt,
		&r.SeatKey,
		&r.SeatSector,
		&r.SeatRow,
		&r.SeatNumber,
		&r.Ordinal,
		&r.CancelledAt,
		&r.CancellationReason,
		&r.RefundMode,
		&r.RefundID,
		&r.RefundDate,
		&r.RefundPrice,
		&r.ReviewHold,
		&r.ReviewHoldReason,
		&r.SystemTicketID,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertTicket
// ─────────────────────────────────────────────────────────────────────────────

const insertTicket = `-- name: InsertTicket :one
INSERT INTO tickets (
    checkout_session_id, session_id, tier_id, holder_email,
    seat_key, seat_sector, seat_row, seat_number, ordinal
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id`

// InsertTicket creates a new ticket row for the given checkout session.
//
// Pass tierID = nil for a GA / untiered ticket.
// Pass holderEmail = nil when the holder email is not yet known.
// Pass seatKey/seatSector/seatRow/seatNumber = nil for a GA ticket; the
// SEAT-C3 issuance path (feature #311) populates all four together from
// the reservation's session_seats row.
// ordinal is the 0-based ticket index within the checkout session
// (feature #366). The pair (checkoutSessionID, ordinal) is unique in the DB.
// Returns the created row including the uuidv7 PK assigned by the database.
func (q *Queries) InsertTicket(
	ctx context.Context,
	checkoutSessionID uuid.UUID,
	sessionID uuid.UUID,
	tierID *uuid.UUID,
	holderEmail *string,
	seatKey *string,
	seatSector *string,
	seatRow *string,
	seatNumber *string,
	ordinal int32,
) (TicketRow, error) {
	row := q.db.QueryRow(ctx, insertTicket,
		checkoutSessionID, sessionID, tierID, holderEmail,
		seatKey, seatSector, seatRow, seatNumber, ordinal,
	)
	return scanTicketRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListTicketsByCheckoutSession
// ─────────────────────────────────────────────────────────────────────────────

const listTicketsByCheckoutSession = `-- name: ListTicketsByCheckoutSession :many
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal,
       cancelled_at, cancellation_reason, refund_mode, refund_id,
       refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id
FROM   tickets
WHERE  checkout_session_id = $1
ORDER BY ordinal ASC, issued_at ASC, id ASC`

// ListTicketsByCheckoutSession returns all tickets issued for the given checkout
// session, ordered by ordinal (ascending). Used as the idempotency check:
// comparing len(result) to the expected quantity detects partial issuance.
func (q *Queries) ListTicketsByCheckoutSession(ctx context.Context, checkoutSessionID uuid.UUID) ([]TicketRow, error) {
	rows, err := q.db.Query(ctx, listTicketsByCheckoutSession, checkoutSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []TicketRow
	for rows.Next() {
		r, err := scanTicketRow(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, r)
	}
	return tickets, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetTicketByID
// ─────────────────────────────────────────────────────────────────────────────

const getTicketByID = `-- name: GetTicketByID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal,
       cancelled_at, cancellation_reason, refund_mode, refund_id,
       refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id
FROM   tickets
WHERE  id = $1`

// GetTicketByID fetches a single ticket by its UUID primary key.
// Returns pgx.ErrNoRows when not found.
func (q *Queries) GetTicketByID(ctx context.Context, id uuid.UUID) (TicketRow, error) {
	row := q.db.QueryRow(ctx, getTicketByID, id)
	return scanTicketRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountTicketsByCheckoutSession
// ─────────────────────────────────────────────────────────────────────────────

const countTicketsByCheckoutSession = `-- name: CountTicketsByCheckoutSession :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  checkout_session_id = $1`

// CountTicketsByCheckoutSession returns the number of tickets already issued
// for the given checkout session. Use this as a lightweight idempotency probe
// before calling ListTicketsByCheckoutSession for the full result set.
func (q *Queries) CountTicketsByCheckoutSession(ctx context.Context, checkoutSessionID uuid.UUID) (int64, error) {
	var count int64
	err := q.db.QueryRow(ctx, countTicketsByCheckoutSession, checkoutSessionID).Scan(&count)
	return count, err
}

// ─────────────────────────────────────────────────────────────────────────────
// CountTicketsBySession
// ─────────────────────────────────────────────────────────────────────────────

const countTicketsBySession = `-- name: CountTicketsBySession :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1`

// CountTicketsBySession returns the number of tickets issued against the
// given (catalog) session. Powers the seating-plan rebind gate
// (feature #306, Wave SEAT-B2) alongside CountReservationsBySession.
func (q *Queries) CountTicketsBySession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var count int64
	err := q.db.QueryRow(ctx, countTicketsBySession, sessionID).Scan(&count)
	return count, err
}

// ─────────────────────────────────────────────────────────────────────────────
// AB-49: ticket cancellation
// ─────────────────────────────────────────────────────────────────────────────

const cancelTicket = `-- name: CancelTicket :one
UPDATE tickets
SET    status              = 'cancelled',
       cancelled_at        = now(),
       cancellation_reason = $2,
       refund_mode         = $3,
       updated_at          = now()
WHERE  id     = $1
  AND  status = 'active'
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id`

// CancelTicket performs the conditional 'active' -> 'cancelled'
// transition for the AB-49 operator cancellation, recording the reason
// and the refund-mode decision. Returns pgx.ErrNoRows when the ticket
// is not active — the caller MUST surface that as a 409 conflict.
func (q *Queries) CancelTicket(ctx context.Context, id uuid.UUID, reason string, refundMode string) (TicketRow, error) {
	row := q.db.QueryRow(ctx, cancelTicket, id, reason, refundMode)
	return scanTicketRow(row)
}

const setTicketRefundRecord = `-- name: SetTicketRefundRecord :one
UPDATE tickets
SET    refund_id    = $2,
       refund_date  = $3,
       refund_price = $4,
       updated_at   = now()
WHERE  id = $1
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id`

// SetTicketRefundRecord records the financial side of a cancellation on
// the ticket (refunds-row link, refund date, refunded amount — the
// Bil24 refundDate/refundPrice shape). Called AFTER the cancellation
// transaction committed; never gates inventory or admission.
func (q *Queries) SetTicketRefundRecord(ctx context.Context, id uuid.UUID, refundID *uuid.UUID, refundDate *time.Time, refundPrice *int64) (TicketRow, error) {
	row := q.db.QueryRow(ctx, setTicketRefundRecord, id, refundID, refundDate, refundPrice)
	return scanTicketRow(row)
}

const setTicketsReviewHoldByCheckoutSession = `-- name: SetTicketsReviewHoldByCheckoutSession :execrows
UPDATE tickets
SET    review_hold        = true,
       review_hold_reason = $2,
       updated_at         = now()
WHERE  checkout_session_id = $1
  AND  status = 'active'
  AND  review_hold = false`

// SetTicketsReviewHoldByCheckoutSession flags every active ticket of an
// order for human review after a PARTIAL inbound provider refund that
// cannot be attributed to specific tickets. The hold FLAGS, never
// blocks admission, and is never propagated to MACS.
func (q *Queries) SetTicketsReviewHoldByCheckoutSession(ctx context.Context, checkoutSessionID uuid.UUID, reason string) (int64, error) {
	tag, err := q.db.Exec(ctx, setTicketsReviewHoldByCheckoutSession, checkoutSessionID, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const clearTicketReviewHold = `-- name: ClearTicketReviewHold :one
UPDATE tickets
SET    review_hold        = false,
       review_hold_reason = NULL,
       updated_at         = now()
WHERE  id = $1
  AND  review_hold = true
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason, system_ticket_id`

// ClearTicketReviewHold resolves a review hold on one ticket. Returns
// pgx.ErrNoRows when the ticket carries no hold.
func (q *Queries) ClearTicketReviewHold(ctx context.Context, id uuid.UUID) (TicketRow, error) {
	row := q.db.QueryRow(ctx, clearTicketReviewHold, id)
	return scanTicketRow(row)
}

const setTicketOrder = `-- name: SetTicketOrder :exec
UPDATE tickets
SET    order_id   = $2,
       updated_at = now()
WHERE  id = $1`

// SetTicketOrder links a freshly issued ticket to the order that paid for it
// (tickets.order_id, added by migration 0092; W1-A6c, feature #488, spec §7.9
// step 5). It is a narrow :exec on purpose: order_id is not a member of
// TicketRow, and widening that struct would force every SELECT feeding
// scanTicketRow — including the ones in superadmin.sql.go and refunds.sql.go —
// to grow a column.
func (q *Queries) SetTicketOrder(ctx context.Context, id, orderID uuid.UUID) error {
	_, err := q.db.Exec(ctx, setTicketOrder, id, orderID)
	return err
}

const countActiveTicketsForSeat = `-- name: CountActiveTicketsForSeat :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1
  AND  seat_key   = $2
  AND  status     = 'active'`

// CountActiveTicketsForSeat returns how many ACTIVE tickets still
// reference (session_id, seat_key). Guard probe for
// ReleaseSoldSessionSeat (uses tickets_active_seat_idx).
func (q *Queries) CountActiveTicketsForSeat(ctx context.Context, sessionID uuid.UUID, seatKey string) (int64, error) {
	var count int64
	err := q.db.QueryRow(ctx, countActiveTicketsForSeat, sessionID, seatKey).Scan(&count)
	return count, err
}

const listTicketsMissingEAN13 = `-- name: ListTicketsMissingEAN13 :many
SELECT t.id, t.checkout_session_id, t.session_id, t.tier_id, t.holder_email,
       t.status, t.issued_at, t.created_at, t.updated_at,
       t.seat_key, t.seat_sector, t.seat_row, t.seat_number, t.ordinal,
       t.cancelled_at, t.cancellation_reason, t.refund_mode, t.refund_id,
       t.refund_date, t.refund_price, t.review_hold, t.review_hold_reason,
       t.system_ticket_id
FROM   tickets t
LEFT JOIN ticket_credentials c
       ON c.ticket_id = t.id
      AND c.type      = 'ean13'
WHERE  c.id IS NULL
  AND  t.status = 'active'
ORDER BY t.id ASC
LIMIT $1`

// ListTicketsMissingEAN13 returns up to limit active tickets that have no
// ean13 ticket_credentials row yet (feature #503, W1-B6b). Powers the
// tickets.backfill_ean13 worker job: a LEFT JOIN ... IS NULL scan is
// naturally idempotent — once a ticket gets its ean13 credential, it drops
// out of every subsequent call's result set.
func (q *Queries) ListTicketsMissingEAN13(ctx context.Context, limit int32) ([]TicketRow, error) {
	rows, err := q.db.Query(ctx, listTicketsMissingEAN13, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []TicketRow
	for rows.Next() {
		r, err := scanTicketRow(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, r)
	}
	return tickets, rows.Err()
}
