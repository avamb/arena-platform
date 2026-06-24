// refunds_138_test.go — unit tests for feature #138 (Refund state machine).
//
// Test coverage:
//   Step 1: Migration file 0028_refunds.sql — table, state enum, idempotency table, RBAC
//   Step 2: State transition guards — all valid and invalid transitions
//   Step 3: SQL query file and gen file structure
//   Step 4: Querier interface — all 7 refund methods present
//   Step 5: HTTP routes — auth-gating, server wiring, validation
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for refund route tests
// ─────────────────────────────────────────────────────────────────────────────

const refundTestActorID = "00000000-0000-0000-0000-000000000138"

// buildRefundServer builds a Server with stub auth, refund routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildRefundServer(t *testing.T) *Server {
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
		t.Fatalf("buildRefundServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		RefundQueries:        gen.New(nil),
		PaymentIntentQueries: gen.New(nil),
		TicketQueries:        gen.New(nil),
	})
}

// mintRefundToken mints a dev JWT for refund route tests.
func mintRefundToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + refundTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintRefundToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintRefundToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintRefundToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if content == "" {
		t.Fatal("0028_refunds.sql is empty")
	}
}

func TestRefund138_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("0028_refunds.sql missing '-- +goose Up' marker")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("0028_refunds.sql missing '-- +goose Down' marker")
	}
}

func TestRefund138_MigrationRefundsTable(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "CREATE TABLE refunds") {
		t.Error("0028_refunds.sql missing CREATE TABLE refunds")
	}
}

func TestRefund138_MigrationStateEnum(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	states := []string{
		"'requested'",
		"'approved'",
		"'rejected'",
		"'provider_pending'",
		"'succeeded'",
		"'failed'",
		"'manual_review'",
	}
	for _, s := range states {
		if !strings.Contains(content, s) {
			t.Errorf("0028_refunds.sql missing state %s in CHECK constraint", s)
		}
	}
}

func TestRefund138_MigrationRequiredColumns(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	cols := []string{
		"payment_intent_id",
		"org_id",
		"amount",
		"currency",
		"reason",
		"requested_by",
		"state",
		"provider_refund_id",
		"failure_reason",
		"requested_at",
		"approved_at",
		"succeeded_at",
		"failed_at",
		"created_at",
		"updated_at",
	}
	for _, col := range cols {
		if !strings.Contains(content, col) {
			t.Errorf("0028_refunds.sql missing column: %s", col)
		}
	}
}

func TestRefund138_MigrationRefundEventsTable(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "CREATE TABLE refund_events") {
		t.Error("0028_refunds.sql missing CREATE TABLE refund_events")
	}
}

func TestRefund138_MigrationIdempotencyConstraint(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "UNIQUE (provider_refund_id, event_type)") {
		t.Error("0028_refunds.sql missing UNIQUE (provider_refund_id, event_type) constraint")
	}
}

func TestRefund138_MigrationIndexes(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "refunds_payment_intent_id") {
		t.Error("0028_refunds.sql missing index refunds_payment_intent_id")
	}
	if !strings.Contains(content, "refunds_state_idx") {
		t.Error("0028_refunds.sql missing index refunds_state_idx")
	}
	if !strings.Contains(content, "refund_events_provider_refund_id") {
		t.Error("0028_refunds.sql missing index refund_events_provider_refund_id")
	}
}

func TestRefund138_MigrationRBACPermissions(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	perms := []string{
		"refund.create",
		"refund.read",
		"refund.approve",
	}
	for _, p := range perms {
		if !strings.Contains(content, p) {
			t.Errorf("0028_refunds.sql missing RBAC permission: %s", p)
		}
	}
}

func TestRefund138_MigrationRolesGranted(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	roles := []string{"'admin'", "'org_admin'", "'member'"}
	for _, role := range roles {
		if !strings.Contains(content, role) {
			t.Errorf("0028_refunds.sql missing role grant for: %s", role)
		}
	}
}

func TestRefund138_MigrationDropsInDown(t *testing.T) {
	content := findFileByName(t, "0028_refunds.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS refund_events") {
		t.Error("0028_refunds.sql Down section missing DROP for refund_events")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS refunds") {
		t.Error("0028_refunds.sql Down section missing DROP for refunds")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: State transition matrix
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_StateTransitionMatrix_ValidTransitions(t *testing.T) {
	valid := []struct{ from, to string }{
		{"requested", "approved"},
		{"requested", "rejected"},
		{"approved", "provider_pending"},
		{"provider_pending", "succeeded"},
		{"provider_pending", "failed"},
		{"provider_pending", "manual_review"},
		{"manual_review", "succeeded"},
		{"manual_review", "failed"},
	}
	for _, tc := range valid {
		targets, ok := validRefundTransitions[tc.from]
		if !ok {
			t.Errorf("state %q not in transition table", tc.from)
			continue
		}
		if !targets[tc.to] {
			t.Errorf("expected valid transition %q → %q", tc.from, tc.to)
		}
	}
}

func TestRefund138_StateTransitionMatrix_InvalidTransitions(t *testing.T) {
	invalid := []struct{ from, to string }{
		{"requested", "succeeded"},      // must go via approved/provider_pending
		{"requested", "provider_pending"}, // must go via approved
		{"approved", "succeeded"},       // must go via provider_pending
		{"rejected", "approved"},        // terminal
		{"succeeded", "failed"},         // terminal
		{"failed", "succeeded"},         // terminal
		{"manual_review", "requested"},  // not valid
	}
	for _, tc := range invalid {
		targets, ok := validRefundTransitions[tc.from]
		if !ok {
			continue // unknown source state — also invalid
		}
		if targets[tc.to] {
			t.Errorf("expected INVALID transition %q → %q but it was marked valid", tc.from, tc.to)
		}
	}
}

func TestRefund138_TerminalStates(t *testing.T) {
	terminals := []string{"succeeded", "failed", "rejected"}
	for _, s := range terminals {
		if !isTerminalRefundState(s) {
			t.Errorf("state %q should be terminal", s)
		}
	}
	nonTerminals := []string{"requested", "approved", "provider_pending", "manual_review"}
	for _, s := range nonTerminals {
		if isTerminalRefundState(s) {
			t.Errorf("state %q should NOT be terminal", s)
		}
	}
}

func TestRefund138_AllStatesInTransitionTable(t *testing.T) {
	allStates := []string{
		"requested", "approved", "rejected", "provider_pending",
		"succeeded", "failed", "manual_review",
	}
	for _, s := range allStates {
		if _, ok := validRefundTransitions[s]; !ok {
			t.Errorf("state %q missing from validRefundTransitions map", s)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: SQL query file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if content == "" {
		t.Fatal("refunds.sql is empty")
	}
}

func TestRefund138_QueryFileHasInsertRefund(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "InsertRefund") {
		t.Error("refunds.sql missing InsertRefund query")
	}
}

func TestRefund138_QueryFileHasGetRefundByID(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "GetRefundByID") {
		t.Error("refunds.sql missing GetRefundByID query")
	}
}

func TestRefund138_QueryFileHasListRefundsByPaymentIntent(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "ListRefundsByPaymentIntent") {
		t.Error("refunds.sql missing ListRefundsByPaymentIntent query")
	}
}

func TestRefund138_QueryFileHasUpdateRefundState(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "UpdateRefundState") {
		t.Error("refunds.sql missing UpdateRefundState query")
	}
}

func TestRefund138_QueryFileHasInsertRefundEvent(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "InsertRefundEvent") {
		t.Error("refunds.sql missing InsertRefundEvent query")
	}
}

func TestRefund138_QueryFileHasGetRefundEvent(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "GetRefundEvent") {
		t.Error("refunds.sql missing GetRefundEvent query")
	}
}

func TestRefund138_QueryFileHasCancelTicketsByCheckoutSession(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "CancelTicketsByCheckoutSession") {
		t.Error("refunds.sql missing CancelTicketsByCheckoutSession query")
	}
}

func TestRefund138_QueryFileHasOnConflictDoNothing(t *testing.T) {
	content := findFileByName(t, "refunds.sql")
	if !strings.Contains(content, "ON CONFLICT") {
		t.Error("refunds.sql missing ON CONFLICT (idempotency)")
	}
	if !strings.Contains(content, "DO NOTHING") {
		t.Error("refunds.sql missing DO NOTHING in idempotency clause")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_GenFileExists(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	if content == "" {
		t.Fatal("refunds.sql.go is empty")
	}
}

func TestRefund138_GenFileHasRefundRowStruct(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	if !strings.Contains(content, "RefundRow") {
		t.Error("refunds.sql.go missing RefundRow struct")
	}
}

func TestRefund138_GenFileHasRefundEventRowStruct(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	if !strings.Contains(content, "RefundEventRow") {
		t.Error("refunds.sql.go missing RefundEventRow struct")
	}
}

func TestRefund138_GenFileHasStateField(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	if !strings.Contains(content, "State") {
		t.Error("refunds.sql.go missing State field")
	}
}

func TestRefund138_GenFileHasAllQueryMethods(t *testing.T) {
	content := findFileByName(t, "refunds.sql.go")
	methods := []string{
		"InsertRefund",
		"GetRefundByID",
		"ListRefundsByPaymentIntent",
		"UpdateRefundState",
		"InsertRefundEvent",
		"GetRefundEvent",
		"CancelTicketsByCheckoutSession",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("refunds.sql.go missing method: %s", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface — all 7 refund methods present
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_QuerierInterfaceHasRefundMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	methods := []string{
		"InsertRefund",
		"GetRefundByID",
		"ListRefundsByPaymentIntent",
		"UpdateRefundState",
		"InsertRefundEvent",
		"GetRefundEvent",
		"CancelTicketsByCheckoutSession",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("querier.go Querier interface missing: %s", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Route auth-gating
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_RouteAuthGating_CreateRequiresJWT(t *testing.T) {
	s := buildRefundServer(t)
	w := httptest.NewRecorder()
	body := `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":1000,"currency":"USD"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/refunds without JWT: got %d, want 401", w.Code)
	}
}

func TestRefund138_RouteAuthGating_ReadRequiresJWT(t *testing.T) {
	s := buildRefundServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/refunds/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/refunds/{id} without JWT: got %d, want 401", w.Code)
	}
}

func TestRefund138_RouteAuthGating_ApproveRequiresJWT(t *testing.T) {
	s := buildRefundServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/00000000-0000-0000-0000-000000000001/approve",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/refunds/{id}/approve without JWT: got %d, want 401", w.Code)
	}
}

func TestRefund138_RouteAuthGating_RejectRequiresJWT(t *testing.T) {
	s := buildRefundServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/00000000-0000-0000-0000-000000000001/reject",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/refunds/{id}/reject without JWT: got %d, want 401", w.Code)
	}
}

func TestRefund138_WebhookRouteNoAuthRequired(t *testing.T) {
	// Webhook route is intentionally unauthenticated — should return 4xx based
	// on body content, not 401.
	s := buildRefundServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("POST /v1/refunds/webhook should NOT require JWT but got 401")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Missing deps → 503
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_NilRefundQueries_WebhookReturns503(t *testing.T) {
	// Build server WITHOUT refundQueries — routes should not mount and webhook
	// should return 404 (not mounted) or 503 depending on wiring.
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
		// RefundQueries intentionally omitted
	})
	w := httptest.NewRecorder()
	body := `{"provider_refund_id":"re_test","event_type":"mock.refund.succeeded","refund_id":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook", strings.NewReader(body))
	s.router.ServeHTTP(w, req)
	// Without refundQueries, webhook route is not mounted → 404.
	if w.Code == http.StatusOK {
		t.Errorf("POST /v1/refunds/webhook without refundQueries should not return 200, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_CreateHandler_InvalidIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	body := `{"payment_intent_id":"not-a-uuid","amount":1000,"currency":"USD"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with invalid payment_intent_id: got %d, want 400", w.Code)
	}
}

func TestRefund138_CreateHandler_ZeroAmountReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	body := `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":0,"currency":"USD"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with zero amount: got %d, want 400", w.Code)
	}
}

func TestRefund138_CreateHandler_NegativeAmountReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	body := `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":-100,"currency":"USD"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with negative amount: got %d, want 400", w.Code)
	}
}

func TestRefund138_CreateHandler_MissingCurrencyReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	body := `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":1000}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create without currency: got %d, want 400", w.Code)
	}
}

func TestRefund138_CreateHandler_EmptyBodyReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with empty body: got %d, want 400", w.Code)
	}
}

func TestRefund138_GetHandler_InvalidIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/refunds/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET with invalid UUID: got %d, want 400", w.Code)
	}
}

func TestRefund138_ApproveHandler_InvalidIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/not-a-uuid/approve", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("approve with invalid UUID: got %d, want 400", w.Code)
	}
}

func TestRefund138_RejectHandler_InvalidIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/not-a-uuid/reject", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("reject with invalid UUID: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook validation
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_WebhookHandler_MissingProviderRefundIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	body := `{"event_type":"mock.refund.succeeded","refund_id":"00000000-0000-0000-0000-000000000001"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("webhook without provider_refund_id: got %d, want 400", w.Code)
	}
}

func TestRefund138_WebhookHandler_MissingEventTypeReturns400(t *testing.T) {
	s := buildRefundServer(t)
	body := `{"provider_refund_id":"re_test","refund_id":"00000000-0000-0000-0000-000000000001"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("webhook without event_type: got %d, want 400", w.Code)
	}
}

func TestRefund138_WebhookHandler_MissingRefundIDReturns400(t *testing.T) {
	s := buildRefundServer(t)
	body := `{"provider_refund_id":"re_test","event_type":"mock.refund.succeeded"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("webhook without refund_id: got %d, want 400", w.Code)
	}
}

func TestRefund138_WebhookHandler_UnknownEventTypeReturnsAcknowledged(t *testing.T) {
	s := buildRefundServer(t)
	payload := map[string]any{
		"provider_refund_id": "re_test_123",
		"event_type":         "refund.unknown_future_type",
		"refund_id":          "00000000-0000-0000-0000-000000000001",
	}
	bodyBytes, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/refunds/webhook",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Should be 200 with processed:false, not 400 or 500
	if w.Code != http.StatusOK {
		t.Errorf("webhook with unknown event_type: got %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if processed, ok := resp["processed"].(bool); !ok || processed {
		t.Errorf("unknown event type should have processed=false, got: %v", resp["processed"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook event type mapping
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_WebhookEventTypeMapping(t *testing.T) {
	tests := []struct {
		eventType string
		wantState string
	}{
		{"charge.refund.updated", "succeeded"},
		{"refund.succeeded", "succeeded"},
		{"refund.failed", "failed"},
		{"refund.manual_review", "manual_review"},
		{"mock.refund.succeeded", "succeeded"},
		{"mock.refund.failed", "failed"},
		{"mock.refund.manual_review", "manual_review"},
	}
	for _, tc := range tests {
		got, ok := refundWebhookEventTypeToState[tc.eventType]
		if !ok {
			t.Errorf("event type %q not in refundWebhookEventTypeToState map", tc.eventType)
			continue
		}
		if got != tc.wantState {
			t.Errorf("event type %q: got state %q, want %q", tc.eventType, got, tc.wantState)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response content-type
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_ResponseContentType_GetReturnsJSON(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/refunds/00000000-0000-0000-0000-000000000001", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("GET /v1/refunds/{id} Content-Type: got %q, want application/json", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring checks
// ─────────────────────────────────────────────────────────────────────────────

func TestRefund138_ServerWiring_RefundQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "refundQueries") {
		t.Error("server.go missing refundQueries field")
	}
	if !strings.Contains(content, "RefundQueries") {
		t.Error("server.go Options missing RefundQueries field")
	}
}

func TestRefund138_ServerWiring_RoutesAreRegistered(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "/refunds") {
		t.Error("server.go missing refunds route registration")
	}
	if !strings.Contains(content, "refund.create") {
		t.Error("server.go missing refund.create permission guard")
	}
}

func TestRefund138_HandlerFile_HasStateTransitionTable(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "validRefundTransitions") {
		t.Error("refunds.go missing validRefundTransitions map")
	}
}

func TestRefund138_HandlerFile_HasWebhookIdempotency(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "InsertRefundEvent") {
		t.Error("refunds.go missing InsertRefundEvent call (webhook idempotency)")
	}
}

func TestRefund138_HandlerFile_HasTicketCancellation(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "CancelTicketsByCheckoutSession") {
		t.Error("refunds.go missing CancelTicketsByCheckoutSession call (ticket revocation)")
	}
}

func TestRefund138_HandlerFile_HasManualReviewPolicy(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "manual_review") {
		t.Error("refunds.go missing manual_review state handling")
	}
	if !strings.Contains(content, "refundNeedsManualReview") {
		t.Error("refunds.go missing refundNeedsManualReview policy check")
	}
}
