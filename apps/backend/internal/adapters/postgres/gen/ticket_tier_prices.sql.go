// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: ticket_tier_prices.sql (AB-48 scheduled pricing)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TicketTierPriceRow is one ticket_tier_prices window (migration 0087).
// ValidTo is exclusive; nil = open-ended. Overlap between windows of one
// tier is impossible (GiST exclusion constraint).
type TicketTierPriceRow struct {
	ID          uuid.UUID  `json:"id"`
	TierID      uuid.UUID  `json:"tier_id"`
	ValidFrom   time.Time  `json:"valid_from"`
	ValidTo     *time.Time `json:"valid_to"`
	PriceAmount int64      `json:"price_amount"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// scanTicketTierPriceRow scans a single ticket_tier_prices row.
func scanTicketTierPriceRow(row interface {
	Scan(dest ...any) error
}) (TicketTierPriceRow, error) {
	var r TicketTierPriceRow
	err := row.Scan(
		&r.ID, &r.TierID, &r.ValidFrom, &r.ValidTo,
		&r.PriceAmount, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

const listTierPriceWindows = `-- name: ListTierPriceWindows :many
SELECT id, tier_id, valid_from, valid_to, price_amount, created_at, updated_at
FROM   ticket_tier_prices
WHERE  tier_id = ANY($1::uuid[])
ORDER  BY tier_id, valid_from ASC`

// ListTierPriceWindows returns every price window for the given tiers,
// ordered by tier and start. Surfaces feed the rows into
// pricing.Resolve — the one AB-48 resolver.
func (q *Queries) ListTierPriceWindows(ctx context.Context, tierIDs []uuid.UUID) ([]TicketTierPriceRow, error) {
	rows, err := q.db.Query(ctx, listTierPriceWindows, tierIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TicketTierPriceRow
	for rows.Next() {
		r, err := scanTicketTierPriceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const insertTierPriceWindow = `-- name: InsertTierPriceWindow :one
INSERT INTO ticket_tier_prices (tier_id, valid_from, valid_to, price_amount)
VALUES ($1, $2, $3, $4)
RETURNING id, tier_id, valid_from, valid_to, price_amount, created_at, updated_at`

// InsertTierPriceWindow inserts one window. An overlap with an existing
// window of the same tier raises SQLSTATE 23P01 (exclusion_violation);
// handlers map it to a 422.
func (q *Queries) InsertTierPriceWindow(ctx context.Context, tierID uuid.UUID, validFrom time.Time, validTo *time.Time, priceAmount int64) (TicketTierPriceRow, error) {
	row := q.db.QueryRow(ctx, insertTierPriceWindow, tierID, validFrom, validTo, priceAmount)
	return scanTicketTierPriceRow(row)
}

const deleteTierPriceWindowsByTier = `-- name: DeleteTierPriceWindowsByTier :execrows
DELETE FROM ticket_tier_prices
WHERE  tier_id = $1`

// DeleteTierPriceWindowsByTier wipes a tier's schedule; used by the
// replace-all PUT inside its transaction before re-inserting.
func (q *Queries) DeleteTierPriceWindowsByTier(ctx context.Context, tierID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteTierPriceWindowsByTier, tierID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
