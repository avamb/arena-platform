// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: (inline) spec 67 sections 3.2 and 5.2: the payment attempt of a
// provider page of a seat hold is written with its page_expires_at in ONE
// statement, and the rows of an existing hold are locked in one order:
// reservations, checkout_sessions, orders, payment_intents, then sessions,
// seats and inventory_ledger (hcheckout).

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertEmbeddedPaymentIntentWithPage = `-- name: InsertEmbeddedPaymentIntentWithPage :one
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state, page_expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'created', $7)
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at`

// InsertEmbeddedPaymentIntentWithPage is InsertEmbeddedPaymentIntent with the
// page expiry of a hold of the click in the same row (nil on the old path).
func (q *Queries) InsertEmbeddedPaymentIntentWithPage(
	ctx context.Context,
	checkoutSessionID *uuid.UUID,
	orgID uuid.UUID,
	provider string,
	providerPaymentID string,
	amount int64,
	currency string,
	pageExpiresAt *time.Time,
) (PaymentIntentRow, error) {
	row := q.db.QueryRow(ctx, insertEmbeddedPaymentIntentWithPage,
		checkoutSessionID, orgID, provider, providerPaymentID, amount, currency, pageExpiresAt,
	)
	return scanPaymentIntentRow(row)
}

const insertHostedPaymentIntentWithPage = `-- name: InsertHostedPaymentIntentWithPage :one
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
    state, hosted_checkout_url, page_expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'created', $7, $8)
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at`

// InsertHostedPaymentIntentWithPage is InsertHostedPaymentIntent with the
// page expiry of a hold of the click in the same row (nil on the old path).
func (q *Queries) InsertHostedPaymentIntentWithPage(
	ctx context.Context,
	checkoutSessionID *uuid.UUID,
	orgID uuid.UUID,
	provider string,
	providerPaymentID string,
	amount int64,
	currency string,
	hostedCheckoutURL string,
	pageExpiresAt *time.Time,
) (PaymentIntentRow, error) {
	row := q.db.QueryRow(ctx, insertHostedPaymentIntentWithPage,
		checkoutSessionID, orgID, provider, providerPaymentID, amount, currency,
		hostedCheckoutURL, pageExpiresAt,
	)
	return scanPaymentIntentRow(row)
}

const reservationHasHoldToken = `-- name: ReservationHasHoldToken :one
SELECT hold_token_hash IS NOT NULL FROM reservations WHERE id = $1`

// ReservationHasHoldToken reads, WITHOUT a lock, whether a reservation is a
// hold of the click. hold_token_hash is written when the hold is created and
// never changes, so the answer can choose the lock order of a transaction.
func (q *Queries) ReservationHasHoldToken(ctx context.Context, reservationID uuid.UUID) (bool, error) {
	var has bool
	err := q.db.QueryRow(ctx, reservationHasHoldToken, reservationID).Scan(&has)
	return has, err
}

// LockedCheckoutOfHold is one checkout_sessions row of a hold, locked.
type LockedCheckoutOfHold struct {
	ID    uuid.UUID
	State string
}

const lockCheckoutSessionsOfReservation = `-- name: LockCheckoutSessionsOfReservation :many
SELECT id, state
FROM   checkout_sessions
WHERE  reservation_id = $1
ORDER  BY id
FOR UPDATE`

// LockCheckoutSessionsOfReservation locks every checkout of a reservation,
// in id order: the second lock of a change of an existing hold, after the
// reservations row.
func (q *Queries) LockCheckoutSessionsOfReservation(ctx context.Context, reservationID uuid.UUID) ([]LockedCheckoutOfHold, error) {
	rows, err := q.db.Query(ctx, lockCheckoutSessionsOfReservation, reservationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LockedCheckoutOfHold
	for rows.Next() {
		var c LockedCheckoutOfHold
		if err := rows.Scan(&c.ID, &c.State); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const lockPendingOrdersOfReservation = `-- name: LockPendingOrdersOfReservation :many
SELECT id
FROM   orders
WHERE  reservation_id = $1
  AND  status = 'pending_payment'
ORDER  BY id
FOR UPDATE`

// LockPendingOrdersOfReservation locks the orders of a reservation that wait
// for payment, in id order: the third lock, after the checkouts.
func (q *Queries) LockPendingOrdersOfReservation(ctx context.Context, reservationID uuid.UUID) ([]uuid.UUID, error) {
	return q.lockIDs(ctx, lockPendingOrdersOfReservation, reservationID)
}

const lockOpenAttemptsOfReservation = `-- name: LockOpenAttemptsOfReservation :many
SELECT pi.id
FROM   payment_intents pi
JOIN   checkout_sessions cs ON cs.id = pi.checkout_session_id
WHERE  cs.reservation_id = $1
  AND  pi.state IN ('created', 'requires_action')
ORDER  BY pi.id
FOR UPDATE OF pi`

// LockOpenAttemptsOfReservation locks every open payment attempt of a
// reservation's checkouts, in id order: the fourth lock, after the orders.
func (q *Queries) LockOpenAttemptsOfReservation(ctx context.Context, reservationID uuid.UUID) ([]uuid.UUID, error) {
	return q.lockIDs(ctx, lockOpenAttemptsOfReservation, reservationID)
}

func (q *Queries) lockIDs(ctx context.Context, sql string, arg uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
