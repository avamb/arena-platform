// promo_368_test.go — structural tests for PR2-12: enforce promo max_uses and
// record redemptions at checkout completion (feature #368).
//
// The bug: validatePromoCode (and applyPromoCode) only checked status, validity
// window, and min order amount — it NEVER consulted the promo_code_redemptions
// table. A single-use 100%-off code was infinitely redeemable.
//
// The fix:
//   - applyPromoCode now enforces max_uses and max_uses_per_customer via
//     CountPromoCodeRedemptions / CountUserRedemptions (soft check at confirm time).
//   - HandleCompleteCheckout uses completeCheckoutWithPromoTx which atomically:
//     1. Locks the promo code row (SELECT … FOR UPDATE)
//     2. Re-checks usage limits under the lock
//     3. Completes the checkout session
//     4. Inserts a promo_code_redemptions row
//     All in a single transaction, preventing concurrent double-use.
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: GetPromoCodeByIDForUpdate SQL contains FOR UPDATE
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step1_GetPromoCodeByIDForUpdateSQL(t *testing.T) {
	src := findFileByName(t, "promo_codes.sql")
	if !strings.Contains(src, "GetPromoCodeByIDForUpdate") {
		t.Error("promo_codes.sql: missing GetPromoCodeByIDForUpdate query name")
	}
	if !strings.Contains(src, "FOR UPDATE") {
		t.Error("promo_codes.sql: GetPromoCodeByIDForUpdate must include FOR UPDATE clause")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Generated Go method GetPromoCodeByIDForUpdate exists
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step2_GeneratedGetPromoCodeByIDForUpdate(t *testing.T) {
	src := findFileByName(t, "promo_codes.sql.go")
	if !strings.Contains(src, "GetPromoCodeByIDForUpdate") {
		t.Error("gen/promo_codes.sql.go: missing GetPromoCodeByIDForUpdate method")
	}
	if !strings.Contains(src, "FOR UPDATE") {
		t.Error("gen/promo_codes.sql.go: getPromoCodeByIDForUpdate SQL constant must include FOR UPDATE")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Querier interface includes GetPromoCodeByIDForUpdate
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step3_QuerierInterfaceIncludesForUpdate(t *testing.T) {
	src := findFileByName(t, "querier.go")
	if !strings.Contains(src, "GetPromoCodeByIDForUpdate") {
		t.Error("gen/querier.go: Querier interface must declare GetPromoCodeByIDForUpdate")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: applyPromoCode enforces max_uses (CountPromoCodeRedemptions)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step4_ApplyPromoCodeChecksMaxUses(t *testing.T) {
	src := findFileByName(t, "checkout.go")
	if !strings.Contains(src, "CountPromoCodeRedemptions") {
		t.Error("checkout.go applyPromoCode: must call CountPromoCodeRedemptions to enforce max_uses")
	}
	if !strings.Contains(src, "promo.exhausted") {
		t.Error("checkout.go applyPromoCode: must return promo.exhausted error code when max_uses is hit")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: applyPromoCode enforces max_uses_per_customer (CountUserRedemptions)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step5_ApplyPromoCodeChecksPerCustomerLimit(t *testing.T) {
	src := findFileByName(t, "checkout.go")
	if !strings.Contains(src, "CountUserRedemptions") {
		t.Error("checkout.go applyPromoCode: must call CountUserRedemptions to enforce max_uses_per_customer")
	}
	if !strings.Contains(src, "promo.per_customer_limit") {
		t.Error("checkout.go applyPromoCode: must return promo.per_customer_limit when per-customer limit is hit")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: applyPromoCode passes userID for per-customer limit enforcement
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step6_ApplyPromoCodeAcceptsUserID(t *testing.T) {
	src := findFileByName(t, "checkout.go")
	// The function signature must include userID *uuid.UUID parameter.
	if !strings.Contains(src, "userID *uuid.UUID") {
		t.Error("checkout.go: applyPromoCode must accept userID *uuid.UUID for per-customer limit enforcement")
	}
	// Must pass checkoutSession.UserID at each call site.
	if !strings.Contains(src, "checkoutSession.UserID") {
		t.Error("checkout.go: applyPromoCode call sites must pass checkoutSession.UserID as userID argument")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: checkout_promo_368.go implements completeCheckoutWithPromoTx
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step7_CompleteWithPromoTxExists(t *testing.T) {
	src := findFileByName(t, "checkout_promo_368.go")
	if !strings.Contains(src, "completeCheckoutWithPromoTx") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx helper must be defined")
	}
	if !strings.Contains(src, "GetPromoCodeByIDForUpdate") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx must call GetPromoCodeByIDForUpdate (FOR UPDATE lock)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: completeCheckoutWithPromoTx inserts a redemption row
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step8_CompleteWithPromoTxInsertsRedemption(t *testing.T) {
	src := findFileByName(t, "checkout_promo_368.go")
	if !strings.Contains(src, "InsertPromoCodeRedemption") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx must call InsertPromoCodeRedemption")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: completeCheckoutWithPromoTx uses an explicit DB transaction
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step9_CompleteWithPromoTxUsesTransaction(t *testing.T) {
	src := findFileByName(t, "checkout_promo_368.go")
	if !strings.Contains(src, "BeginTx") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx must call BeginTx to start an explicit transaction")
	}
	if !strings.Contains(src, "tx.Commit") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx must commit the transaction")
	}
	if !strings.Contains(src, "tx.Rollback") {
		t.Error("checkout_promo_368.go: completeCheckoutWithPromoTx must rollback on promo limit hit or error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: HandleCompleteCheckout routes through completeCheckoutWithPromoTx
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step10_HandleCompleteCheckoutUsesPromoTx(t *testing.T) {
	src := findFileByName(t, "checkout.go")
	if !strings.Contains(src, "completeCheckoutWithPromoTx") {
		t.Error("checkout.go HandleCompleteCheckout: must call completeCheckoutWithPromoTx for atomic complete+redemption")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: HandleCompleteCheckout pre-loads session for promo params
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step11_HandleCompleteCheckoutPreLoadsSession(t *testing.T) {
	src := findFileByName(t, "checkout.go")
	if !strings.Contains(src, "promoCodeIDForComplete") {
		t.Error("checkout.go HandleCompleteCheckout: must pre-load checkout session to extract promoCodeIDForComplete")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: errPromoExhausted and errPromoPerCustomerLimit sentinel errors exist
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo368_Step12_SentinelErrorsExist(t *testing.T) {
	src := findFileByName(t, "checkout_promo_368.go")
	if !strings.Contains(src, "errPromoExhausted") {
		t.Error("checkout_promo_368.go: errPromoExhausted sentinel error must be defined")
	}
	if !strings.Contains(src, "errPromoPerCustomerLimit") {
		t.Error("checkout_promo_368.go: errPromoPerCustomerLimit sentinel error must be defined")
	}
}
