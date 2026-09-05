// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: reservation_ga_items.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ReservationGAItemRow — result type for reservation_ga_items queries
// ─────────────────────────────────────────────────────────────────────────────

// ReservationGAItemRow is one general-admission line of a reservation joined
// with the owning ticket tier's display metadata. UnitPrice is the per-ticket
// price snapshot (smallest currency unit) taken when the hold was priced
// through the platform pricing pipeline.
type ReservationGAItemRow struct {
	ReservationID uuid.UUID `json:"reservation_id"`
	TierID        uuid.UUID `json:"tier_id"`
	Quantity      int32     `json:"quantity"`
	UnitPrice     int64     `json:"unit_price"`
	TierName      string    `json:"tier_name"`
	Currency      string    `json:"currency"`
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertReservationGAItem
// ─────────────────────────────────────────────────────────────────────────────

const insertReservationGAItem = `-- name: InsertReservationGAItem :exec
INSERT INTO reservation_ga_items (reservation_id, tier_id, quantity, unit_price)
VALUES ($1, $2, $3, $4)`

// InsertReservationGAItem records one GA line (tier, quantity, unit-price
// snapshot) for a reservation inside the hold transaction. Callers aggregate
// quantities per tier before the insert; the composite PRIMARY KEY
// (reservation_id, tier_id) raises 23505 on a duplicate tier.
func (q *Queries) InsertReservationGAItem(ctx context.Context, reservationID, tierID uuid.UUID, quantity int32, unitPrice int64) error {
	_, err := q.db.Exec(ctx, insertReservationGAItem, reservationID, tierID, quantity, unitPrice)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListReservationGAItems
// ─────────────────────────────────────────────────────────────────────────────

const listReservationGAItems = `-- name: ListReservationGAItems :many
SELECT gi.reservation_id, gi.tier_id, gi.quantity, gi.unit_price,
       t.name AS tier_name, t.currency
FROM   reservation_ga_items gi
JOIN   ticket_tiers t ON t.id = gi.tier_id
WHERE  gi.reservation_id = $1
ORDER  BY t.name ASC, gi.tier_id ASC`

// ListReservationGAItems returns every GA line of a reservation joined with
// the owning ticket_tiers row (display name + currency), ordered by tier name
// then tier id for deterministic status responses.
func (q *Queries) ListReservationGAItems(ctx context.Context, reservationID uuid.UUID) ([]ReservationGAItemRow, error) {
	rows, err := q.db.Query(ctx, listReservationGAItems, reservationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ReservationGAItemRow
	for rows.Next() {
		var it ReservationGAItemRow
		if err := rows.Scan(
			&it.ReservationID,
			&it.TierID,
			&it.Quantity,
			&it.UnitPrice,
			&it.TierName,
			&it.Currency,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpsertReservationGAItemQuantity
// ─────────────────────────────────────────────────────────────────────────────

const upsertReservationGAItemQuantity = `-- name: UpsertReservationGAItemQuantity :exec
INSERT INTO reservation_ga_items (reservation_id, tier_id, quantity, unit_price)
VALUES ($1, $2, $3, $4)
ON CONFLICT (reservation_id, tier_id) DO UPDATE
SET quantity = reservation_ga_items.quantity + EXCLUDED.quantity`

// UpsertReservationGAItemQuantity adds quantity tickets to the reservation's
// line for tierID, creating the line at unitPrice when it does not exist yet.
// An existing line keeps its locked unit_price (AB-48: the quoted price
// survives a cart extension) — only the quantity grows. W1-A5a, feature #483.
func (q *Queries) UpsertReservationGAItemQuantity(ctx context.Context, reservationID, tierID uuid.UUID, quantity int32, unitPrice int64) error {
	_, err := q.db.Exec(ctx, upsertReservationGAItemQuantity, reservationID, tierID, quantity, unitPrice)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// DecrementReservationGAItemQuantity
// ─────────────────────────────────────────────────────────────────────────────

const decrementReservationGAItemQuantity = `-- name: DecrementReservationGAItemQuantity :one
WITH depleted AS (
    DELETE FROM reservation_ga_items
    WHERE  reservation_id = $1
      AND  tier_id = $2
      AND  quantity <= $3
    RETURNING 0::integer AS quantity
), reduced AS (
    UPDATE reservation_ga_items
    SET    quantity = quantity - $3
    WHERE  reservation_id = $1
      AND  tier_id = $2
      AND  quantity > $3
    RETURNING quantity
)
SELECT quantity FROM depleted
UNION ALL
SELECT quantity FROM reduced`

// DecrementReservationGAItemQuantity removes quantity tickets from the
// reservation's line for tierID and returns the remaining quantity (0 = the
// line was consumed and deleted). Returns pgx.ErrNoRows when the line does not
// exist, which callers treat as "nothing to shrink".
//
// A consumed line is DELETED rather than zeroed because migration 0063
// constrains reservation_ga_items.quantity > 0; an UPDATE to 0 would raise
// 23514. Both CTEs read the same snapshot, so exactly one of them fires.
func (q *Queries) DecrementReservationGAItemQuantity(ctx context.Context, reservationID, tierID uuid.UUID, quantity int32) (int32, error) {
	row := q.db.QueryRow(ctx, decrementReservationGAItemQuantity, reservationID, tierID, quantity)
	var remaining int32
	err := row.Scan(&remaining)
	return remaining, err
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteReservationGAItems
// ─────────────────────────────────────────────────────────────────────────────

const deleteReservationGAItems = `-- name: DeleteReservationGAItems :exec
DELETE FROM reservation_ga_items
WHERE  reservation_id = $1`

// DeleteReservationGAItems removes every GA line for a reservation. The FK
// already cascades on reservation deletion; this exists for explicit cleanup
// paths that keep the reservation row (symmetry with DeleteReservationSeats).
func (q *Queries) DeleteReservationGAItems(ctx context.Context, reservationID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteReservationGAItems, reservationID)
	return err
}
