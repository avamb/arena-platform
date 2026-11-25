// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: payment_embedded.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PaymentIntentFailureSuperseded is payment_intents.failure_code of an open
// intent replaced by a newer page on the same checkout (MarkPaymentIntentSuperseded).
const PaymentIntentFailureSuperseded = "superseded"

const insertEmbeddedPaymentIntent = `-- name: InsertEmbeddedPaymentIntent :one
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state
)
VALUES ($1, $2, $3, $4, $5, $6, 'created')
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at`

// InsertEmbeddedPaymentIntent creates the payment intent of an embedded
// checkout session (spec 65): keyed by the session id (cs_…) like a hosted
// one, with hosted_checkout_url NULL. The client secret is never stored.
func (q *Queries) InsertEmbeddedPaymentIntent(
	ctx context.Context,
	checkoutSessionID *uuid.UUID,
	orgID uuid.UUID,
	provider string,
	providerPaymentID string,
	amount int64,
	currency string,
) (PaymentIntentRow, error) {
	row := q.db.QueryRow(ctx, insertEmbeddedPaymentIntent,
		checkoutSessionID, orgID, provider, providerPaymentID, amount, currency,
	)
	return scanPaymentIntentRow(row)
}

const markPaymentIntentSuperseded = `-- name: MarkPaymentIntentSuperseded :execrows
UPDATE payment_intents
   SET failure_code = 'superseded',
       failure_message = 'replaced by a hosted payment page on the same hold',
       updated_at = now()
 WHERE id = $1
   AND state NOT IN ('succeeded', 'failed')`

// MarkPaymentIntentSuperseded marks an open intent as replaced by a newer
// page on the same checkout. Its state is left alone on purpose: a
// superseded session that still gets paid must pay the order. Reports
// whether a row changed.
func (q *Queries) MarkPaymentIntentSuperseded(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, markPaymentIntentSuperseded, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const getCheckoutPaymentContext = `-- name: GetCheckoutPaymentContext :one
SELECT cs.id, cs.org_id, cs.channel_id, cs.checkout_token, cs.state, cs.total, cs.currency,
       cs.buyer_locale, cs.return_url,
       o.id, o.status, o.buyer_email, o.buyer_name, o.buyer_phone,
       r.state, r.expires_at, e.name
  FROM checkout_sessions cs
  JOIN reservations r ON r.id = cs.reservation_id
  JOIN orders o       ON o.checkout_session_id = cs.id
  JOIN events e       ON e.id = o.event_id
 WHERE cs.id = $1
 ORDER BY o.created_at DESC, o.id DESC
 LIMIT 1`

// CheckoutPaymentContextRow is what a new payment page of an existing
// checkout needs (GetCheckoutPaymentContext).
type CheckoutPaymentContextRow struct {
	CheckoutSessionID uuid.UUID `json:"checkout_session_id"`
	OrgID             uuid.UUID `json:"org_id"`
	ChannelID         uuid.UUID `json:"channel_id"`
	CheckoutToken     string    `json:"checkout_token"`
	CheckoutState     string    `json:"checkout_state"`
	Total             *int64    `json:"total"`
	Currency          *string   `json:"currency"`
	BuyerLocale       *string   `json:"buyer_locale"`
	ReturnURL         *string   `json:"return_url"`
	OrderID           uuid.UUID `json:"order_id"`
	OrderStatus       string    `json:"order_status"`
	BuyerEmail        *string   `json:"buyer_email"`
	BuyerName         *string   `json:"buyer_name"`
	BuyerPhone        *string   `json:"buyer_phone"`
	ReservationState  string    `json:"reservation_state"`
	HoldExpiresAt     time.Time `json:"hold_expires_at"`
	EventName         string    `json:"event_name"`
}

// GetCheckoutPaymentContext reads one checkout with its order, hold and
// event. Returns pgx.ErrNoRows when the checkout has no order yet.
// orders.checkout_session_id is not unique: with several orders the newest
// one (created_at, then id) is answered, deterministically.
func (q *Queries) GetCheckoutPaymentContext(ctx context.Context, checkoutSessionID uuid.UUID) (CheckoutPaymentContextRow, error) {
	var r CheckoutPaymentContextRow
	err := q.db.QueryRow(ctx, getCheckoutPaymentContext, checkoutSessionID).Scan(
		&r.CheckoutSessionID, &r.OrgID, &r.ChannelID, &r.CheckoutToken, &r.CheckoutState, &r.Total, &r.Currency,
		&r.BuyerLocale, &r.ReturnURL,
		&r.OrderID, &r.OrderStatus, &r.BuyerEmail, &r.BuyerName, &r.BuyerPhone,
		&r.ReservationState, &r.HoldExpiresAt, &r.EventName,
	)
	return r, err
}
