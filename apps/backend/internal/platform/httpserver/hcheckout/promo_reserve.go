// promo_reserve.go — a code's max_uses is taken when the order is written,
// not when it is paid (P0-1 defect D-02).
//
// ValidatePromoForLines and CheckPromoLimits run before any transaction and
// count recorded redemptions only, and a redemption is recorded at payment.
// Two buyers who start a checkout at the same moment with a max_uses=1 code
// therefore both got the discount and both paid it. ReservePromoUseTx closes
// that gap: inside the transaction that writes the order it takes a lock
// per code, then counts redemptions plus the code's orders that were paid or
// still hold their places. The lock is held until that transaction commits,
// so the second buyer counts the first buyer's order. Nothing is stored: an
// unpaid order stops counting when its hold expires.
package hcheckout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// PromoUse names the order about to carry a code.
type PromoUse struct {
	PromoCodeID   uuid.UUID
	OrgID         uuid.UUID
	ReservationID *uuid.UUID
	CustomerID    *uuid.UUID
	BuyerEmail    string
}

// ReservePromoUseTx answers true when the code has no use left for this
// order. q must be bound to the transaction that writes the order. A code
// that is not the organization's (deleted meanwhile) counts as exhausted.
func ReservePromoUseTx(ctx context.Context, q *gen.Queries, use PromoUse) (exhausted bool, err error) {
	if err := q.LockPromoCodeUses(ctx, use.PromoCodeID); err != nil {
		return false, fmt.Errorf("lock promo code uses: %w", err)
	}
	maxUses, used, err := q.CountPromoCodeUsesInFlight(ctx, use.PromoCodeID, use.OrgID,
		use.ReservationID, use.CustomerID, use.BuyerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("count promo code uses: %w", err)
	}
	return maxUses != nil && used >= *maxUses, nil
}
