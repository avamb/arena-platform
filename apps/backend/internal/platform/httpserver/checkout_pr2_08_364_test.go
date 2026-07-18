// checkout_pr2_08_364_test.go — structural tests for PR2-08 (feature #364):
// Derive checkout pricing from the reservation, not client input.
//
// Root cause (before fix):
//
//	HandleConfirmCheckout priced client-supplied req.TierID × req.Quantity with
//	no cross-check against the linked reservation, so a buyer holding 10 VIP
//	seats could confirm pricing for quantity=1 of a cheap tier and still receive
//	10 seated tickets at the cheap price.
//
// Fix:
//  1. Load the checkout session → get reservation_id.
//  2. Load the reservation → get authoritative session_id, tier_id, quantity.
//  3. Reject any client-supplied field that disagrees with the reservation
//     (422 checkout.pricing_mismatch).
//  4. Use reservation data for all pricing; support seated path via
//     buildSeatedPricingLines and multi-tier GA path via reservation_ga_items.
//
// Tests here are purely structural (source-file reads + HTTP shape tests);
// no live PostgreSQL connection is required.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helper: read checkout.go source
// ─────────────────────────────────────────────────────────────────────────────

func readConfirmCheckoutSource(t *testing.T) string {
	t.Helper()
	root := findProjectRoot(t)
	p := filepath.Join(root, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "checkout.go")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("cannot read checkout.go: %v", err)
	}
	return string(data)
}

// findProjectRoot walks up from the test binary's directory until it finds go.mod.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod (project root)")
		}
		dir = parent
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Handler loads checkout session before using tier/quantity
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step1_LoadsCheckoutSessionBeforeTierLookup verifies that the
// confirm handler calls GetCheckoutSessionByID before GetTicketTierByID so
// that the reservation is always the authoritative source.
func TestPR2_08_364_Step1_LoadsCheckoutSessionBeforeTierLookup(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	// Find the positions of the two calls.
	csPos := strings.Index(src, "GetCheckoutSessionByID")
	tierPos := strings.Index(src, "GetTicketTierByID")

	if csPos < 0 {
		t.Fatal("checkout.go: GetCheckoutSessionByID not found")
	}
	if tierPos < 0 {
		t.Fatal("checkout.go: GetTicketTierByID not found")
	}
	if csPos >= tierPos {
		t.Errorf("checkout.go: GetCheckoutSessionByID must appear before GetTicketTierByID "+
			"(positions: %d vs %d)", csPos, tierPos)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Handler loads reservation after checkout session
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step2_LoadsReservationAfterCheckoutSession verifies that the
// confirm handler calls GetReservationByID, and that this call comes after the
// GetCheckoutSessionByID call.
func TestPR2_08_364_Step2_LoadsReservationAfterCheckoutSession(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	csPos := strings.Index(src, "GetCheckoutSessionByID")
	resPos := strings.Index(src, "GetReservationByID")

	if csPos < 0 {
		t.Fatal("checkout.go: GetCheckoutSessionByID not found")
	}
	if resPos < 0 {
		t.Fatal("checkout.go: GetReservationByID not found — handler must load reservation")
	}
	if resPos <= csPos {
		t.Errorf("checkout.go: GetReservationByID must appear after GetCheckoutSessionByID "+
			"(positions: %d vs %d)", resPos, csPos)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Pricing uses reservation.Quantity, not req.Quantity
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step3_QuantityFromReservation verifies that the confirm handler
// uses reservation.Quantity (not req.Quantity) when calling ComputePricing.
func TestPR2_08_364_Step3_QuantityFromReservation(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	// The fix stores reservation.Quantity into a local 'quantity' variable and
	// passes it to ComputePricing. Confirm that "reservation.Quantity" appears.
	if !strings.Contains(src, "reservation.Quantity") {
		t.Error("checkout.go: 'reservation.Quantity' not found — pricing must use reservation quantity")
	}

	// The vulnerability: the old code passed "req.Quantity" to ComputePricing.
	// After the fix, ComputePricing should be called with the local 'quantity'
	// var (which holds reservation.Quantity) not with req.Quantity directly.
	// We verify that "ComputePricing(unitPrice, req.Quantity" no longer appears.
	if strings.Contains(src, "ComputePricing(unitPrice, req.Quantity") {
		t.Error("checkout.go: ComputePricing still passes req.Quantity — must use reservation.Quantity")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: pricing_mismatch error code present for cross-validation
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step4_PricingMismatchErrorCode verifies that the
// checkout.pricing_mismatch error code is emitted when client-supplied
// tier/quantity disagrees with the reservation.
func TestPR2_08_364_Step4_PricingMismatchErrorCode(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "checkout.pricing_mismatch") {
		t.Error("checkout.go: 'checkout.pricing_mismatch' error code missing — " +
			"must reject mismatched tier_id / quantity")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Reservation dependency guard
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step5_ReservationNilGuard verifies that the handler returns
// 503 when reservationQueries is nil (missing dependency), not a panic.
func TestPR2_08_364_Step5_ReservationNilGuard(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "dependency.reservation_unavailable") {
		t.Error("checkout.go: missing 'dependency.reservation_unavailable' nil-guard — " +
			"handler must check h.reservationQueries != nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Seated pricing path uses buildSeatedPricingLines
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step6_SeatedPathUsesBuildSeatedPricingLines verifies that the
// confirm handler now calls buildSeatedPricingLines (feature #310 reuse) to
// derive per-seat pricing from the reservation's session_seats rows.
func TestPR2_08_364_Step6_SeatedPathUsesBuildSeatedPricingLines(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "buildSeatedPricingLines") {
		t.Error("checkout.go: 'buildSeatedPricingLines' not called — " +
			"seated reservations must derive pricing from their seat rows")
	}
	if !strings.Contains(src, "ListReservationSeats") {
		t.Error("checkout.go: 'ListReservationSeats' not called — " +
			"seated path must load seat rows from the reservation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Multi-tier GA path uses ListReservationGAItems
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step7_MultiTierGAPathUsesGAItems verifies that the confirm
// handler handles multi-tier GA reservations (reservation.TierID == nil) by
// loading stored unit prices from reservation_ga_items.
func TestPR2_08_364_Step7_MultiTierGAPathUsesGAItems(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "ListReservationGAItems") {
		t.Error("checkout.go: 'ListReservationGAItems' not called — " +
			"multi-tier GA reservations must load items from reservation_ga_items")
	}
	if !strings.Contains(src, "checkout.pricing_unavailable") {
		t.Error("checkout.go: 'checkout.pricing_unavailable' missing — " +
			"must reject GA reservations with no items and no tier_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: HTTP shape — confirm still returns 401 without auth
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step8_ConfirmStillRequiresAuth ensures the auth middleware is
// still enforced after the PR2-08 refactor.
func TestPR2_08_364_Step8_ConfirmStillRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/confirm",
		strings.NewReader(`{"tier_id":"00000000-0000-0000-0000-000000000002","quantity":2}`))
	req.Header.Set("Content-Type", "application/json")
	// Intentionally no Authorization header.
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("confirm without auth: got %d, want 401; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Handler returns 503 when reservationQueries missing (HTTP shape)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step9_MissingReservationQueryReturns503 verifies the HTTP
// response shape when the handler is wired without reservationQueries.
// buildCheckoutServer omits ReservationQueries, so the nil guard fires.
func TestPR2_08_364_Step9_MissingReservationQueryReturns503(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/confirm",
		strings.NewReader(`{"tier_id":"00000000-0000-0000-0000-000000000002","quantity":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// The test server has no ReservationQueries; expect 503.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("confirm without reservation_queries: got %d, want 503; body: %s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dependency.reservation_unavailable") {
		t.Errorf("expected 'dependency.reservation_unavailable', got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Structural — applyPromoCode helper extracted
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step10_ApplyPromoCodeHelperExtracted verifies that the promo
// code logic is now shared via an applyPromoCode helper rather than duplicated
// across three pricing paths (seated / multi-tier GA / single-tier GA).
func TestPR2_08_364_Step10_ApplyPromoCodeHelperExtracted(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "func (h *Handler) applyPromoCode(") {
		t.Error("checkout.go: 'applyPromoCode' helper not found — " +
			"promo code logic should be extracted to avoid duplication across three pricing paths")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: Structural — TierID/Quantity check uses reservation, not req fields
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step11_TierIDFromReservation verifies that the tier lookup
// uses *reservation.TierID not req.TierID.
func TestPR2_08_364_Step11_TierIDFromReservation(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "reservation.TierID") {
		t.Error("checkout.go: 'reservation.TierID' not found — tier must be resolved from the reservation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: Structural — reservation_not_found error code present
// ─────────────────────────────────────────────────────────────────────────────

// TestPR2_08_364_Step12_ReservationNotFoundCode verifies that the handler
// returns a distinct error code when the linked reservation is missing, so
// operators can diagnose data integrity issues.
func TestPR2_08_364_Step12_ReservationNotFoundCode(t *testing.T) {
	src := readConfirmCheckoutSource(t)

	if !strings.Contains(src, "checkout.reservation_not_found") {
		t.Error("checkout.go: 'checkout.reservation_not_found' error code missing")
	}
}
