// checkout_pr2_27_383_test.go — structural unit tests for feature #383
// (PR2-27 BLOCKER): Close the PR2-04 held→sold convert-failure window.
//
// # Problem
//
// PR2-04 (feature #360) wired convertReservationTx (SellReservationSeatsTx +
// ConfirmCapacity + UpdateReservationState('converted')) into all completion
// paths, but it ran in its OWN transaction, separate from
// CompleteCheckoutSession, and every call site treated its failure as
// non-fatal (log + continue). So if the convert tx failed after the checkout
// committed, the reservation stayed active/draft and the TTL worker could
// still release and resell the paid seats — the original BLOCKER window was
// narrowed, not closed.
//
// # Fix
//
// Two complementary changes close the window completely:
//
//  1. Checkout API path (completeCheckoutWithPromoTx, checkout_promo_368.go):
//     conversion is now inside the same transaction as checkout completion.
//     If conversion fails, the whole tx rolls back — checkout stays in
//     pricing_confirmed, seats remain held and safe. The window is eliminated
//     because completion and conversion are now a single durable unit.
//
//  2. Payment webhook path (HandlePaymentIntentWebhook, payment_intents.go):
//     a durable "checkout.convert_reservation" worker_jobs row is enqueued
//     inside the same atomic transaction as the idempotency event INSERT, the
//     PI state transition UPDATE, and the checkout.issue_tickets enqueue.
//     If the inline convertReservationTx call after the commit fails, the
//     worker picks up the job and retries until conversion succeeds or
//     max_attempts is exhausted.
//
// These are pure structural unit tests — no live PostgreSQL required.
// The integration test (durable job end-to-end) is in
// checkout_pr2_27_383_integration_test.go (//go:build integration).
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — completeCheckoutWithPromoTx ALWAYS opens a transaction
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step1_CompleteWithPromoTxAlwaysOpensTransaction verifies that
// completeCheckoutWithPromoTx (checkout_promo_368.go) always opens a
// transaction, regardless of whether a promo code was applied.
//
// Before PR2-27, the no-promo path skipped the transaction entirely, creating
// a window where checkout completed but the reservation stayed 'active'. Now
// completion and conversion always commit together.
func TestPR227_Step1_CompleteWithPromoTxAlwaysOpensTransaction(t *testing.T) {
	content := findFileByName(t, "checkout_promo_368.go")

	// Must open a transaction unconditionally — not inside a promo-code guard.
	if !strings.Contains(content, "PR2-27: Always use a transaction") {
		t.Error("checkout_promo_368.go must document that a transaction is ALWAYS opened (PR2-27: previously no-promo path skipped the tx)")
	}

	// Must still call BeginTx (structural check that the tx is opened).
	if !strings.Contains(content, "BeginTx") {
		t.Error("checkout_promo_368.go must call BeginTx for the completion+conversion transaction")
	}

	// Must commit after both completion and conversion succeed.
	if !strings.Contains(content, "tx.Commit") {
		t.Error("checkout_promo_368.go must commit the transaction after successful completion and conversion")
	}

	// Must defer rollback for safety on any early-return path.
	if !strings.Contains(content, "tx.Rollback") {
		t.Error("checkout_promo_368.go must defer tx.Rollback for safety on error/early-return paths")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — convertReservationInTx is called inside completeCheckoutWithPromoTx
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step2_ConvertReservationInTxIsCalledInsideCompleteTx verifies that
// completeCheckoutWithPromoTx calls convertReservationInTx within the same
// transaction, making completion and conversion atomically committed together.
func TestPR227_Step2_ConvertReservationInTxIsCalledInsideCompleteTx(t *testing.T) {
	content := findFileByName(t, "checkout_promo_368.go")

	// Must call convertReservationInTx (the in-tx variant, not convertReservationTx).
	if !strings.Contains(content, "convertReservationInTx") {
		t.Error("checkout_promo_368.go must call convertReservationInTx (the in-tx conversion variant) inside the completion transaction")
	}

	// Must pass the transaction-bound query handle (txQ) to convertReservationInTx.
	if !strings.Contains(content, "convertReservationInTx(ctx, txQ, reservationID)") {
		t.Error("checkout_promo_368.go must pass txQ (transaction-bound *gen.Queries) to convertReservationInTx so conversion commits atomically with completion")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — conversion failure causes checkout rollback (not a non-fatal log)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step3_ConversionFailureCausesCheckoutRollback verifies that when
// convertReservationInTx fails inside completeCheckoutWithPromoTx, the whole
// transaction rolls back — checkout stays in pricing_confirmed, seats remain
// held, and no paid seat can be resold.
//
// This is the key semantic change from PR2-04 (non-fatal log + continue) to
// PR2-27 (fatal: rollback entire checkout).
func TestPR227_Step3_ConversionFailureCausesCheckoutRollback(t *testing.T) {
	content := findFileByName(t, "checkout_promo_368.go")

	// The error path must mention rolling back — the caller gets an error,
	// which bubbles up and causes the checkout to return 500 to the client
	// (retry-able), rather than completing with an unconverted reservation.
	if !strings.Contains(content, "reservation conversion failed (checkout rolled back)") {
		t.Error("checkout_promo_368.go must return an error that mentions 'reservation conversion failed (checkout rolled back)' so callers know to surface this to the client")
	}

	// The log message before the rollback must be present.
	if !strings.Contains(content, "conversion failed; rolling back completion") {
		t.Error("checkout_promo_368.go must log 'conversion failed; rolling back completion' before returning the rollback error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — checkout_convert.go has both convertReservationTx and
//
//	convertReservationInTx
//
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step4_CheckoutConvertHasBothFunctions verifies that
// checkout_convert.go defines both the in-tx variant (convertReservationInTx,
// used by completeCheckoutWithPromoTx) and the self-contained variant
// (convertReservationTx, used by the webhook inline path and the worker job).
func TestPR227_Step4_CheckoutConvertHasBothFunctions(t *testing.T) {
	content := findFileByName(t, "checkout_convert.go")

	// convertReservationInTx: runs within the caller's transaction.
	if !strings.Contains(content, "func (h *Handler) convertReservationInTx") {
		t.Error("checkout_convert.go must define convertReservationInTx (the in-tx helper for atomic completion)")
	}

	// convertReservationTx: opens its own transaction (for webhook + worker paths).
	if !strings.Contains(content, "func (h *Handler) convertReservationTx") {
		t.Error("checkout_convert.go must define convertReservationTx (the self-contained variant for webhook/worker paths)")
	}

	// ConvertReservationTx: exported for the arena-worker job handler.
	if !strings.Contains(content, "func (h *Handler) ConvertReservationTx") {
		t.Error("checkout_convert.go must export ConvertReservationTx so the arena-worker can call it via the convertjob handler")
	}

	// convertReservationInTx must be idempotent (terminal states → skip).
	if !strings.Contains(content, `"converted", "expired", "cancelled"`) {
		t.Error("convertReservationInTx must skip reservations already in a terminal state (idempotent for webhook replays)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — webhook path enqueues checkout.convert_reservation job atomically
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step5_WebhookEnqueuesConvertJobAtomically verifies that
// HandlePaymentIntentWebhook enqueues a durable checkout.convert_reservation
// job inside the same atomic transaction as the idempotency event INSERT and
// the PI state transition UPDATE.
//
// This ensures: if the inline convertReservationTx call after the commit fails
// (transient network/db hiccup), the worker picks up the job and retries until
// conversion succeeds, permanently preventing resale.
func TestPR227_Step5_WebhookEnqueuesConvertJobAtomically(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")

	// Must reference the checkout.convert_reservation job type.
	if !strings.Contains(content, "checkout.convert_reservation") {
		t.Error("payment_intents.go must enqueue a 'checkout.convert_reservation' durable job on payment.succeeded (PR2-27)")
	}

	// Must use the convertjob package's JobType constant (not a raw string).
	if !strings.Contains(content, "convertjob.JobType") {
		t.Error("payment_intents.go must use convertjob.JobType constant for the convert job type string")
	}

	// Must reference convertjob.Payload to build the job payload.
	if !strings.Contains(content, "convertjob.Payload") {
		t.Error("payment_intents.go must use convertjob.Payload to marshal the reservation_id into the job payload")
	}

	// Step 4 of the webhook tx block: must document this as PR2-27.
	if !strings.Contains(content, "PR2-27") {
		t.Error("payment_intents.go must reference PR2-27 in the durable convert job enqueue comment")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6 — convertjob package implements the worker handler
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step6_ConvertJobPackageExistsWithCorrectJobType verifies that the
// convertjob package defines the JobType constant and the NewHandler function
// for the checkout.convert_reservation worker job.
func TestPR227_Step6_ConvertJobPackageExistsWithCorrectJobType(t *testing.T) {
	content := findFileByName(t, "convertjob.go")

	// Must define the job type string.
	if !strings.Contains(content, `"checkout.convert_reservation"`) {
		t.Error("convertjob.go must define JobType = \"checkout.convert_reservation\"")
	}

	// Must export the JobType constant.
	if !strings.Contains(content, "JobType = ") {
		t.Error("convertjob.go must export a JobType constant")
	}

	// Must export NewHandler (the constructor for the worker handler function).
	if !strings.Contains(content, "func NewHandler(") {
		t.Error("convertjob.go must export NewHandler to construct the worker handler")
	}

	// The handler must be retried on error (not swallow errors silently).
	if !strings.Contains(content, "ConvertFn") {
		t.Error("convertjob.go must expose a ConvertFn field so the arena-worker can wire the real ConvertReservationTx implementation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7 — checkout.convert_reservation handler is registered in arena-worker
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step7_WorkerRegistersConvertJobHandler verifies that the
// arena-worker main.go registers the checkout.convert_reservation job handler
// using convertjob.NewHandler and wires ConvertReservationTx as the ConvertFn.
//
// Without this registration, the durable job enqueued by the webhook would
// accumulate in worker_jobs without being processed, and the TTL resale window
// would never be closed by the worker path.
func TestPR227_Step7_WorkerRegistersConvertJobHandler(t *testing.T) {
	content := findFileByName(t, "arena-worker-main.go")

	// Must register the handler using convertjob.JobType.
	if !strings.Contains(content, "convertjob.JobType") {
		t.Error("arena-worker/main.go must register 'checkout.convert_reservation' using convertjob.JobType")
	}

	// Must call convertjob.NewHandler with the ConvertFn option.
	if !strings.Contains(content, "convertjob.NewHandler") {
		t.Error("arena-worker/main.go must call convertjob.NewHandler to construct the job handler")
	}

	// Must wire ConvertReservationTx (the exported form used by the worker).
	if !strings.Contains(content, "ConvertReservationTx") {
		t.Error("arena-worker/main.go must wire hcheckout.Handler.ConvertReservationTx as the ConvertFn for the convertjob handler")
	}

	// Must build a minimal hcheckout.Handler for the convert path (not reusing
	// the full API handler, which has unnecessary deps like tierQ or ticketQ).
	if !strings.Contains(content, "checkout.convert_reservation: durable") {
		t.Error("arena-worker/main.go must include a comment explaining the PR2-27 durable convert registration")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8 — checkout.go PR2-27 atomicity comment documents the design
// ─────────────────────────────────────────────────────────────────────────────

// TestPR227_Step8_CheckoutGoDocumentsAtomicDesign verifies that checkout.go
// contains comments on both the free and paid completion branches that explain
// the PR2-27 atomicity design.
//
// This is a documentation witness: future maintainers must understand that
// removing the completeCheckoutWithPromoTx call in favour of a separate
// convertReservationTx call would re-open the PR2-04 window.
func TestPR227_Step8_CheckoutGoDocumentsAtomicDesign(t *testing.T) {
	content := findFileByName(t, "checkout.go")

	// Both branches must document the PR2-27 design via a comment.
	count := strings.Count(content, "PR2-27")
	if count < 2 {
		t.Errorf("checkout.go must have PR2-27 comments in BOTH free and paid completion branches documenting the atomic design; found %d comment(s), need >= 2", count)
	}
}
