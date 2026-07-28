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
	"fmt"
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

// completeCheckoutWithPromoTx atomically completes a checkout session, records a
// promo code redemption (when a promo code was applied), and converts the
// reservation (held→sold) — all within a single database transaction.
//
// When h.pool is nil (unit-test environments only), the function falls back to
// the plain completion path via completeFn(h.checkoutQueries) without a
// transaction and without conversion (pool is required for both).
//
// PR2-27 (feature #383): previously, when promoCodeID was nil, the function
// skipped the transaction entirely. This created a window where the checkout was
// marked 'completed' but the reservation remained 'active', allowing the TTL
// worker to expire and resell paid seats. Now completion and conversion always
// commit together when pool is available, permanently closing that window.
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
//  7. convertReservationInTx: held seats → sold, capacity_held → capacity_sold,
//     reservation.state → 'converted' — all within the same transaction
//  8. COMMIT: completion + conversion are now durable together
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
	// ── No pool: fall back to plain completion without tx ─────────────────────
	// This path is taken only in unit-test environments where pool is nil.
	// It does NOT include conversion (pool required) — unit tests that need
	// conversion should use a real or stubbed pool.
	if h.pool == nil {
		return completeFn(h.checkoutQueries)
	}

	// ── PR2-27: Always use a transaction for atomic completion + conversion ────
	// Previously, the no-promo path skipped the transaction entirely, creating a
	// window where the checkout was marked 'completed' but the reservation was
	// still 'active'. The TTL worker could expire and resell the paid seats.
	// Now completion and conversion always commit together, closing the window.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gen.CheckoutSessionRow{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txQ := gen.New(tx)

	// ── Promo code enforcement (only when a promo code was applied) ───────────
	var (
		promoFound bool
		pc         gen.PromoCodeRow // zero value when !promoFound
	)
	if promoCodeID != nil && h.promoQueries != nil {
		var promoErr error
		pc, promoErr = txQ.GetPromoCodeByIDForUpdate(ctx, *promoCodeID)
		if errors.Is(promoErr, pgx.ErrNoRows) {
			// Promo deleted between confirm and complete — proceed without lock.
			promoFound = false
		} else if promoErr != nil {
			return gen.CheckoutSessionRow{}, promoErr
		} else {
			promoFound = true
		}

		if promoFound {
			// Enforce total redemption limit.
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

			// Enforce per-customer limit.
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
	}

	// ── Complete the checkout session ─────────────────────────────────────────
	cs, err := completeFn(txQ)
	if err != nil {
		return gen.CheckoutSessionRow{}, err
	}

	// ── Record promo redemption (when promo was applied) ─────────────────────
	if promoFound {
		rid := reservationID
		if _, insErr := txQ.InsertPromoCodeRedemption(ctx, pc.ID, userID, &rid, discountAmount, orderAmount); insErr != nil {
			h.logger.Error("promo: insert redemption failed (checkout completion will still succeed)",
				slog.String("promo_code_id", pc.ID.String()),
				slog.String("checkout_reservation_id", rid.String()),
				slog.String("error", insErr.Error()),
			)
			// Non-fatal: the checkout state is already updated; proceed to conversion.
		}
	}

	// ── PR2-27: Convert reservation atomically with completion ────────────────
	// convertReservationInTx sets reservation.state='converted', sells held
	// seats, and moves capacity_held→capacity_sold — all within this transaction.
	// Once committed, GetExpiredReservations (which filters state IN
	// ('draft','active')) cannot select this reservation, permanently closing
	// the held→sold window described in PR2-04 follow-up.
	//
	// If conversion fails, the whole transaction rolls back. The checkout session
	// stays in 'pricing_confirmed' state and the caller returns an error to the
	// client. Seats remain held and safe. The client can retry the completion.
	if convErr := h.convertReservationInTx(ctx, txQ, reservationID); convErr != nil {
		h.logger.Error("completeCheckoutWithPromoTx: conversion failed; rolling back completion",
			slog.String("reservation_id", reservationID.String()),
			slog.String("error", convErr.Error()),
		)
		return gen.CheckoutSessionRow{}, fmt.Errorf("completeCheckoutWithPromoTx: reservation conversion failed (checkout rolled back): %w", convErr)
	}

	// ── Commit: completion + conversion are now durable together ──────────────
	if err := tx.Commit(ctx); err != nil {
		return gen.CheckoutSessionRow{}, err
	}
	return cs, nil
}
