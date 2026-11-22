// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: session_external_sales.sql (migration 0218, spec 63 §14.1)

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SetSessionSalesMode
// ─────────────────────────────────────────────────────────────────────────────

const setSessionSalesMode = `-- name: SetSessionSalesMode :one
UPDATE sessions
SET    sales_mode               = $3,
       external_ticket_url      = $4,
       external_seller_name     = $5,
       display_price_from_minor = $6,
       display_currency         = $7,
       updated_at               = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING ` + sessionColumns

// SetSessionSalesMode writes who sells the tickets of an active session and
// the partner fields in one statement (migration 0218). For
// SessionSalesModePlatform pass nil for all four partner fields; for
// SessionSalesModeExternal url and sellerName are required (https only),
// priceFromMinor and currency go together. The table CHECKs reject any
// other combination (SQLSTATE 23514). The caller checks the lock rules with
// GetSessionSalesModeLockFacts first, inside the same transaction.
// Returns pgx.ErrNoRows when the session does not exist, belongs to a
// different event, or has been soft-deleted.
func (q *Queries) SetSessionSalesMode(ctx context.Context, id, eventID uuid.UUID, salesMode string, url, sellerName *string, priceFromMinor *int64, currency *string) (SessionRow, error) {
	row := q.db.QueryRow(ctx, setSessionSalesMode,
		id, eventID, salesMode, url, sellerName, priceFromMinor, currency,
	)
	return scanSessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSalesModeLockFacts
// ─────────────────────────────────────────────────────────────────────────────

// SessionSalesModeLockFacts tells what a session already has, for the lock
// rules of spec 63 §14.1:
//   - platform -> external only while HasOrders and HasHolds are false;
//   - external -> platform only while HasSeats and HasTiers are false.
//
// HasOrders counts any order and any issued ticket (an invitation is a
// ticket without an order). HasHolds counts draft or active reservations.
// HasSeats counts session_seats rows, HasTiers non-deleted ticket_tiers.
type SessionSalesModeLockFacts struct {
	HasOrders bool `json:"has_orders"`
	HasHolds  bool `json:"has_holds"`
	HasSeats  bool `json:"has_seats"`
	HasTiers  bool `json:"has_tiers"`
}

const getSessionSalesModeLockFacts = `-- name: GetSessionSalesModeLockFacts :one
SELECT EXISTS (SELECT 1 FROM orders o WHERE o.session_id = $1)
       OR EXISTS (SELECT 1 FROM tickets t WHERE t.session_id = $1) AS has_orders,
       EXISTS (SELECT 1 FROM reservations r
                WHERE r.session_id = $1
                  AND r.state IN ('draft', 'active'))                AS has_holds,
       EXISTS (SELECT 1 FROM session_seats ss WHERE ss.session_id = $1) AS has_seats,
       EXISTS (SELECT 1 FROM ticket_tiers tt
                WHERE tt.session_id = $1
                  AND tt.deleted_at IS NULL)                         AS has_tiers`

// GetSessionSalesModeLockFacts reports whether a session has orders,
// holds, seats or tiers (spec 63 §14.1 lock rules). It never fails for an
// unknown session: every fact is then false.
func (q *Queries) GetSessionSalesModeLockFacts(ctx context.Context, sessionID uuid.UUID) (SessionSalesModeLockFacts, error) {
	var f SessionSalesModeLockFacts
	err := q.db.QueryRow(ctx, getSessionSalesModeLockFacts, sessionID).Scan(
		&f.HasOrders, &f.HasHolds, &f.HasSeats, &f.HasTiers,
	)
	return f, err
}
