// Package convertjob implements the "checkout.convert_reservation" background
// worker job (feature #383, PR2-27 BLOCKER: close the PR2-04 convert-failure window).
//
// Problem:
//
// PR2-04 wired convertReservationTx (SellReservationSeatsTx + ConfirmCapacity +
// UpdateReservationState('converted')) into the payment webhook path, but it
// runs in its OWN transaction after HandlePaymentIntentWebhook commits. Every
// call site treats its failure as non-fatal (log + continue). So if the convert
// tx fails after the webhook commits, the reservation stays active/draft and the
// TTL worker can still release and resell the paid seats.
//
// Fix (webhook path):
//
// HandlePaymentIntentWebhook now enqueues a "checkout.convert_reservation"
// worker_jobs row inside the same atomic transaction as the idempotency event
// INSERT, the state transition UPDATE, and the checkout.issue_tickets enqueue.
// If the inline convertReservationTx call after the commit succeeds (fast path),
// the job runs as a no-op (idempotent). If the inline call fails, the worker
// picks up the job and retries until conversion succeeds or hits max_attempts.
//
// Fix (checkout complete API path):
//
// completeCheckoutWithPromoTx (checkout_promo_368.go) now always uses a
// transaction and includes convertReservationInTx inside it — making conversion
// atomic with completion. This closes the window completely for the API path.
package convertjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// JobType is the worker job_type string for reservation conversion.
// It is used both when enqueueing the job (in HandlePaymentIntentWebhook's
// atomic transaction) and when registering the handler in arena-worker.
const JobType = "checkout.convert_reservation"

// Payload is the JSON payload stored in worker_jobs.payload for a
// checkout.convert_reservation job.
type Payload struct {
	ReservationID string `json:"reservation_id"`
}

// HandlerOptions bundles the dependencies required by the
// checkout.convert_reservation worker job handler.
type HandlerOptions struct {
	// ConvertFn is the idempotent conversion function. In production this is
	// (*hcheckout.Handler).ConvertReservationTx exposed via a closure in
	// arena-worker. When nil, the handler logs a warning and returns nil (no
	// retry) — callers should always wire a real implementation.
	ConvertFn func(ctx context.Context, reservationID uuid.UUID) error

	// Logger receives structured log output. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewHandler returns a worker handler function for "checkout.convert_reservation" jobs.
//
// The handler:
//  1. Unmarshals the Payload to obtain the reservation_id.
//  2. Calls opts.ConvertFn (convertReservationTx, feature #360) to perform the
//     held→sold conversion. The function is idempotent: already-converted
//     reservations are skipped without error.
//
// Errors returned from the handler cause the worker to retry the job up to the
// configured max_attempts. The job is considered permanently failed when
// max_attempts is exhausted and the row is moved to worker_dead_letter.
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, payload []byte) error {
		var p Payload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("checkout.convert_reservation: unmarshal payload: %w", err)
		}
		reservationID, err := uuid.Parse(p.ReservationID)
		if err != nil {
			return fmt.Errorf("checkout.convert_reservation: invalid reservation_id %q: %w", p.ReservationID, err)
		}

		if opts.ConvertFn == nil {
			logger.Warn("checkout.convert_reservation: ConvertFn not configured; skipping",
				slog.String("reservation_id", reservationID.String()),
			)
			return nil
		}

		if err := opts.ConvertFn(ctx, reservationID); err != nil {
			return fmt.Errorf("checkout.convert_reservation: convert reservation %s: %w", reservationID, err)
		}

		logger.Info("checkout.convert_reservation: reservation converted",
			slog.String("reservation_id", reservationID.String()),
		)
		return nil
	}
}
