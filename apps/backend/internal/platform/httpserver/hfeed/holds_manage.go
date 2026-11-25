// holds_manage.go — the routes of one hold, addressed by its token (spec
// 08_architecture/67, sections 4.2, 4.3 and 4.7):
//
//	DELETE /v1/public/holds/{hold_token}/seats/{seat_key}  remove one seat
//	DELETE /v1/public/holds/{hold_token}                   remove every seat
//	GET    /v1/public/holds/{hold_token}                   read the hold
//	POST   /v1/public/holds/{hold_token}/extend            extend once
//
// Only the token addresses a hold: an unknown token is 404 hold.not_found
// whoever's it is, so nothing leaks (section 5.4). Every change locks the
// hold row first, then the hcheckout helpers take sessions → seats → ledger.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// holdRoute runs the common head of a token route: the switch, the rate
// limit (the checkout-token bucket keyed by the hold token, plus the address
// bucket) and the token. It answers and returns "" when the request stops.
func (hh *HoldsHandler) holdRoute(w http.ResponseWriter, r *http.Request) string {
	if !hh.ready(w, r) {
		return ""
	}
	token := chi.URLParam(r, "hold_token")
	if !hh.h.enforceRateLimit(w, r, "hold.rate_limited", hh.h.rl.CheckCheckoutToken, token) {
		return ""
	}
	if token == "" {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("hold.not_found", "We cannot find this hold.", r))
		return ""
	}
	return token
}

// inHoldTx runs fn in a transaction, retried as a whole on 40P01/40001.
func (hh *HoldsHandler) inHoldTx(ctx context.Context, fn func(q *gen.Queries) error) error {
	return txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := hh.h.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("begin hold tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := fn(hh.h.reservationQueries.WithTx(tx)); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit hold tx: %w", err)
		}
		return nil
	})
}

// lockHoldTx locks the hold of a token; errHoldNotFound when there is none.
func lockHoldTx(ctx context.Context, q *gen.Queries, token string) (gen.PublicHoldRow, error) {
	row, err := q.LockPublicHoldByTokenHash(ctx, holdTokenHash(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.PublicHoldRow{}, errHoldNotFound
		}
		return gen.PublicHoldRow{}, fmt.Errorf("lock hold: %w", err)
	}
	return row, nil
}

// closedHoldRefusal answers a change of a hold that is no longer open: a
// cancelled hold answers its (empty) composition, so a repeated removal is
// 200; an expired or converted one is 409 hold.expired.
func closedHoldRefusal(row gen.PublicHoldRow) error {
	if row.State == "cancelled" {
		return nil
	}
	return errHoldExpired
}

// HandleReleaseHoldSeat serves DELETE /v1/public/holds/{hold_token}/seats/{seat_key}.
func (hh *HoldsHandler) HandleReleaseHoldSeat(w http.ResponseWriter, r *http.Request) {
	token := hh.holdRoute(w, r)
	if token == "" {
		return
	}
	seatKey, err := url.PathUnescape(chi.URLParam(r, "seat_key"))
	if err != nil || seatKey == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("hold.invalid_seats", "Send the seat_key in the path.", r))
		return
	}
	hh.changeHold(w, r, token, func(ctx context.Context, q *gen.Queries, row gen.PublicHoldRow) error {
		seats, err := q.ListReservationSeats(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("list hold seats: %w", err)
		}
		held := false
		for _, s := range seats {
			if s.SeatKey == seatKey {
				held = true
				break
			}
		}
		if !held {
			return nil // already free: the same answer as after the removal
		}
		res, err := hcheckout.ShrinkHoldTx(ctx, q, hcheckout.HoldMutationInput{
			ReservationID: row.ID, SeatKeys: []string{seatKey},
		})
		if err != nil {
			return err
		}
		if res.Cancelled {
			return nil
		}
		left, err := q.ListReservationSeats(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("list hold seats: %w", err)
		}
		return q.SetPublicHoldSeatKeys(ctx, row.ID, seatKeysOf(left))
	})
}

// HandleReleaseHold serves DELETE /v1/public/holds/{hold_token}.
func (hh *HoldsHandler) HandleReleaseHold(w http.ResponseWriter, r *http.Request) {
	token := hh.holdRoute(w, r)
	if token == "" {
		return
	}
	hh.changeHold(w, r, token, func(ctx context.Context, q *gen.Queries, row gen.PublicHoldRow) error {
		if _, err := hcheckout.ReleaseHoldTx(ctx, q, row.ID); err != nil {
			return err
		}
		return nil
	})
}

// changeHold locks the hold, refuses a closed hold or one on the payment
// step, runs change and answers the hold as it stands after it.
func (hh *HoldsHandler) changeHold(w http.ResponseWriter, r *http.Request, token string,
	change func(ctx context.Context, q *gen.Queries, row gen.PublicHoldRow) error,
) {
	ctx := r.Context()
	now := hh.now()
	var answer HoldAnswer
	err := hh.inHoldTx(ctx, func(q *gen.Queries) error {
		row, err := lockHoldTx(ctx, q, token)
		if err != nil {
			return err
		}
		if holdOpen(row.State) {
			inPayment, err := q.PublicHoldHasOpenOrder(ctx, row.ID)
			if err != nil {
				return fmt.Errorf("open order lookup: %w", err)
			}
			if inPayment {
				return errHoldInPayment
			}
			if err := change(ctx, q, row); err != nil {
				return mapReleaseError(err)
			}
			if row, err = lockHoldTx(ctx, q, token); err != nil {
				return err
			}
		} else if err := closedHoldRefusal(row); err != nil {
			return err
		}
		seats, err := readHoldSeats(ctx, q, row)
		if err != nil {
			return fmt.Errorf("list hold seats: %w", err)
		}
		if !holdOpen(row.State) {
			seats = nil
		}
		answer = buildHoldAnswer(row, token, seats, false, now)
		return nil
	})
	if err != nil {
		hh.writeHoldFailure(w, r, "release", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, answer)
}

// mapReleaseError turns a concurrent close into hold.expired.
func mapReleaseError(err error) error {
	var notMutable *hcheckout.NotMutableError
	var notReleasable *hcheckout.NotReleasableError
	if errors.As(err, &notMutable) || errors.As(err, &notReleasable) || errors.Is(err, hcheckout.ErrHoldNotFound) {
		return errHoldExpired
	}
	return err
}

// HandleGetHold serves GET /v1/public/holds/{hold_token}: the answer of
// section 4.1 plus checkout_token when an order waits for payment.
func (hh *HoldsHandler) HandleGetHold(w http.ResponseWriter, r *http.Request) {
	token := hh.holdRoute(w, r)
	if token == "" {
		return
	}
	ctx := r.Context()
	q := hh.h.reservationQueries
	row, err := q.GetPublicHoldByTokenHash(ctx, holdTokenHash(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = errHoldNotFound
		}
		hh.writeHoldFailure(w, r, "read", err)
		return
	}
	seats, err := readHoldSeats(ctx, q, row)
	if err != nil {
		hh.writeHoldFailure(w, r, "read", err)
		return
	}
	var inPayment bool
	var checkoutToken *string
	if holdOpen(row.State) {
		checkoutToken, err = q.GetPublicHoldCheckoutToken(ctx, row.ID)
		switch {
		case err == nil:
			inPayment = true
		case errors.Is(err, pgx.ErrNoRows):
			checkoutToken = nil
		default:
			hh.writeHoldFailure(w, r, "read", err)
			return
		}
	}
	answer := buildHoldAnswer(row, token, seats, inPayment, hh.now())
	answer.CheckoutToken = checkoutToken
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, answer)
}

// HandleExtendHold serves POST /v1/public/holds/{hold_token}/extend: +extend
// once per hold, on either step, only within the last 75 seconds and never
// after the expiry job began closing the payment page (section 4.7). On the
// payment step the order waiting for payment gets the same expiry in the
// same transaction.
func (hh *HoldsHandler) HandleExtendHold(w http.ResponseWriter, r *http.Request) {
	token := hh.holdRoute(w, r)
	if token == "" {
		return
	}
	ctx := r.Context()
	now := hh.now()
	var answer HoldAnswer
	err := hh.inHoldTx(ctx, func(q *gen.Queries) error {
		row, err := lockHoldTx(ctx, q, token)
		if err != nil {
			return err
		}
		if !holdOpen(row.State) || row.ClosingStartedAt != nil || !row.ExpiresAt.After(now) {
			return errHoldExpired
		}
		pol := holdPolicyOf(row.HoldPolicy)
		if pol.Extend <= 0 || int(row.ExtensionsUsed) >= pol.ExtendCount {
			return refuse(http.StatusConflict, "hold.extend_used", "This hold has no extension left.", "", nil)
		}
		if row.ExpiresAt.Sub(now) > holdExtendWindow {
			return refuse(http.StatusConflict, "hold.extend_not_yet",
				"You can extend the hold during its last minute.", "",
				map[string]any{"available_at": row.ExpiresAt.Add(-holdExtendWindow).UTC().Format(time.RFC3339)})
		}
		inPayment, err := q.PublicHoldHasOpenOrder(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("open order lookup: %w", err)
		}
		newExpiry := row.ExpiresAt.Add(pol.Extend)
		updated, err := q.ExtendPublicHold(ctx, row.ID, newExpiry, !inPayment)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errHoldExpired
			}
			return fmt.Errorf("extend hold: %w", err)
		}
		if inPayment {
			if _, err := q.SetPendingOrderExpiryForReservation(ctx, row.ID, newExpiry); err != nil {
				return fmt.Errorf("extend order: %w", err)
			}
		}
		seats, err := readHoldSeats(ctx, q, updated)
		if err != nil {
			return fmt.Errorf("list hold seats: %w", err)
		}
		answer = buildHoldAnswer(updated, token, seats, inPayment, now)
		return nil
	})
	if err != nil {
		hh.writeHoldFailure(w, r, "extend", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, answer)
}
