// checkout_hold.go — checkout/start with hold_token (spec 67 section 4.4):
// the seats of a hold taken at the click become the order.
//
// The seats come from the hold, so no new hold is taken and "seat taken" is
// impossible here. Two transactions, as on the classic path:
//
//  1. pricing: the hold row is locked FIRST (AGENTS.md: any change of an
//     existing hold locks its reservations row before anything else), it
//     must be open, of this session, not expired and not closing, without
//     another open checkout; its seats are priced through the transaction's
//     own connection. A refusal leaves the hold as it was.
//  2. confirmation (confirmPublicCheckout): the hold row is locked again and
//     checked again, the payment step is given ONCE (payment_started_at =
//     now, expires_at = now + payment, from the hold's policy snapshot), and
//     the checkout and the order are written; the order copies expires_at
//     from the hold. A later checkout of the same hold keeps expires_at.
//
// A channel whose provider cannot close its page (Capabilities.CanExpirePage
// false: Flitt, AllPay, PayPlus) keeps today's payment step: the channel's
// window plus the grace (spec 67 section 5.5).
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Error codes of the hold path of checkout/start (spec 67 section 4.4).
const (
	ErrCodeHoldAndSeats  = "checkout.hold_and_seats"
	ErrCodeHoldExpired   = "hold.expired"
	ErrCodeHoldInPayment = "hold.in_payment"
	ErrCodeHoldNotFound  = "hold.not_found"
)

// holdCheckout is what the confirmation transaction needs to know about the
// hold behind a checkout/start with hold_token.
type holdCheckout struct {
	reservationID uuid.UUID
	sessionID     uuid.UUID
	policy        holdpolicy.Policy
	// canExpirePage: the channel's provider can close its page, so the
	// payment step is the policy's and the page gets page_expires_at.
	canExpirePage bool
}

// paymentStep is how long the payment step of this hold lasts from its
// first checkout: the policy's payment time, or the channel's window plus the
// grace for a provider that cannot close its page.
func (h *Handler) holdPaymentStep(ctx context.Context, hold *holdCheckout, orgID, channelID uuid.UUID) time.Duration {
	if hold.canExpirePage {
		return hold.policy.Payment
	}
	return h.channelPaymentWindow(ctx, orgID, channelID) + h.paymentGrace
}

// channelCanExpirePage reports whether the channel's payment provider can
// close its payment page itself (spec 67 section 5.5). Read on the pool,
// before any transaction. A failed read answers false: the long window of
// today is the safe side.
func (h *Handler) channelCanExpirePage(ctx context.Context, orgID, channelID uuid.UUID) bool {
	if h.publicFeedQueries == nil {
		return false
	}
	ch, err := h.publicFeedQueries.GetSalesChannelByID(ctx, channelID, orgID)
	if err != nil {
		h.logger.Warn("public_feed_checkout: channel provider lookup failed; the hold keeps the long payment window",
			slog.String("channel_id", channelID.String()), slog.String("error", err.Error()))
		return false
	}
	entry, ok := hostedEntry(ch.Provider)
	return ok && entry.Descriptor.Capabilities.CanExpirePage
}

// lockHoldForCheckout is the first statement of the pricing transaction of
// a hold: the hold row locked, checked, and its seats read. On a refusal it
// has written the answer and reports false.
func (h *Handler) lockHoldForCheckout(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
	txq *gen.Queries, holdToken string, sessionID uuid.UUID,
) (gen.HoldCheckoutRow, []gen.SessionSeatRow, bool) {
	hold, err := txq.LockHoldCheckoutByTokenHash(ctx, holdpolicy.HashHoldToken(holdToken))
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			ErrCodeHoldNotFound, "We cannot find these seats. Choose the seats again.", r,
		))
		return hold, nil, false
	}
	if err != nil {
		h.logger.Error("public_feed_checkout: hold lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.unavailable", "We could not check your seats. Try again in a minute.", r,
		))
		return hold, nil, false
	}
	if hold.SessionID != sessionID {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.hold_session_mismatch", "These seats are held for another date. Choose the seats again.", r,
			map[string]any{"field": "session_id"},
		))
		return hold, nil, false
	}
	if !holdLive(hold, time.Now().UTC()) {
		writeHoldExpired(w, r)
		return hold, nil, false
	}
	if refused := h.refuseHoldInPayment(ctx, w, r, txq, hold.ID); refused {
		return hold, nil, false
	}
	seats, err := txq.ListReservationSeats(ctx, hold.ID)
	if err != nil {
		h.logger.Error("public_feed_checkout: hold seats lookup failed",
			slog.String("reservation_id", hold.ID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.unavailable", "We could not check your seats. Try again in a minute.", r,
		))
		return hold, nil, false
	}
	held := make([]gen.SessionSeatRow, 0, len(seats))
	for _, s := range seats {
		if s.Status == "held" && s.ReservationID != nil && *s.ReservationID == hold.ID {
			held = append(held, s)
		}
	}
	if len(held) == 0 || len(held) != len(seats) {
		// A hold whose seats went back (or were never linked) cannot be paid.
		writeHoldExpired(w, r)
		return hold, nil, false
	}
	return hold, held, true
}

// refuseHoldInPayment answers 409 hold.in_payment when the hold already has
// an open checkout (a second tab, a double press). The same Idempotency-Key
// is answered by the middleware before this runs.
func (h *Handler) refuseHoldInPayment(ctx context.Context, w http.ResponseWriter, r *http.Request, txq *gen.Queries, reservationID uuid.UUID) bool {
	_, _, err := txq.FindOpenCheckoutOfHold(ctx, reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		h.logger.Error("public_feed_checkout: open checkout of the hold lookup failed",
			slog.String("reservation_id", reservationID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.unavailable", "We could not check your seats. Try again in a minute.", r,
		))
		return true
	}
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		ErrCodeHoldInPayment, "These seats are already in a payment. Finish it, or cancel it first.", r,
	))
	return true
}

// holdLive reports whether a hold can still become an order: open, not past
// its expiry, and not being closed by the expiry job.
func holdLive(hold gen.HoldCheckoutRow, now time.Time) bool {
	if hold.State != "draft" && hold.State != "active" {
		return false
	}
	return hold.ClosingStartedAt == nil && hold.ExpiresAt.After(now)
}

func writeHoldExpired(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		ErrCodeHoldExpired, "The time to hold these seats is over. Hold them again.", r,
	))
}

// holdConfirmOutcome is what the confirmation transaction found on the hold.
type holdConfirmOutcome int

const (
	holdConfirmOK holdConfirmOutcome = iota
	holdConfirmExpired
	holdConfirmInPayment
)

// startHoldPaymentStepTx is the first statement of the confirmation
// transaction of a hold: the hold row locked and checked again, the payment
// step given once. It returns the hold's expires_at the checkout and the
// order take.
func (h *Handler) startHoldPaymentStepTx(ctx context.Context, txq *gen.Queries, hold *holdCheckout, step time.Duration, paid bool, now time.Time) (time.Time, holdConfirmOutcome, error) {
	row, err := txq.LockHoldCheckout(ctx, hold.reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, holdConfirmExpired, nil
	}
	if err != nil {
		return time.Time{}, 0, err
	}
	if !holdLive(row, now) {
		return time.Time{}, holdConfirmExpired, nil
	}
	if _, _, err := txq.FindOpenCheckoutOfHold(ctx, row.ID); err == nil {
		return time.Time{}, holdConfirmInPayment, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, 0, err
	}
	if !paid || row.PaymentStartedAt != nil {
		// Once per hold: a reopened order sheet keeps the time it had.
		return row.ExpiresAt, holdConfirmOK, nil
	}
	started, err := txq.StartHoldPaymentStep(ctx, row.ID, now, now.Add(step))
	if err != nil {
		return time.Time{}, 0, err
	}
	return started.ExpiresAt, holdConfirmOK, nil
}

// writeHoldConfirmRefusal answers a hold the confirmation transaction found
// closed or already in a payment.
func writeHoldConfirmRefusal(w http.ResponseWriter, r *http.Request, outcome holdConfirmOutcome) {
	if outcome == holdConfirmInPayment {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			ErrCodeHoldInPayment, "These seats are already in a payment. Finish it, or cancel it first.", r,
		))
		return
	}
	writeHoldExpired(w, r)
}

// holdPaymentWindow is the payment window of a checkout's hold as the
// public routes see it (attach, /paying, status, hosted fallback).
type holdPaymentWindow struct {
	row gen.CheckoutHoldWindowRow
	// short: the hold of the click on a provider that closes its page, so
	// "Pay" is taken until the hold's expires_at, without the grace (spec 67
	// section 3.3).
	short bool
}

// checkoutHoldWindow reads the hold window of a checkout. found=false on the
// old path's absence of the row or a failed read (the old rules then apply).
func (h *Handler) checkoutHoldWindow(ctx context.Context, q *gen.Queries, checkoutSessionID uuid.UUID) (holdPaymentWindow, bool) {
	if q == nil {
		return holdPaymentWindow{}, false
	}
	row, err := q.GetCheckoutHoldWindow(ctx, checkoutSessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_checkout: hold window lookup failed; the old payment window applies",
				slog.String("checkout_session_id", checkoutSessionID.String()), slog.String("error", err.Error()))
		}
		return holdPaymentWindow{}, false
	}
	return holdPaymentWindow{row: row, short: row.HasHoldToken && row.PageExpiresAt != nil}, true
}

// grace is what is taken off the hold's expires_at for the end of "Pay".
func (w holdPaymentWindow) grace(def time.Duration) time.Duration {
	if w.short {
		return 0
	}
	return def
}

// newHoldPageExpiry is the page expiry of a new provider page of a hold
// created now (spec 67 section 3.3).
func newHoldPageExpiry(p holdpolicy.Policy, now time.Time) time.Time {
	return now.Add(p.PageLifetime())
}

// recordPageExpiry writes page_expires_at on the payment attempt of a page
// just created for a hold. Best effort: the page exists and the buyer can pay
// on it; a missing value only keeps the old rules for this checkout.
func (h *Handler) recordPageExpiry(ctx context.Context, q *gen.Queries, intentID uuid.UUID, pageExpiresAt time.Time) {
	if q == nil || pageExpiresAt.IsZero() {
		return
	}
	if _, err := q.SetPaymentIntentPageExpiresAt(ctx, intentID, pageExpiresAt); err != nil {
		h.logger.Warn("public_feed_checkout: recording the page expiry of the payment attempt failed",
			slog.String("payment_intent_id", intentID.String()), slog.String("error", err.Error()))
	}
}
