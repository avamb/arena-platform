// checkout_132_test.go — unit tests for the checkout session state machine
// (feature #132).
//
// Test coverage:
//
//	Step 1: Migration file 0024_checkout_sessions.sql — table, constraints, RBAC
//	Step 2: Gen file structure — state machine queries, column types
//	Step 3: State transition logic — validCheckoutTransitions matrix
//	Step 4: HTTP routes — auth gating, missing params, server wiring
//	Step 5: checkoutSessionFromRow — nil-safe JSON field conversion
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test actor ID
// ─────────────────────────────────────────────────────────────────────────────

const checkoutTestActorID = "00000000-0000-0000-0000-000000000132"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for checkout route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildCheckoutServer builds a Server with stub auth, checkout routes mounted,
// and a dbDownPool so real DB operations never execute.
func buildCheckoutServer(t *testing.T) *Server {
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
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildCheckoutServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:          cfg,
		Auth:            stub,
		Pool:            &dbDownPool{},
		CheckoutQueries: gen.New(nil),
		TierQueries:     gen.New(nil),
		PromoQueries:    gen.New(nil),
	})
}

// mintCheckoutToken mints a dev JWT (admin role) for checkout route tests.
func mintCheckoutToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + checkoutTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintCheckoutToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintCheckoutToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintCheckoutToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout132_Step1_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0024_checkout_sessions.sql")

	t.Run("goose_up_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Up") {
			t.Error("0024_checkout_sessions.sql: missing '-- +goose Up' marker")
		}
	})

	t.Run("goose_down_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Down") {
			t.Error("0024_checkout_sessions.sql: missing '-- +goose Down' marker")
		}
	})

	t.Run("creates_checkout_sessions_table", func(t *testing.T) {
		if !strings.Contains(content, "CREATE TABLE checkout_sessions") {
			t.Error("0024_checkout_sessions.sql: missing 'CREATE TABLE checkout_sessions'")
		}
	})

	t.Run("state_constraint_has_all_states", func(t *testing.T) {
		for _, state := range []string{
			"created", "pricing_confirmed", "payment_started",
			"completed", "abandoned", "expired", "manual_review",
		} {
			if !strings.Contains(content, "'"+state+"'") {
				t.Errorf("0024_checkout_sessions.sql: missing state %q in CHECK constraint", state)
			}
		}
	})

	t.Run("pricing_snapshot_columns", func(t *testing.T) {
		for _, col := range []string{"subtotal", "discount", "platform_fee", "provider_fee", "tax", "total", "currency"} {
			if !strings.Contains(content, col) {
				t.Errorf("0024_checkout_sessions.sql: missing pricing column %q", col)
			}
		}
	})

	t.Run("terminal_timestamp_columns", func(t *testing.T) {
		for _, col := range []string{"completed_at", "abandoned_at", "expired_at"} {
			if !strings.Contains(content, col) {
				t.Errorf("0024_checkout_sessions.sql: missing timestamp column %q", col)
			}
		}
	})

	t.Run("checkout_permissions_seeded", func(t *testing.T) {
		for _, perm := range []string{"checkout.start", "checkout.confirm", "checkout.complete", "checkout.abandon", "checkout.read"} {
			if !strings.Contains(content, "'"+perm+"'") {
				t.Errorf("0024_checkout_sessions.sql: permission %q not seeded", perm)
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout132_Step2_GenFileHasCheckoutSessionRow(t *testing.T) {
	content := findFileByName(t, "checkout_sessions.sql.go")

	t.Run("checkout_session_row_struct", func(t *testing.T) {
		if !strings.Contains(content, "type CheckoutSessionRow struct") {
			t.Error("checkout_sessions.sql.go: missing 'type CheckoutSessionRow struct'")
		}
	})

	t.Run("insert_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) InsertCheckoutSession") {
			t.Error("checkout_sessions.sql.go: missing InsertCheckoutSession method")
		}
	})

	t.Run("confirm_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) ConfirmCheckoutSession") {
			t.Error("checkout_sessions.sql.go: missing ConfirmCheckoutSession method")
		}
	})

	t.Run("complete_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) CompleteCheckoutSession") {
			t.Error("checkout_sessions.sql.go: missing CompleteCheckoutSession method")
		}
	})

	t.Run("abandon_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) AbandonCheckoutSession") {
			t.Error("checkout_sessions.sql.go: missing AbandonCheckoutSession method")
		}
	})

	t.Run("expire_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) ExpireCheckoutSession") {
			t.Error("checkout_sessions.sql.go: missing ExpireCheckoutSession method")
		}
	})

	t.Run("list_by_reservation_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) ListCheckoutSessionsByReservation") {
			t.Error("checkout_sessions.sql.go: missing ListCheckoutSessionsByReservation method")
		}
	})
}

func TestCheckout132_Step2_QuerierHasCheckoutMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")

	for _, method := range []string{
		"InsertCheckoutSession",
		"GetCheckoutSessionByID",
		"ConfirmCheckoutSession",
		"CompleteCheckoutSession",
		"AbandonCheckoutSession",
		"ExpireCheckoutSession",
		"ListCheckoutSessionsByReservation",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go: missing method %q", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: State transition matrix
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout132_Step3_ValidTransitions(t *testing.T) {
	t.Parallel()

	// Valid transitions that must exist.
	valid := []struct{ from, to string }{
		{"created", "pricing_confirmed"},
		{"created", "abandoned"},
		{"created", "expired"},
		{"pricing_confirmed", "completed"},
		{"pricing_confirmed", "abandoned"},
		{"pricing_confirmed", "expired"},
		{"payment_started", "completed"},
		{"payment_started", "manual_review"},
		{"payment_started", "abandoned"},
	}
	for _, tc := range valid {
		if !validCheckoutTransitions[tc.from][tc.to] {
			t.Errorf("expected valid transition %s → %s", tc.from, tc.to)
		}
	}
}

func TestCheckout132_Step3_TerminalStatesHaveNoTransitions(t *testing.T) {
	t.Parallel()
	terminals := []string{"completed", "abandoned", "expired"}
	for _, state := range terminals {
		if len(validCheckoutTransitions[state]) != 0 {
			t.Errorf("terminal state %q should have no outgoing transitions, got %v",
				state, validCheckoutTransitions[state])
		}
	}
}

func TestCheckout132_Step3_InvalidTransitionCreatedToCompleted(t *testing.T) {
	t.Parallel()
	if validCheckoutTransitions["created"]["completed"] {
		t.Error("created → completed should NOT be a valid transition (must go through pricing_confirmed)")
	}
}

func TestCheckout132_Step3_AllStatesInMap(t *testing.T) {
	t.Parallel()
	states := []string{
		"created", "pricing_confirmed", "payment_started",
		"completed", "abandoned", "expired", "manual_review",
	}
	for _, state := range states {
		if _, ok := validCheckoutTransitions[state]; !ok {
			t.Errorf("state %q not in validCheckoutTransitions map", state)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP routes — auth gating
// ─────────────────────────────────────────────────────────────────────────────

const checkoutStartPath = "/v1/checkout/start"
const checkoutBasePath = "/v1/checkout/00000000-0000-0000-0000-000000000001"

func TestCheckout132_Step4_StartRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)
	req := httptest.NewRequest(http.MethodPost, checkoutStartPath,
		strings.NewReader(`{"org_id":"00000000-0000-0000-0000-000000000001","channel_id":"00000000-0000-0000-0000-000000000002","reservation_id":"00000000-0000-0000-0000-000000000003"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("start without auth: got %d, want 401", w.Code)
	}
}

func TestCheckout132_Step4_GetRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)
	req := httptest.NewRequest(http.MethodGet, checkoutBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("get without auth: got %d, want 401", w.Code)
	}
}

func TestCheckout132_Step4_ConfirmRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)
	req := httptest.NewRequest(http.MethodPost, checkoutBasePath+"/confirm",
		strings.NewReader(`{"tier_id":"00000000-0000-0000-0000-000000000001","session_id":"00000000-0000-0000-0000-000000000002","quantity":1,"org_id":"00000000-0000-0000-0000-000000000003"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("confirm without auth: got %d, want 401", w.Code)
	}
}

func TestCheckout132_Step4_CompleteRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)
	req := httptest.NewRequest(http.MethodPost, checkoutBasePath+"/complete",
		strings.NewReader(`{"payment_intent_id":"pi_xxx","payment_provider":"stripe"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("complete without auth: got %d, want 401", w.Code)
	}
}

func TestCheckout132_Step4_AbandonRequiresAuth(t *testing.T) {
	s := buildCheckoutServer(t)
	req := httptest.NewRequest(http.MethodPost, checkoutBasePath+"/abandon", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("abandon without auth: got %d, want 401", w.Code)
	}
}

func TestCheckout132_Step4_StartMissingOrgID(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodPost, checkoutStartPath,
		strings.NewReader(`{"channel_id":"00000000-0000-0000-0000-000000000002","reservation_id":"00000000-0000-0000-0000-000000000003"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("start missing org_id: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "checkout.invalid_org_id") {
		t.Errorf("expected 'checkout.invalid_org_id' in body, got: %s", w.Body.String())
	}
}

func TestCheckout132_Step4_StartEmptyBody(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodPost, checkoutStartPath, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("start empty body: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "checkout.empty_body") {
		t.Errorf("expected 'checkout.empty_body' in body, got: %s", w.Body.String())
	}
}

func TestCheckout132_Step4_CompleteEmptyBody(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodPost, checkoutBasePath+"/complete", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "checkout.empty_body") {
		t.Errorf("complete empty body should follow free-checkout path, got: %s", w.Body.String())
	}
	if w.Code != http.StatusConflict && w.Code != http.StatusInternalServerError {
		t.Errorf("complete empty body: got %d, want 409 or 500; body: %s", w.Code, w.Body.String())
	}
}

func TestCheckout132_Step4_CompleteMissingPaymentIntentID(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodPost, checkoutBasePath+"/complete",
		strings.NewReader(`{"payment_provider":"stripe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("complete missing payment_intent_id: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "checkout.missing_payment_intent") {
		t.Errorf("expected 'checkout.missing_payment_intent' in body, got: %s", w.Body.String())
	}
}

func TestCheckout132_Step4_GetInvalidUUID(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodGet, "/v1/checkout/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("get invalid UUID: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "checkout.invalid_id") {
		t.Errorf("expected 'checkout.invalid_id' in body, got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: checkoutSessionFromRow — nil-safe conversion
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout132_Step5_FromRowNilFields(t *testing.T) {
	t.Parallel()
	cs := gen.CheckoutSessionRow{
		ID:            uuid.MustParse("00000000-0000-0000-0000-000000000132"),
		OrgID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		ChannelID:     uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ReservationID: uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		State:         "created",
		CreatedAt:     time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC),
		// All optional fields are nil.
	}

	resp := checkoutSessionFromRow(cs)

	if resp.ID != "00000000-0000-0000-0000-000000000132" {
		t.Errorf("ID: got %q, want 00000000-0000-0000-0000-000000000132", resp.ID)
	}
	if resp.State != "created" {
		t.Errorf("State: got %q, want 'created'", resp.State)
	}
	if resp.UserID != nil {
		t.Errorf("UserID should be nil, got %v", resp.UserID)
	}
	if resp.Subtotal != nil {
		t.Errorf("Subtotal should be nil for uncofirmed session, got %v", resp.Subtotal)
	}
	if resp.Total != nil {
		t.Errorf("Total should be nil for unconfirmed session, got %v", resp.Total)
	}
	if resp.CompletedAt != nil {
		t.Errorf("CompletedAt should be nil, got %v", resp.CompletedAt)
	}
}

func TestCheckout132_Step5_FromRowAllFields(t *testing.T) {
	t.Parallel()
	subtotal := int64(2000)
	discount := int64(200)
	platformFee := int64(90)
	providerFee := int64(36)
	tax := int64(0)
	total := int64(1926)
	currency := "ILS"
	promoID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	payIntent := "pi_xxx"
	payProvider := "stripe"
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

	cs := gen.CheckoutSessionRow{
		ID:              uuid.MustParse("00000000-0000-0000-0000-000000000132"),
		OrgID:           uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		ChannelID:       uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ReservationID:   uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		State:           "completed",
		Subtotal:        &subtotal,
		Discount:        &discount,
		PlatformFee:     &platformFee,
		ProviderFee:     &providerFee,
		Tax:             &tax,
		Total:           &total,
		Currency:        &currency,
		PromoCodeID:     &promoID,
		PaymentIntentID: &payIntent,
		PaymentProvider: &payProvider,
		CompletedAt:     &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	resp := checkoutSessionFromRow(cs)

	if resp.State != "completed" {
		t.Errorf("State: got %q, want 'completed'", resp.State)
	}
	if resp.Total == nil || *resp.Total != 1926 {
		t.Errorf("Total: got %v, want 1926", resp.Total)
	}
	if resp.Currency == nil || *resp.Currency != "ILS" {
		t.Errorf("Currency: got %v, want 'ILS'", resp.Currency)
	}
	if resp.PromoCodeID == nil {
		t.Error("PromoCodeID should not be nil")
	}
	if resp.CompletedAt == nil {
		t.Error("CompletedAt should not be nil")
	}
	if resp.PaymentIntentID == nil || *resp.PaymentIntentID != "pi_xxx" {
		t.Errorf("PaymentIntentID: got %v, want 'pi_xxx'", resp.PaymentIntentID)
	}
}
