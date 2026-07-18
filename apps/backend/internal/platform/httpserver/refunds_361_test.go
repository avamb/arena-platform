// refunds_361_test.go — acceptance tests for feature #361 (over-refund / double-refund protection).
//
// Acceptance criteria (from feature description):
//  1. Lock the payment intent row and validate refundable = intent.amount - SUM(non-failed refunds).
//  2. Reject refunds on non-succeeded intents, currency mismatches, and amounts exceeding
//     the remaining refundable.
//  3. Make approval re-validate against the current refunded total inside the tx.
//  4. Concurrent/duplicate full refunds and partial-then-full over-refund are rejected.
//
// Test strategy:
//   - Pure structural tests verify the protection code is present in the handler and
//     SQL-gen files (executable without PostgreSQL).
//   - HTTP response tests verify that creation/approval enforce input constraints.
//     The dbDownPool causes BeginTx to fail → any request that reaches the DB layer
//     returns 500, not 409, so we assert ≠ 200 / ≠ 201 for those paths.
//   - SQL content tests verify the generated query files contain FOR UPDATE and
//     SUM(amount) clauses.
//   - Querier interface tests verify the two new methods are declared.
//
// The concurrent-access guarantee (row-level FOR UPDATE serialisation) requires
// a live PostgreSQL instance; those tests are marked TODO for the integration
// suite and must not be weakened here.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRecorder returns a fresh httptest.ResponseRecorder.
func newRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

// newRequest creates an *http.Request with the given method, path, and JSON body.
// Content-Type is set to application/json automatically.
func newRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Handler file contains all protection code
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund361_HandlerFile_HasTransactionalCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "BeginTx") {
		t.Error("refunds.go: HandleCreateRefund must call BeginTx to wrap PI lock + INSERT in a transaction")
	}
}

func TestRefund361_HandlerFile_HasForUpdateOnCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "GetPaymentIntentByIDForUpdate") {
		t.Error("refunds.go: HandleCreateRefund must call GetPaymentIntentByIDForUpdate to lock the PI row")
	}
}

func TestRefund361_HandlerFile_HasSumCheckOnCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "SumNonFailedRefundsByIntent") {
		t.Error("refunds.go: HandleCreateRefund must call SumNonFailedRefundsByIntent to enforce the refundable budget")
	}
}

func TestRefund361_HandlerFile_RejectsNonSucceededIntentOnCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "refund.intent_not_succeeded") {
		t.Error("refunds.go: missing 'refund.intent_not_succeeded' error code — HandleCreateRefund must reject non-succeeded intents")
	}
}

func TestRefund361_HandlerFile_RejectsCurrencyMismatchOnCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "refund.currency_mismatch") {
		t.Error("refunds.go: missing 'refund.currency_mismatch' error code — HandleCreateRefund must reject currency mismatches")
	}
}

func TestRefund361_HandlerFile_RejectsOverAmountOnCreate(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "refund.amount_exceeds_refundable") {
		t.Error("refunds.go: missing 'refund.amount_exceeds_refundable' error code — HandleCreateRefund must reject amounts exceeding remaining balance")
	}
}

func TestRefund361_HandlerFile_HasTransactionalApprove(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	// BeginTx is used in both HandleCreateRefund and HandleApproveRefund —
	// verifying it's present (at least twice conceptually) is sufficient.
	count := strings.Count(content, "BeginTx")
	if count < 2 {
		t.Errorf("refunds.go: expected BeginTx in both HandleCreateRefund and HandleApproveRefund (found %d occurrences)", count)
	}
}

func TestRefund361_HandlerFile_ApproveReValidatesIntentState(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "refund.over_refund_detected") {
		t.Error("refunds.go: missing 'refund.over_refund_detected' error code — HandleApproveRefund must re-validate the refund budget")
	}
}

func TestRefund361_HandlerFile_HasRefundNeedsManualReviewWithPI(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "refundNeedsManualReviewWithPI") {
		t.Error("refunds.go: missing refundNeedsManualReviewWithPI — HandleApproveRefund must use the PI-aware variant to avoid a double lookup inside the transaction")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL-gen file contains FOR UPDATE query
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund361_GenFile_PaymentIntentsHasForUpdateQuery(t *testing.T) {
	content := findFileByName(t, "payment_intents.sql.go")
	if !strings.Contains(content, "GetPaymentIntentByIDForUpdate") {
		t.Error("payment_intents.sql.go: missing GetPaymentIntentByIDForUpdate method")
	}
	if !strings.Contains(content, "FOR UPDATE") {
		t.Error("payment_intents.sql.go: GetPaymentIntentByIDForUpdate must use FOR UPDATE clause")
	}
}

func TestRefund361_GenFile_RefundsHasSumQuery(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	if !strings.Contains(content, "SumNonFailedRefundsByIntent") {
		t.Error("refunds.sql.go: missing SumNonFailedRefundsByIntent method")
	}
	if !strings.Contains(content, "SUM(amount)") {
		t.Error("refunds.sql.go: SumNonFailedRefundsByIntent must use SUM(amount)")
	}
}

func TestRefund361_GenFile_SumQueryExcludesFailedAndRejected(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	// The query must exclude failed and rejected states.
	if !strings.Contains(content, "'failed'") || !strings.Contains(content, "'rejected'") {
		t.Error("refunds.sql.go: SumNonFailedRefundsByIntent must exclude 'failed' and 'rejected' states")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Querier interface has the two new methods
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund361_QuerierHasGetPaymentIntentByIDForUpdate(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "GetPaymentIntentByIDForUpdate") {
		t.Error("querier.go: missing GetPaymentIntentByIDForUpdate in Querier interface")
	}
}

func TestRefund361_QuerierHasSumNonFailedRefundsByIntent(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "SumNonFailedRefundsByIntent") {
		t.Error("querier.go: missing SumNonFailedRefundsByIntent in Querier interface")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP behaviour — input validation still returns 400 before DB access
// ─────────────────────────────────────────────────────────────────────────────
// These tests confirm the early-exit validation path still works correctly even
// though the handler now uses a transaction. The dbDownPool causes BeginTx to
// fail, so any request that clears input validation and reaches the DB layer
// will return a 500 — but these tests verify we never reach that layer.

func TestRefund361_CreateHandler_InvalidUUIDStillReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds", `{"payment_intent_id":"not-a-uuid","amount":1000,"currency":"USD"}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("create with invalid payment_intent_id: got %d, want 400", w.Code)
	}
}

func TestRefund361_CreateHandler_ZeroAmountStillReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds", `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":0,"currency":"USD"}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("create with zero amount: got %d, want 400", w.Code)
	}
}

func TestRefund361_CreateHandler_MissingCurrencyStillReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds", `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":500}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("create without currency: got %d, want 400", w.Code)
	}
}

// TestRefund361_CreateHandler_ValidInputHitsDB verifies that a syntactically-valid
// create request is not rejected before reaching the DB layer. With dbDownPool
// the BeginTx call fails → 500. If this test ever returns 400 instead, it means
// an input-validation guard is incorrectly rejecting a well-formed request.
func TestRefund361_CreateHandler_ValidInputHitsDB(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds", `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":1000,"currency":"USD"}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	// With dbDownPool, BeginTx fails → 500. Must NOT be 400 (that would mean
	// the request was incorrectly rejected before hitting the DB).
	if w.Code == 400 {
		t.Errorf("valid create request: got 400 — want any non-400 (input-validation bug)")
	}
	// Must NOT be 201 either (that would mean the refund was created without DB).
	if w.Code == 201 {
		t.Errorf("valid create request: got 201 without a real DB — unexpected")
	}
}

// TestRefund361_ApproveHandler_InvalidUUIDStillReturns400 ensures approve
// input-parsing still returns 400 before any DB access.
func TestRefund361_ApproveHandler_InvalidUUIDStillReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds/not-a-uuid/approve", "{}")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("approve with invalid UUID: got %d, want 400", w.Code)
	}
}

// TestRefund361_ApproveHandler_ValidIdHitsDB verifies that a well-formed approve
// request reaches the DB layer (confirmed by dbDownPool returning 500, not 400).
func TestRefund361_ApproveHandler_ValidIdHitsDB(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := newRecorder()
	req := newRequest("POST", "/v1/refunds/00000000-0000-0000-0000-000000000001/approve", "{}")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	// With dbDownPool BeginTx returns error → 500.
	// Must NOT be 400 (input-validation bug) or 200 (spurious success without DB).
	if w.Code == 400 {
		t.Errorf("approve with valid UUID: got 400 — want 500 (db-down path), got input-validation rejection instead")
	}
	if w.Code == 200 {
		t.Errorf("approve with valid UUID: got 200 without real DB — unexpected")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: WithTx is used (enables transactional queries inside the tx)
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund361_HandlerFile_UsesWithTx(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "WithTx") {
		t.Error("refunds.go: must call gen.Queries.WithTx(tx) to bind queries to the active transaction")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TODO: Integration tests requiring PostgreSQL (not runnable without a live DB)
// ─────────────────────────────────────────────────────────────────────────────
//
// The following scenarios MUST be covered by integration tests when the
// PostgreSQL container is available:
//
//  1. TestRefund361_Integration_DuplicateFullRefundRejected:
//     Create intent (amount=1000, state=succeeded). Concurrently submit two
//     POST /v1/refunds with amount=1000. Exactly one must succeed (201) and
//     the other must fail (409, refund.amount_exceeds_refundable).
//
//  2. TestRefund361_Integration_PartialThenFullOverRefundRejected:
//     Create intent (amount=1000). Create refund A (amount=600) → approve.
//     Create refund B (amount=600) → must fail with 409.
//
//  3. TestRefund361_Integration_ConcurrentApproveRejected:
//     Create intent + two refunds (both amount=1000). Approve both concurrently.
//     Exactly one approve must succeed; the other must fail with 409
//     (refund.over_refund_detected).
//
//  4. TestRefund361_Integration_NonSucceededIntentRejected:
//     Create intent in state='created'. Attempt refund → 409 (refund.intent_not_succeeded).
//
//  5. TestRefund361_Integration_CurrencyMismatchRejected:
//     Create intent (currency=USD). Attempt refund with currency=EUR → 409.
