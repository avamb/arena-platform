// checkout_promo_368.go — PR2-12: enforce promo max_uses and record redemptions
// atomically at checkout completion (feature #368).
//
// Problem fixed:
//   - applyPromoCode checked status/window/min-amount but never consulted the
//     promo_code_redemptions table, so a single-use 100%-off code was infinitely
//     redeemable.
//   - Redemptions were never recorded; the promo_code_redemptions table was always empty.
//
// This file adds:
//   - completeCheckoutWithPromoTx: wraps the checkout-complete query and the
//     redemption INSERT in a single database transaction with a SELECT … FOR UPDATE
//     on the promo code row, so concurrent completions serialise and the second
//     caller is rejected once max_uses is hit.
//   - errPromoExhausted / errPromoPerCustomerLimit: sentinel errors returned when
//     the limit is hit after acquiring the row lock; the caller must NOT write an
//     additional HTTP response when it receives one of these.
package hcheckout

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// errPromoExhausted is returned by completeCheckoutWithPromoTx when the promo
// code's max_uses limit is reached after the row lock is acquired.
// The HTTP 409 response has already been written; the caller must return immediately.
var errPromoExhausted = errors.New("promo code exhausted")

// errPromoPerCustomerLimit is returned by completeCheckoutWithPromoTx when the
// per-customer redemption limit is reached after the row lock is acquired.
// The HTTP 409 response has already been written; the caller must return immediately.
var errPromoPerCustomerLimit = errors.New("promo code per-customer limit reached")

// completeCheckoutWithPromoTx atomically completes a checkout session and records a
// promo code redemption when a promo code was applied to the session.
//
// When promoCodeID is nil, or when h.promoQueries / h.pool are unavailable, the
// function falls back to the plain completion path (existing behaviour, no tx started).
//
// Concurrency guarantee (when promoCodeID != nil):
//  1. BEGIN transaction
//  2. SELECT promo_codes WHERE id=promoCodeID FOR UPDATE
//     → row-level lock serialises all concurrent calls for the same promo code
//  3. COUNT existing redemptions; if >= max_uses → write 409, return errPromoExhausted
//  4. COUNT user-specific redemptions (when userID != nil and max_uses_per_customer set)
//     → if >= max_uses_per_customer → write 409, return errPromoPerCustomerLimit
//  5. Complete the checkout session (free or paid path) via completeFn within the tx
//  6. INSERT promo_code_redemptions row (non-fatal on failure — checkout is already done)
//  7. COMMIT
//
// completeFn is a closure that accepts a *gen.Queries (which may wrap a pgx.Tx)
// and executes either CompleteFreeCheckoutSession or CompleteCheckoutSession.
// Returning pgx.ErrNoRows from completeFn signals an invalid state transition (409).
func (h *Handler) completeCheckoutWithPromoTx(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	promoCodeID *uuid.UUID,
	userID *uuid.UUID,
	reservationID uuid.UUID,
	discountAmount, orderAmount int64,
	completeFn func(txQ *gen.Queries) (gen.CheckoutSessionRow, error),
) (gen.CheckoutSessionRow, error) {
	// ── No promo code or no pool: fall back to plain completion ──────────────
	if promoCodeID == nil || h.promoQueries == nil || h.pool == nil {
		return completeFn(h.checkoutQueries)
	}

	// ── Start a transaction for atomic lock + complete + insert ───────────────
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gen.CheckoutSessionRow{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txQ := gen.New(tx)

	// ── Step 2: Lock promo code row (FOR UPDATE) ──────────────────────────────
	pc, err := txQ.GetPromoCodeByIDForUpdate(ctx, *promoCodeID)
	promoFound := true
	if errors.Is(err, pgx.ErrNoRows) {
		// Promo code was deleted between confirm and complete — proceed without
		// the lock (no limits to check, no redemption to record).
		promoFound = false
	} else if err != nil {
		return gen.CheckoutSessionRow{}, err
	}

	if promoFound {
		// ── Step 3: Enforce total redemption limit ────────────────────────────
		if pc.MaxUses != nil {
			count, err := txQ.CountPromoCodeRedemptions(ctx, pc.ID)
			if err != nil {
				return gen.CheckoutSessionRow{}, err
			}
			if count >= *pc.MaxUses {
				_ = tx.Rollback(ctx)
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"promo.exhausted",
					"this promo code has reached its maximum number of uses",
					r,
				))
				return gen.CheckoutSessionRow{}, errPromoExhausted
			}
		}

		// ── Step 4: Enforce per-customer limit ────────────────────────────────
		if pc.MaxUsesPerCustomer != nil && userID != nil {
			userCount, err := txQ.CountUserRedemptions(ctx, pc.ID, *userID)
			if err != nil {
				return gen.CheckoutSessionRow{}, err
			}
			if userCount >= *pc.MaxUsesPerCustomer {
				_ = tx.Rollback(ctx)
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"promo.per_customer_limit",
					"you have already used this promo code the maximum number of times",
					r,
				))
				return gen.CheckoutSessionRow{}, errPromoPerCustomerLimit
			}
		}
	}

	// ── Step 5: Complete the checkout session within the transaction ──────────
	cs, err := completeFn(txQ)
	if err != nil {
		return gen.CheckoutSessionRow{}, err
	}

	// ── Step 6: Insert redemption record (non-fatal on failure) ──────────────
	if promoFound {
		rid := reservationID
		if _, insErr := txQ.InsertPromoCodeRedemption(ctx, pc.ID, userID, &rid, discountAmount, orderAmount); insErr != nil {
			h.logger.Error("promo: insert redemption failed (checkout completion will still succeed)",
				slog.String("promo_code_id", pc.ID.String()),
				slog.String("checkout_reservation_id", rid.String()),
				slog.String("error", insErr.Error()),
			)
			// Non-fatal: the checkout state is already updated; proceed to COMMIT.
		}
	}

	// ── Step 7: Commit ────────────────────────────────────────────────────────
	if err := tx.Commit(ctx); err != nil {
		return gen.CheckoutSessionRow{}, err
	}
	return cs, nil
}
