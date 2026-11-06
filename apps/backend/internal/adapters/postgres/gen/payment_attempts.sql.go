// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: payment_attempts.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// InsertHostedPaymentAttempt
// ─────────────────────────────────────────────────────────────────────────────

const insertHostedPaymentAttempt = `-- name: InsertHostedPaymentAttempt :one
INSERT INTO payment_intents (
    checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state
)
VALUES ($1, $2, $3, $4, $5, $6, 'created')
RETURNING id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency,
          state, sca_redirect_url, client_secret, failure_code, failure_message,
          authorized_at, succeeded_at, failed_at, hosted_checkout_url, provider_charge_ref,
          created_at, updated_at`

// InsertHostedPaymentAttempt records a hosted payment BEFORE the provider is
// called (П-19), for a provider whose payment id is arena's own reference
// (payments.Capabilities.ReferenceIsPaymentID — AllPay's order_id is the
// checkout session id). hosted_checkout_url stays NULL until the provider
// answers (SetPaymentIntentHostedURL); a row left without it is what the
// checkout.payment_recover job looks for.
func (q *Queries) InsertHostedPaymentAttempt(
	ctx context.Context,
	checkoutSessionID *uuid.UUID,
	orgID uuid.UUID,
	provider string,
	providerPaymentID string,
	amount int64,
	currency string,
) (PaymentIntentRow, error) {
	row := q.db.QueryRow(ctx, insertHostedPaymentAttempt,
		checkoutSessionID, orgID, provider, providerPaymentID, amount, currency,
	)
	return scanPaymentIntentRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SetPaymentIntentHostedURL
// ─────────────────────────────────────────────────────────────────────────────

const setPaymentIntentHostedURL = `-- name: SetPaymentIntentHostedURL :execrows
UPDATE payment_intents
   SET hosted_checkout_url = $2, updated_at = now()
 WHERE id = $1
   AND hosted_checkout_url IS NULL
   AND state = 'created'`

// SetPaymentIntentHostedURL stores the payment page of a recorded attempt.
// It only ever fills an EMPTY page of an intent still in 'created': the
// request and the recovery job may both learn the URL, and the first one
// wins. Reports whether a row changed.
func (q *Queries) SetPaymentIntentHostedURL(ctx context.Context, id uuid.UUID, hostedCheckoutURL string) (bool, error) {
	tag, err := q.db.Exec(ctx, setPaymentIntentHostedURL, id, hostedCheckoutURL)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListStalledHostedPaymentAttempts
// ─────────────────────────────────────────────────────────────────────────────

const listStalledHostedPaymentAttempts = `-- name: ListStalledHostedPaymentAttempts :many
SELECT pi.id, pi.org_id, pi.provider, pi.provider_payment_id, pi.amount, pi.currency,
       cs.id, cs.channel_id, cs.checkout_token, cs.buyer_locale, cs.return_url,
       o.id, o.buyer_email, o.buyer_name, o.buyer_phone, r.expires_at, e.name
  FROM payment_intents pi
  JOIN checkout_sessions cs ON cs.id = pi.checkout_session_id
  JOIN reservations r      ON r.id = cs.reservation_id
  JOIN orders o            ON o.checkout_session_id = cs.id
  JOIN events e            ON e.id = o.event_id
 WHERE pi.provider = $1
   AND pi.state = 'created'
   AND pi.hosted_checkout_url IS NULL
   AND pi.provider_payment_id IS NOT NULL
   AND pi.created_at < $2
   AND cs.state IN ('pricing_confirmed', 'payment_started')
   AND o.status = 'pending_payment'
   AND r.state IN ('draft', 'active')
   AND r.expires_at > now()
 ORDER BY pi.created_at
 LIMIT $3`

// StalledHostedPaymentAttemptRow is one recorded payment attempt that never
// got its payment page, with what is needed to ask the provider again.
type StalledHostedPaymentAttemptRow struct {
	PaymentIntentID   uuid.UUID `json:"payment_intent_id"`
	OrgID             uuid.UUID `json:"org_id"`
	Provider          string    `json:"provider"`
	ProviderPaymentID string    `json:"provider_payment_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	CheckoutSessionID uuid.UUID `json:"checkout_session_id"`
	ChannelID         uuid.UUID `json:"channel_id"`
	CheckoutToken     string    `json:"checkout_token"`
	BuyerLocale       *string   `json:"buyer_locale"`
	ReturnURL         *string   `json:"return_url"`
	OrderID           uuid.UUID `json:"order_id"`
	BuyerEmail        *string   `json:"buyer_email"`
	BuyerName         *string   `json:"buyer_name"`
	BuyerPhone        *string   `json:"buyer_phone"`
	// HoldExpiresAt is the reservation's expiry: the payment window plus
	// the grace (checkout/start sets both on the same transaction).
	HoldExpiresAt time.Time `json:"hold_expires_at"`
	EventName     string    `json:"event_name"`
}

// ListStalledHostedPaymentAttempts lists recorded attempts of one provider
// still without a payment page, created before createdBefore, whose
// checkout, order and hold are all still open (checkout.payment_recover).
func (q *Queries) ListStalledHostedPaymentAttempts(
	ctx context.Context,
	provider string,
	createdBefore time.Time,
	limit int32,
) ([]StalledHostedPaymentAttemptRow, error) {
	rows, err := q.db.Query(ctx, listStalledHostedPaymentAttempts, provider, createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StalledHostedPaymentAttemptRow
	for rows.Next() {
		var r StalledHostedPaymentAttemptRow
		if err := rows.Scan(
			&r.PaymentIntentID, &r.OrgID, &r.Provider, &r.ProviderPaymentID, &r.Amount, &r.Currency,
			&r.CheckoutSessionID, &r.ChannelID, &r.CheckoutToken, &r.BuyerLocale, &r.ReturnURL,
			&r.OrderID, &r.BuyerEmail, &r.BuyerName, &r.BuyerPhone, &r.HoldExpiresAt, &r.EventName,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
