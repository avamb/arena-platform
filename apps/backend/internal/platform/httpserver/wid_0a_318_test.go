// wid_0a_318_test.go — unit tests for feature #318 (WID-0a):
// Seats/mixed carts through public checkout.
//
// Test coverage:
//   - Static analysis: handler file contains new types and fields
//   - Static analysis: gen file has InsertCheckoutSessionWithToken
//   - Static analysis: querier interface has InsertCheckoutSessionWithToken
//   - Static analysis: hcheckout exports NormalizeSeatKeys, SeatConflicts
//   - No auth required (not 401)
//   - Empty body → 400
//   - Invalid session_id → 400
//   - Missing holder_email → 400
//   - Seats + ga_items accepted (reaches DB, gets 500 from nil DB panic, not 400)
//   - Legacy tier_id+qty still works (backward compat with feature #153)
//   - 409 path exists in source (static analysis)
//   - checkout_token returned in response (static analysis check)
//   - expires_at returned in response (static analysis check)
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory
// ─────────────────────────────────────────────────────────────────────────────

func buildWID0aServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	return New(Options{
		Config:             cfg,
		PublicFeedQueries:  gen.New(nil),
		CheckoutQueries:    gen.New(nil),
		ReservationQueries: gen.New(nil),
		InventoryQueries:   gen.New(nil),
		TierQueries:        gen.New(nil),
		PromoQueries:       gen.New(nil),
		Pool:               &dbDownPool{},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Static analysis: handler source file
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0a318_Step1_HandlerContainsPublicGAItem(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "PublicGAItem") {
		t.Error("public_feed_checkout.go: expected 'PublicGAItem' type")
	}
	if !strings.Contains(content, "Seats") {
		t.Error("public_feed_checkout.go: expected 'Seats' field")
	}
	if !strings.Contains(content, "GaItems") {
		t.Error("public_feed_checkout.go: expected 'GaItems' field")
	}
}

func TestWID0a318_Step1_HandlerContainsCheckoutToken(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "checkout_token") {
		t.Error("public_feed_checkout.go: expected 'checkout_token' in response")
	}
	if !strings.Contains(content, "expires_at") {
		t.Error("public_feed_checkout.go: expected 'expires_at' in response")
	}
}

func TestWID0a318_Step1_HandlerContains409Path(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "reservation.seats_conflict") {
		t.Error("public_feed_checkout.go: expected 'reservation.seats_conflict' 409 path")
	}
	if !strings.Contains(content, "reservation.over_capacity") {
		t.Error("public_feed_checkout.go: expected 'reservation.over_capacity' 409 path")
	}
}

func TestWID0a318_Step1_HandlerContainsMintCheckoutToken(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "mintCheckoutToken") {
		t.Error("public_feed_checkout.go: expected 'mintCheckoutToken' function")
	}
	if !strings.Contains(content, "crypto/rand") {
		t.Error("public_feed_checkout.go: expected 'crypto/rand' import for token minting")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Static analysis: gen file + querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0a318_Step2_GenFileContainsInsertWithToken(t *testing.T) {
	content := findFileByName(t, "checkout_sessions.sql.go")
	if !strings.Contains(content, "InsertCheckoutSessionWithToken") {
		t.Error("checkout_sessions.sql.go: expected 'InsertCheckoutSessionWithToken' function")
	}
	if !strings.Contains(content, "checkout_token") {
		t.Error("checkout_sessions.sql.go: expected 'checkout_token' column in insert")
	}
}

func TestWID0a318_Step3_QuerierInterfaceHasInsertWithToken(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "InsertCheckoutSessionWithToken") {
		t.Error("querier.go: expected 'InsertCheckoutSessionWithToken' in Querier interface")
	}
}

// Compile-time check: *gen.Queries must satisfy gen.Querier (includes InsertCheckoutSessionWithToken).
var _ gen.Querier = (*gen.Queries)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Static analysis: hcheckout exported helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0a318_Step4_HcheckoutExportsNormalizeSeatKeys(t *testing.T) {
	// Compile-time check: NormalizeSeatKeys must be callable.
	out, dupKey, err := hcheckout.NormalizeSeatKeys([]string{"B-2", "A-1", "A-1"})
	if err == nil {
		t.Error("NormalizeSeatKeys: expected error for duplicate key")
	}
	if dupKey != "A-1" {
		t.Errorf("NormalizeSeatKeys: expected dupKey 'A-1', got %q", dupKey)
	}
	_ = out
}

func TestWID0a318_Step4_HcheckoutExportsSeatConflicts(t *testing.T) {
	// Compile-time check: SeatConflicts must be callable.
	conflicts := hcheckout.SeatConflicts([]string{"A-1"}, nil)
	if len(conflicts) != 1 {
		t.Errorf("SeatConflicts: expected 1 conflict for unknown key, got %d", len(conflicts))
	}
	if conflicts[0]["status"] != "unknown" {
		t.Errorf("SeatConflicts: expected status 'unknown', got %q", conflicts[0]["status"])
	}
}

func TestWID0a318_Step4_HcheckoutExportsConstants(t *testing.T) {
	if hcheckout.AdmissionGeneralAdmission != "general_admission" {
		t.Errorf("AdmissionGeneralAdmission: expected 'general_admission', got %q", hcheckout.AdmissionGeneralAdmission)
	}
	if hcheckout.AdmissionAssignedSeats != "assigned_seats" {
		t.Errorf("AdmissionAssignedSeats: expected 'assigned_seats', got %q", hcheckout.AdmissionAssignedSeats)
	}
	if hcheckout.Int32Max != 2147483647 {
		t.Errorf("Int32Max: expected 2147483647, got %d", hcheckout.Int32Max)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP tests
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0a318_NoAuthRequired(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","holder_email":"test@example.com","ga_items":[{"tier_id":"00000000-0000-0000-0000-000000000001","quantity":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — endpoint is public, no JWT needed")
	}
}

func TestWID0a318_EmptyBody_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWID0a318_InvalidSessionID_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"not-a-uuid","holder_email":"test@example.com","ga_items":[{"tier_id":"00000000-0000-0000-0000-000000000001","quantity":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWID0a318_MissingHolderEmail_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","ga_items":[{"tier_id":"00000000-0000-0000-0000-000000000001","quantity":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWID0a318_NoSeatsOrGAItems_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","holder_email":"test@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no seats and no ga_items, got %d", w.Code)
	}
}

func TestWID0a318_SeatsAndGAItems_ReachesDB(t *testing.T) {
	// This test verifies that a valid mixed-cart request passes all validation
	// and reaches the DB layer. With gen.New(nil), the DB call panics → 500.
	// A 400 would indicate the handler rejected the request during validation.
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{
		"session_id":   "00000000-0000-0000-0000-000000000002",
		"holder_email": "test@example.com",
		"seats":        ["Main-A-1"],
		"ga_items":     [{"tier_id": "00000000-0000-0000-0000-000000000001", "quantity": 1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 400 (validation passed) and must not be 401 (public endpoint).
	if w.Code == http.StatusBadRequest {
		t.Fatalf("expected not 400 — valid request should reach DB layer, got 400 with body: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint, no auth needed")
	}
}

func TestWID0a318_PureSeats_ReachesDB(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","holder_email":"test@example.com","seats":["Main-A-1","Main-A-2"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("expected not 400 — pure seated request should reach DB layer, body: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint, no auth needed")
	}
}

func TestWID0a318_LegacyTierIDQty_StillAccepted(t *testing.T) {
	// Backward-compat test: legacy tier_id + qty format (feature #153) must
	// continue to work after the WID-0a extension.
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"tier_id":"00000000-0000-0000-0000-000000000001","session_id":"00000000-0000-0000-0000-000000000002","qty":2,"holder_email":"buyer@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 400 (validation passed) and must not be 401 (public endpoint).
	if w.Code == http.StatusBadRequest {
		t.Fatalf("legacy tier_id+qty rejected with 400: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint")
	}
}

func TestWID0a318_InvalidGATierID_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","holder_email":"test@example.com","ga_items":[{"tier_id":"not-a-uuid","quantity":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ga_items[0].tier_id, got %d", w.Code)
	}
}

func TestWID0a318_ZeroQuantity_Returns400(t *testing.T) {
	s := buildWID0aServer(t)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000002","holder_email":"test@example.com","ga_items":[{"tier_id":"00000000-0000-0000-0000-000000000001","quantity":0}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for quantity=0 in ga_items, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time check: request type fields (backward compat with feature #153)
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0a318_RequestTypeFields_BackwardCompat(t *testing.T) {
	// Ensure the existing #153 fields are still present.
	req := publicFeedCheckoutStartRequest{
		TierID:      "00000000-0000-0000-0000-000000000001",
		SessionID:   "00000000-0000-0000-0000-000000000002",
		Qty:         2,
		HolderEmail: "buyer@example.com",
	}
	if req.TierID == "" {
		t.Error("TierID field missing from publicFeedCheckoutStartRequest")
	}
	if req.SessionID == "" {
		t.Error("SessionID field missing from publicFeedCheckoutStartRequest")
	}
	if req.Qty != 2 {
		t.Errorf("Qty field: expected 2, got %d", req.Qty)
	}
	if req.HolderEmail == "" {
		t.Error("HolderEmail field missing from publicFeedCheckoutStartRequest")
	}
}

func TestWID0a318_RequestTypeFields_NewWID0aFields(t *testing.T) {
	// Ensure the new WID-0a fields are present on the request type.
	req := publicFeedCheckoutStartRequest{
		SessionID:   "00000000-0000-0000-0000-000000000002",
		HolderEmail: "buyer@example.com",
		Seats:       []string{"Main-A-1", "Main-A-2"},
		GaItems:     []publicGAItem{{TierID: "00000000-0000-0000-0000-000000000001", Quantity: 2}},
	}
	if req.SessionID == "" || req.HolderEmail == "" {
		t.Error("existing #153 fields must remain settable alongside the WID-0a fields")
	}
	if len(req.Seats) != 2 {
		t.Errorf("Seats field: expected 2 entries, got %d", len(req.Seats))
	}
	if len(req.GaItems) != 1 {
		t.Errorf("GaItems field: expected 1 entry, got %d", len(req.GaItems))
	}
	if req.GaItems[0].Quantity != 2 {
		t.Errorf("GaItems[0].Quantity: expected 2, got %d", req.GaItems[0].Quantity)
	}
}
