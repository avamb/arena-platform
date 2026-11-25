// checkout_hold_page.go — recording a new provider payment page of a seat
// hold of the click (spec 67 sections 3.2 and 5.2).
//
// Two rules keep a page from outliving the hold it is for:
//
//   - the payment attempt and its page_expires_at are ONE row written by ONE
//     statement, so no attempt of a hold exists without its page expiry (the
//     reservation sweep and hold.close_payment rely on it);
//   - the attempt is recorded in a transaction that locks the hold's
//     reservations row first and checks again that the hold is open, not
//     past expires_at and not being closed by hold.close_payment
//     (closing_started_at IS NULL). If it is, nothing is recorded, the page
//     just created is expired at the provider and the buyer gets 409
//     hold.expired. hold.close_payment's Release takes the same row lock and
//     refuses to release a hold with an open attempt it did not close, so
//     the two never miss each other.
//
// No page is created at all when less than holdPageMinRemaining is left on
// the hold.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// holdPageMinRemaining is the least time left on a hold for which a new
// provider page is created.
const holdPageMinRemaining = 60 * time.Second

// errHoldClosedForPage: the hold was closed, expired or claimed by
// hold.close_payment before the new page could be recorded.
var errHoldClosedForPage = errors.New("hfeed: the hold is closed or being closed; the new payment page is not recorded")

// holdTooLateForPage reports whether too little is left on a hold for a new
// provider page.
func holdTooLateForPage(expiresAt, now time.Time) bool {
	return expiresAt.Sub(now) < holdPageMinRemaining
}

// pageAttempt is the payment_intents row of a provider page just created.
type pageAttempt struct {
	// holdReservationID is the hold of the click the page is for; uuid.Nil
	// on the old path (no lock, no check).
	holdReservationID uuid.UUID
	checkoutSessionID uuid.UUID
	orgID             uuid.UUID
	provider          string
	providerPaymentID string
	amount            int64
	currency          string
	// hostedURL is the hosted page; "" for an embedded session.
	hostedURL string
	// pageExpiresAt is written to page_expires_at; zero writes NULL.
	pageExpiresAt time.Time
	// supersededID, when set, is the open attempt this page replaces (the
	// hosted fallback): marked superseded in the same transaction.
	supersededID uuid.UUID
	// reuseExisting: a row already recorded for providerPaymentID (a retry
	// after a lost answer) is reused instead of inserted again.
	reuseExisting bool
}

// newPageAttempt is the attempt of a page created by checkout/start or a
// prepared order.
func newPageAttempt(in hostedPaymentInput, provider, providerPaymentID, hostedURL string) pageAttempt {
	a := pageAttempt{
		checkoutSessionID: in.Session.ID,
		orgID:             in.CheckCtx.OrgID,
		provider:          provider,
		providerPaymentID: providerPaymentID,
		amount:            in.Breakdown.Total,
		currency:          in.Breakdown.Currency,
		hostedURL:         hostedURL,
		pageExpiresAt:     in.PageExpiresAt,
	}
	if in.KeepHold {
		a.holdReservationID = in.Session.ReservationID
	}
	return a
}

// recordPageAttempt writes the attempt (see the file comment). It returns
// errHoldClosedForPage when the hold can no longer take a page; any other
// error is a failed write.
func (h *Handler) recordPageAttempt(ctx context.Context, a pageAttempt) (gen.PaymentIntentRow, error) {
	if h.pool == nil {
		if h.checkoutQueries == nil {
			return gen.PaymentIntentRow{}, errors.New("hfeed: no database to record the payment attempt")
		}
		// No transaction to lock in (unit wiring): the single INSERT still
		// carries the page expiry.
		return insertPageAttempt(ctx, h.checkoutQueries, a)
	}
	var pi gen.PaymentIntentRow
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("begin payment attempt tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := gen.New(tx)
		if a.holdReservationID != uuid.Nil {
			// The reservations row first: the lock order of an existing hold.
			row, err := q.LockHoldCheckout(ctx, a.holdReservationID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errHoldClosedForPage
			}
			if err != nil {
				return fmt.Errorf("lock hold: %w", err)
			}
			if !holdLive(row, time.Now().UTC()) {
				return errHoldClosedForPage
			}
		}
		found := false
		if a.reuseExisting {
			existing, err := q.GetPaymentIntentByProviderID(ctx, a.providerPaymentID)
			switch {
			case err == nil:
				pi, found = existing, true
			case !errors.Is(err, pgx.ErrNoRows):
				return fmt.Errorf("payment attempt lookup: %w", err)
			}
		}
		if !found {
			if pi, err = insertPageAttempt(ctx, q, a); err != nil {
				return err
			}
		}
		if a.supersededID != uuid.Nil {
			if _, err := q.MarkPaymentIntentSuperseded(ctx, a.supersededID); err != nil {
				return fmt.Errorf("mark superseded: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit payment attempt: %w", err)
		}
		return nil
	})
	return pi, err
}

func insertPageAttempt(ctx context.Context, q *gen.Queries, a pageAttempt) (gen.PaymentIntentRow, error) {
	var page *time.Time
	if !a.pageExpiresAt.IsZero() {
		p := a.pageExpiresAt.UTC()
		page = &p
	}
	csID := a.checkoutSessionID
	if a.hostedURL == "" {
		return q.InsertEmbeddedPaymentIntentWithPage(ctx, &csID, a.orgID, a.provider, a.providerPaymentID,
			a.amount, a.currency, page)
	}
	return q.InsertHostedPaymentIntentWithPage(ctx, &csID, a.orgID, a.provider, a.providerPaymentID,
		a.amount, a.currency, a.hostedURL, page)
}

// refuseClosedHoldPage expires at the provider a page that was created for a
// hold closed meanwhile (never handed out) and answers 409 hold.expired.
func (h *Handler) refuseClosedHoldPage(ctx context.Context, w http.ResponseWriter, r *http.Request,
	orgID, channelID, checkoutSessionID uuid.UUID, provider, providerPaymentID string,
) {
	h.logger.Info("public_checkout: the hold closed before its new payment page was recorded; the page is expired",
		slog.String("checkout_session_id", checkoutSessionID.String()),
		slog.String("provider_payment_id", providerPaymentID))
	h.expireUnrecordedPage(ctx, orgID, channelID, provider, providerPaymentID)
	writeHoldExpired(w, r)
}

// expireUnrecordedPage closes a provider page arena did not record. Best
// effort: a page that cannot be closed dies at its own expiry, and nothing
// in arena points at it.
func (h *Handler) expireUnrecordedPage(ctx context.Context, orgID, channelID uuid.UUID, provider, providerPaymentID string) {
	es, ok := h.paymentStarter.(EmbeddedPaymentStarter)
	if !ok {
		h.logger.Warn("public_checkout: the unrecorded payment page cannot be closed here; it ends at its own expiry",
			slog.String("provider_payment_id", providerPaymentID))
		return
	}
	if err := es.ExpireEmbeddedCheckout(ctx, orgID, channelID, provider, providerPaymentID); err != nil &&
		!errors.Is(err, payments.ErrCheckoutSessionNotExpirable) {
		h.logger.Warn("public_checkout: closing the unrecorded payment page failed; it ends at its own expiry",
			slog.String("provider_payment_id", providerPaymentID),
			slog.String("error", err.Error()))
	}
}
