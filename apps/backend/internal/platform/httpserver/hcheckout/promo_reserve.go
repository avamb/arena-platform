// promo_reserve.go — a code's max_uses and max_uses_per_customer are taken
// when the order is written, not when it is paid (P0-1 defect D-02).
//
// ValidatePromoForLines and CheckPromoLimits run before any transaction and
// count recorded redemptions only, and a redemption is recorded at payment.
// Two buyers who started a checkout at the same moment with a max_uses=1
// code therefore both got the discount and both paid it, and so did one
// buyer with two checkouts open against a max_uses_per_customer=1 code.
// ReservePromoUseTx closes that gap: inside the transaction that writes the
// order it takes a lock per code, then counts redemptions plus the code's
// orders that were paid or still hold their places. The lock is held until
// that transaction commits, so the second checkout counts the first one's
// order. Nothing is stored: an unpaid order stops counting when its hold
// expires.
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// PromoUse names the order about to carry a code. The buyer is the customer
// or the e-mail, whichever the caller knows: an order matches when either
// one does.
type PromoUse struct {
	PromoCodeID   uuid.UUID
	OrgID         uuid.UUID
	SessionID     *uuid.UUID
	ReservationID *uuid.UUID
	CustomerID    *uuid.UUID
	BuyerEmail    string
}

// ReservePromoUseTx answers the rule this order would break, or "" when the
// code still has a use for it: PromoErrExhausted for max_uses (also for a
// code that is no longer the organization's), PromoErrPerCustomerLimit for
// max_uses_per_customer. q must be bound to the transaction that writes the
// order.
func ReservePromoUseTx(ctx context.Context, q *gen.Queries, use PromoUse) (refusal string, err error) {
	if err := q.LockPromoCodeUses(ctx, use.PromoCodeID); err != nil {
		return "", fmt.Errorf("lock promo code uses: %w", err)
	}
	uses, err := q.CountPromoCodeUsesInFlight(ctx, use.PromoCodeID, use.OrgID,
		use.ReservationID, use.SessionID, use.CustomerID, strings.TrimSpace(use.BuyerEmail))
	if errors.Is(err, pgx.ErrNoRows) {
		return PromoErrExhausted, nil
	}
	if err != nil {
		return "", fmt.Errorf("count promo code uses: %w", err)
	}
	if uses.MaxUses != nil && uses.Used >= *uses.MaxUses {
		return PromoErrExhausted, nil
	}
	if uses.MaxUsesPerCustomer != nil && uses.Mine >= *uses.MaxUsesPerCustomer {
		return PromoErrPerCustomerLimit, nil
	}
	return "", nil
}
