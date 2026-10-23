// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: seat_layout.sql
//
// Read-only inputs of the seat-layout allocator (task S1e).

package gen

import (
	"context"

	"github.com/google/uuid"
)

// SessionSeatLayoutStateRow is one seat of a session as the allocator sees
// it. OrderPrice is set only for a sold or held seat whose current
// reservation has an order line; TierPrice is nil for a seat without a
// category.
type SessionSeatLayoutStateRow struct {
	SeatKey    string `json:"seat_key"`
	Status     string `json:"status"`
	OrderPrice *int64 `json:"order_price"`
	TierPrice  *int64 `json:"tier_price"`
	// HoldExpired is true for a held seat whose reservation is no longer
	// live (not draft/active, or expires_at passed). The allocator treats
	// it as free: reservation.expire_sweep releases it within ~30 s.
	HoldExpired bool `json:"hold_expired"`
}

const listSessionSeatLayoutStates = `-- name: ListSessionSeatLayoutStates :many
SELECT ss.seat_key,
       ss.status,
       p.total         AS order_price,
       tt.price_amount AS tier_price,
       (ss.status = 'held' AND r.id IS NOT NULL
        AND (r.state NOT IN ('draft', 'active') OR r.expires_at <= now())) AS hold_expired
FROM      session_seats ss
LEFT JOIN ticket_tiers  tt ON tt.id = ss.tier_id
LEFT JOIN reservations  r  ON r.id = ss.reservation_id
LEFT JOIN (
    SELECT DISTINCT ON (oi.session_seat_id, o.reservation_id)
           oi.session_seat_id, o.reservation_id, oi.total
    FROM   orders o
    JOIN   order_items oi ON oi.order_id = o.id
    WHERE  o.session_id = $1
      AND  oi.session_seat_id IS NOT NULL
    ORDER  BY oi.session_seat_id, o.reservation_id, o.created_at DESC, oi.ordinal DESC
) p ON  p.session_seat_id = ss.id
    AND p.reservation_id  = ss.reservation_id
    AND ss.status IN ('sold', 'held')
WHERE  ss.session_id = $1
  AND  ss.kind = 'seat'
ORDER  BY ss.seat_key`

// ListSessionSeatLayoutStates lists every seat of a session with its status,
// the order price of a sold or held seat and its category's list price.
func (q *Queries) ListSessionSeatLayoutStates(ctx context.Context, sessionID uuid.UUID) ([]SessionSeatLayoutStateRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatLayoutStates, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatLayoutStateRow
	for rows.Next() {
		var r SessionSeatLayoutStateRow
		if err := rows.Scan(&r.SeatKey, &r.Status, &r.OrderPrice, &r.TierPrice, &r.HoldExpired); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
