// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: ticket_reconciliation.sql
//
// ListTicketsForReconciliation backs the admin Tickets console's manual
// reconciliation view (GET /v1/admin/tickets): the operator compares Arena
// against MACS by event/session with the fields a scanning reconciliation
// actually needs (barcode, category, order number, buyer, dates).
//
// This is a DELIBERATELY SEPARATE row type/scanner from TicketRow /
// scanTicketRow (tickets.sql.go), not a widening of the shared one — per
// AGENTS.md, scanTicketRow is shared by superadmin.sql.go, refunds.sql.go
// and macs_system_ids.sql.go, and widening it would require updating every
// one of those SELECTs too. Keeping this query and its row type independent
// avoids that blast radius entirely.
package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TicketReconciliationRow is one ticket joined with the context an operator
// needs to reconcile Arena against MACS: its session's owning event, its
// tier name (category), its order's site-visible number (orders.system_id)
// and its EAN-13 barcode credential (nil when none was issued yet — the
// caller falls back to the derived platform code, same as the MACS export).
type TicketReconciliationRow struct {
	ID                 uuid.UUID  `json:"id"`
	CheckoutSessionID  uuid.UUID  `json:"checkout_session_id"`
	SessionID          uuid.UUID  `json:"session_id"`
	EventID            uuid.UUID  `json:"event_id"`
	TierID             *uuid.UUID `json:"tier_id"`
	TierName           *string    `json:"tier_name"`
	HolderEmail        *string    `json:"holder_email"`
	Status             string     `json:"status"`
	IssuedAt           time.Time  `json:"issued_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	SeatKey            *string    `json:"seat_key"`
	SeatSector         *string    `json:"seat_sector"`
	SeatRow            *string    `json:"seat_row"`
	SeatNumber         *string    `json:"seat_number"`
	Ordinal            int32      `json:"ordinal"`
	CancelledAt        *time.Time `json:"cancelled_at"`
	CancellationReason *string    `json:"cancellation_reason"`
	RefundMode         *string    `json:"refund_mode"`
	RefundID           *uuid.UUID `json:"refund_id"`
	RefundDate         *time.Time `json:"refund_date"`
	RefundPrice        *int64     `json:"refund_price"`
	ReviewHold         bool       `json:"review_hold"`
	ReviewHoldReason   *string    `json:"review_hold_reason"`
	SystemTicketID     int64      `json:"system_ticket_id"`
	// OrderSystemID is orders.system_id (the site-visible order number).
	// Nil for a ticket predating the orders aggregate (tickets.order_id
	// NULL).
	OrderSystemID *int64  `json:"order_system_id"`
	BarcodeStr    *string `json:"barcode_str"`
}

const listTicketsForReconciliation = `-- name: ListTicketsForReconciliation :many
SELECT t.id, t.checkout_session_id, t.session_id, s.event_id, t.tier_id,
       tt.name AS tier_name, t.holder_email, t.status, t.issued_at,
       t.created_at, t.updated_at, t.seat_key, t.seat_sector, t.seat_row,
       t.seat_number, t.ordinal, t.cancelled_at, t.cancellation_reason,
       t.refund_mode, t.refund_id, t.refund_date, t.refund_price,
       t.review_hold, t.review_hold_reason,
       t.system_ticket_id, ord.system_id AS order_system_id,
       tc.payload AS barcode_str
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
JOIN   sessions s ON s.id = t.session_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN orders ord ON ord.id = t.order_id
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
WHERE  ($1::uuid IS NULL OR cs.org_id = $1)
  AND  ($2::text  IS NULL OR t.status = $2)
  AND  ($3::uuid IS NULL OR s.event_id = $3)
  AND  ($4::uuid IS NULL OR t.session_id = $4)
ORDER BY t.issued_at DESC, t.id DESC
LIMIT  $5 OFFSET $6`

// ListTicketsForReconciliation returns tickets across organizations (or one
// organization, when orgID is set) with the event/session/tier/order context
// the admin Tickets reconciliation console needs. Pass nil for any filter to
// leave it unconstrained.
func (q *Queries) ListTicketsForReconciliation(
	ctx context.Context,
	orgID *uuid.UUID,
	statusFilter *string,
	eventID *uuid.UUID,
	sessionID *uuid.UUID,
	limit int32,
	offset int32,
) ([]TicketReconciliationRow, error) {
	rows, err := q.db.Query(ctx, listTicketsForReconciliation,
		orgID, statusFilter, eventID, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TicketReconciliationRow
	for rows.Next() {
		var r TicketReconciliationRow
		if err := rows.Scan(
			&r.ID,
			&r.CheckoutSessionID,
			&r.SessionID,
			&r.EventID,
			&r.TierID,
			&r.TierName,
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
			&r.OrderSystemID,
			&r.BarcodeStr,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
