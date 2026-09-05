// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: gateway_cart.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// BindReservationToGatewaySession
// ─────────────────────────────────────────────────────────────────────────────

const bindReservationToGatewaySession = `-- name: BindReservationToGatewaySession :exec
UPDATE reservations
SET    gateway_session_id = $2,
       customer_id        = COALESCE($3, customer_id),
       updated_at         = now()
WHERE  id = $1`

// BindReservationToGatewaySession attaches a freshly created hold to the
// Bil24 gateway session that requested it, so later RESERVE / UN_RESERVE /
// UN_RESERVE_ALL commands of the same session can find and mutate the cart.
// customerID may be nil for anonymous gateway sessions; an existing
// customer_id is then preserved.
func (q *Queries) BindReservationToGatewaySession(ctx context.Context, reservationID, gatewaySessionID uuid.UUID, customerID *uuid.UUID) error {
	_, err := q.db.Exec(ctx, bindReservationToGatewaySession, reservationID, gatewaySessionID, customerID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetActiveGatewayCartReservation
// ─────────────────────────────────────────────────────────────────────────────

const getActiveGatewayCartReservation = `-- name: GetActiveGatewayCartReservation :one
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  gateway_session_id = $1
  AND  session_id = $2
  AND  state IN ('draft', 'active')
  AND  expires_at > now()
ORDER  BY created_at DESC, id DESC
LIMIT  1`

// GetActiveGatewayCartReservation returns the single open reservation the
// gateway session holds for one event session — the row a RESERVE extends and
// an UN_RESERVE shrinks. Returns pgx.ErrNoRows when the cart carries no line
// for that event session yet, which callers translate into "create the hold".
func (q *Queries) GetActiveGatewayCartReservation(ctx context.Context, gatewaySessionID, sessionID uuid.UUID) (ReservationRow, error) {
	row := q.db.QueryRow(ctx, getActiveGatewayCartReservation, gatewaySessionID, sessionID)
	return scanReservationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListActiveGatewayCartReservations
// ─────────────────────────────────────────────────────────────────────────────

const listActiveGatewayCartReservations = `-- name: ListActiveGatewayCartReservations :many
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  gateway_session_id = $1
  AND  state IN ('draft', 'active')
  AND  expires_at > now()
ORDER  BY created_at ASC, id ASC`

// ListActiveGatewayCartReservations returns every open reservation of the
// gateway session across all event sessions — the whole cart projected into
// the RESERVATION response seatList and the exact set cancelled by
// UN_RESERVE_ALL. Ordered oldest-first for a stable response.
func (q *Queries) ListActiveGatewayCartReservations(ctx context.Context, gatewaySessionID uuid.UUID) ([]ReservationRow, error) {
	rows, err := q.db.Query(ctx, listActiveGatewayCartReservations, gatewaySessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ReservationRow
	for rows.Next() {
		r, err := scanReservationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
