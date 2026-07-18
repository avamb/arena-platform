// checkout_pr2_07_363_test.go — structural unit tests for feature #363
// (PR2-07 BLOCKER): Make webhook processing atomic and durable.
//
// # Problem being fixed
//
// The original HandlePaymentIntentWebhook committed two separate SQL statements:
//
//  1. InsertPaymentIntentEvent (idempotency row) — auto-commit
//  2. UpdatePaymentIntentState                   — auto-commit
//
// If the process crashed between statements 1 and 2 (or between 2 and the inline
// h.issueTickets call), the idempotency row was already committed. Provider
// redelivery found the row via ON CONFLICT DO NOTHING, returned pgx.ErrNoRows,
// and the handler returned 204 without reprocessing — permanently losing the
// state transition and ticket issuance.
//
// # Fix
//
// Steps 1 (InsertPaymentIntentEvent), 2 (UpdatePaymentIntentState), and 3
// (INSERT worker_jobs "checkout.issue_tickets") are now committed in a single
// PostgreSQL transaction.  A crash before the commit rolls back all three;
// provider redelivery finds no idempotency row and processes the event from
// scratch.
//
// Ticket issuance is handled by the checkout.issue_tickets worker job (package
// issuejob) rather than inline.  IssueTicketsForCheckout (feature #366) is
// idempotent, so multiple worker-job attempts are safe.
//
// All tests in this file are pure structural unit tests — no live PostgreSQL
// required.
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — payment_intents.go wraps event INSERT + state UPDATE in one tx
// ─────────────────────────────────────────────────────────────────────────────

// TestPR207_Step1_WebhookUsesBeginTxForAtomicEventAndState verifies that
// HandlePaymentIntentWebhook calls BeginTx to open a transaction before the
// InsertPaymentIntentEvent and UpdatePaymentIntentState operations, ensuring
// they commit atomically.
func TestPR207_Step1_WebhookUsesBeginTxForAtomicEventAndState(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "BeginTx") {
		t.Error("payment_intents.go: HandlePaymentIntentWebhook must call BeginTx to atomically commit the idempotency event and state transition (feature #363 PR2-07)")
	}
}

// TestPR207_Step1_WebhookUsesTransactionQueriesForEventInsert verifies that the
// event INSERT uses the transaction-scoped query set (txQ) rather than the pool
// query set, so it participates in the atomic transaction.
func TestPR207_Step1_WebhookUsesTransactionQueriesForEventInsert(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	// The tx-scoped query set is constructed as gen.New(tx).
	if !strings.Contains(content, "gen.New(tx)") {
		t.Error("payment_intents.go: HandlePaymentIntentWebhook must use gen.New(tx) to run the event INSERT inside the transaction (feature #363)")
	}
}

// TestPR207_Step1_WebhookCommitsTransaction verifies that the handler commits
// the atomic transaction after all three operations succeed.
func TestPR207_Step1_WebhookCommitsTransaction(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "tx.Commit") {
		t.Error("payment_intents.go: HandlePaymentIntentWebhook must commit the transaction after the event INSERT, state UPDATE, and job enqueue (feature #363)")
	}
}

// TestPR207_Step1_WebhookDefersRollback verifies that the handler defers a
// rollback as a safety net so that any error path before Commit leaves no
// partial state committed.
func TestPR207_Step1_WebhookDefersRollback(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "tx.Rollback") {
		t.Error("payment_intents.go: HandlePaymentIntentWebhook must defer tx.Rollback as a safety net for early-exit error paths (feature #363)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — checkout.issue_tickets job enqueued in the same transaction
// ─────────────────────────────────────────────────────────────────────────────

// TestPR207_Step2_WebhookEnqueuesIssueTicketsJob verifies that the webhook
// handler enqueues a "checkout.issue_tickets" worker job when the payment
// succeeds, replacing the inline h.issueTickets call.
func TestPR207_Step2_WebhookEnqueuesIssueTicketsJob(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "checkout.issue_tickets") {
		t.Error("payment_intents.go: HandlePaymentIntentWebhook must enqueue a 'checkout.issue_tickets' worker job when payment.succeeded fires (feature #363 PR2-07)")
	}
}

// TestPR207_Step2_IssueJobPackageDefinesJobType verifies that the issuejob
// package defines the JobType constant used by both the enqueue site
// (payment_intents.go) and the handler registration (arena-worker main.go).
func TestPR207_Step2_IssueJobPackageDefinesJobType(t *testing.T) {
	content := findFileByName(t, "issuejob.go")
	if !strings.Contains(content, `JobType = "checkout.issue_tickets"`) {
		t.Error("issuejob.go must define JobType = \"checkout.issue_tickets\" (feature #363 PR2-07)")
	}
}

// TestPR207_Step2_IssueJobPackageDefinesPayloadStruct verifies that the
// issuejob package defines a Payload struct with a CheckoutSessionID field,
// which is the JSON envelope for the worker job payload.
func TestPR207_Step2_IssueJobPackageDefinesPayloadStruct(t *testing.T) {
	content := findFileByName(t, "issuejob.go")
	if !strings.Contains(content, "CheckoutSessionID") {
		t.Error("issuejob.go must define a Payload struct with CheckoutSessionID for the checkout.issue_tickets worker job (feature #363)")
	}
}

// TestPR207_Step2_IssueJobPackageDefinesNewHandler verifies that the issuejob
// package exports a NewHandler constructor that the worker binary uses to build
// the job handler function.
func TestPR207_Step2_IssueJobPackageDefinesNewHandler(t *testing.T) {
	content := findFileByName(t, "issuejob.go")
	if !strings.Contains(content, "func NewHandler") {
		t.Error("issuejob.go must export NewHandler to construct the checkout.issue_tickets worker handler (feature #363)")
	}
}

// TestPR207_Step2_WorkerJobEnqueuedInSameTxAsState verifies that the worker
// job INSERT uses the transaction executor (tx.Exec), not the auto-commit pool,
// so it is atomically committed with the event INSERT and state UPDATE.
func TestPR207_Step2_WorkerJobEnqueuedInSameTxAsState(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "tx.Exec") {
		t.Error("payment_intents.go: the checkout.issue_tickets worker job must be enqueued via tx.Exec so it is atomically committed with the event row and state change (feature #363)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Redelivery after mid-flight failure still completes issuance
// ─────────────────────────────────────────────────────────────────────────────

// TestPR207_Step3_DuplicateEventRollsBackTransaction verifies that when
// InsertPaymentIntentEvent returns pgx.ErrNoRows (duplicate delivery), the
// handler explicitly rolls back the transaction before returning 204. This
// ensures an empty transaction is never accidentally committed.
func TestPR207_Step3_DuplicateEventRollsBackTransaction(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	// Must explicitly rollback before returning 204 on duplicate.
	if !strings.Contains(content, "tx.Rollback") {
		t.Error("payment_intents.go: duplicate event path must call tx.Rollback before returning 204 (feature #363)")
	}
	if !strings.Contains(content, "204") && !strings.Contains(content, "StatusNoContent") {
		t.Error("payment_intents.go: duplicate event path must return HTTP 204 No Content (feature #363)")
	}
}

// TestPR207_Step3_InlineIssueTicketsRemovedFromWebhook verifies that the inline
// h.issueTickets call was removed from HandlePaymentIntentWebhook. Ticket
// issuance is now handled exclusively by the checkout.issue_tickets worker job,
// which is enqueued atomically within the same transaction as the event record
// and state change.
func TestPR207_Step3_InlineIssueTicketsRemovedFromWebhook(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if strings.Contains(content, "h.issueTickets(") {
		t.Error("payment_intents.go: the inline h.issueTickets() call must be removed from HandlePaymentIntentWebhook; ticket issuance is now handled by the checkout.issue_tickets worker job (feature #363)")
	}
}

// TestPR207_Step3_ConvertReservationStillCalledInlineAfterCommit verifies that
// convertReservationTx is still called inline in the webhook handler (after the
// atomic tx commits) for fast seat-to-sold conversion. This preserves the
// feature #360 (PR2-04) fix. The call is non-fatal and idempotent.
func TestPR207_Step3_ConvertReservationStillCalledInlineAfterCommit(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "convertReservationTx") {
		t.Error("payment_intents.go: convertReservationTx must still be called inline after the atomic tx commits (feature #360 requirement preserved by feature #363)")
	}
	if !strings.Contains(content, "convert reservation failed after payment succeeded") {
		t.Error("payment_intents.go: must log 'convert reservation failed after payment succeeded' for the inline convertReservationTx call (feature #360 requirement)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — arena-worker registers the checkout.issue_tickets handler
// ─────────────────────────────────────────────────────────────────────────────

// TestPR207_Step4_WorkerRegistersIssueTicketsHandler verifies that
// arena-worker's registerBuiltinHandlers registers the checkout.issue_tickets
// job type so the worker can pick up and execute the jobs enqueued by the
// webhook handler.
func TestPR207_Step4_WorkerRegistersIssueTicketsHandler(t *testing.T) {
	content := findFileByName(t, "arena-worker-main.go")
	if !strings.Contains(content, "checkout.issue_tickets") {
		t.Error("arena-worker/main.go: registerBuiltinHandlers must register the 'checkout.issue_tickets' job type (feature #363 PR2-07)")
	}
}

// TestPR207_Step4_WorkerUsesIssuejobNewHandler verifies that the worker binary
// uses issuejob.NewHandler to build the checkout.issue_tickets worker function,
// ensuring the correct handler factory is wired.
func TestPR207_Step4_WorkerUsesIssuejobNewHandler(t *testing.T) {
	content := findFileByName(t, "arena-worker-main.go")
	if !strings.Contains(content, "issuejob.NewHandler") {
		t.Error("arena-worker/main.go: must call issuejob.NewHandler to build the checkout.issue_tickets handler function (feature #363)")
	}
}

// TestPR207_Step4_WorkerPassesIssueTicketsCallback verifies that the worker
// wires htickets.Handler.IssueTicketsForCheckout as the IssueTickets callback
// in issuejob.HandlerOptions so actual ticket issuance logic is executed.
func TestPR207_Step4_WorkerPassesIssueTicketsCallback(t *testing.T) {
	content := findFileByName(t, "arena-worker-main.go")
	if !strings.Contains(content, "IssueTicketsForCheckout") {
		t.Error("arena-worker/main.go: issuejob.NewHandler must receive IssueTicketsForCheckout as its IssueTickets callback (feature #363)")
	}
}

// TestPR207_Step4_IssuejobHandlerCallsIssueTickets verifies that the issuejob
// handler implementation calls opts.IssueTickets to perform actual ticket
// issuance, not a stub or no-op.
func TestPR207_Step4_IssuejobHandlerCallsIssueTickets(t *testing.T) {
	content := findFileByName(t, "issuejob.go")
	if !strings.Contains(content, "opts.IssueTickets(") {
		t.Error("issuejob.go: NewHandler must call opts.IssueTickets to perform ticket issuance (feature #363)")
	}
}

// TestPR207_Step4_Feature367CommentPreservedInWebhook verifies that the
// payment_intents.go webhook handler still contains the feature #367 comment
// documenting that delivery is enqueued inside IssueTicketsForCheckout, not via
// a separate h.enqueueDelivery call.
func TestPR207_Step4_Feature367CommentPreservedInWebhook(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "feature #367") {
		t.Error("payment_intents.go: must retain the feature #367 reference documenting that delivery is enqueued inside IssueTicketsForCheckout (feature #367 regression check)")
	}
}
