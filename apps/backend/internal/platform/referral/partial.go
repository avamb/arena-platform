package referral

// partial.go — who can carry a personal code. The cashback is a partial refund
// of the referrer's own payment, so a code is issued only for an order paid
// through a provider that can return a part of a payment to the card (owner
// decision, second review). The rule is read from the payment module registry,
// the same descriptors the organizer's capability block shows.

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// ReasonProviderUnsupported is the reward reason when the referrer's payment
// provider cannot return a part of a payment. It never sends a letter.
const ReasonProviderUnsupported = "provider_unsupported"

// CanPartialRefund reports whether provider has a payment module that can
// return a part of a payment.
func CanPartialRefund(reg *payments.Registry, provider string) bool {
	entry, ok := reg.Get(payments.NormalizeProviderName(provider))
	return ok && entry.New != nil && entry.Descriptor.Capabilities.PartialRefund
}

// providerOfOrder is the provider of the succeeded payment of a ticket order.
// found is false when the order has no payment arena took.
func providerOfOrder(ctx context.Context, db gen.DBTX, orderID uuid.UUID) (provider string, found bool, err error) {
	err = db.QueryRow(ctx, `
		SELECT pi.provider
		FROM   orders o
		JOIN   payment_intents pi ON pi.checkout_session_id = o.checkout_session_id AND pi.org_id = o.org_id
		WHERE  o.id = $1 AND pi.state = 'succeeded'
		ORDER  BY pi.succeeded_at DESC NULLS LAST, pi.created_at DESC LIMIT 1`, orderID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return provider, err == nil, err
}

// providerOfShopPayment is the provider of a shop order's succeeded payment.
func providerOfShopPayment(ctx context.Context, db gen.DBTX, paymentIntentID *uuid.UUID) (provider string, found bool, err error) {
	if paymentIntentID == nil {
		return "", false, nil
	}
	err = db.QueryRow(ctx, `SELECT provider FROM payment_intents WHERE id = $1 AND state = 'succeeded'`,
		*paymentIntentID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return provider, err == nil, err
}

// orderCanCarryCode: the order was paid through a provider that can pay a
// cashback back to the card.
func orderCanCarryCode(ctx context.Context, db gen.DBTX, orderID uuid.UUID) (bool, error) {
	provider, found, err := providerOfOrder(ctx, db, orderID)
	if err != nil || !found {
		return false, err
	}
	return CanPartialRefund(paymodules.Registry(), provider), nil
}

// shopOrderCanCarryCode is orderCanCarryCode for a shop order.
func shopOrderCanCarryCode(ctx context.Context, db gen.DBTX, paymentIntentID *uuid.UUID) (bool, error) {
	provider, found, err := providerOfShopPayment(ctx, db, paymentIntentID)
	if err != nil || !found {
		return false, err
	}
	return CanPartialRefund(paymodules.Registry(), provider), nil
}
