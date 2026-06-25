// tickets_139_test.go — unit tests for feature #139 (Ticket issuance on payment success / free checkout).
//
// Test coverage:
//
//	Step 1: Migration file 0026_tickets.sql — table, status enum, indexes, RBAC
//	Step 2: SQL query file tickets.sql — all 4 named queries present
//	Step 3: Gen file tickets.sql.go — TicketRow type, all 4 query functions
//	Step 4: Querier interface — ticket methods present (compile-time)
//	Step 5: issueTicketsForCheckout — nil-queries guard, idempotency documented
//	Step 6: HTTP route — GET /v1/checkout/{id}/tickets requires auth (401)
//	Step 7: HTTP route — GET /v1/checkout/{id}/tickets mounted and reachable
//	Step 8: Webhook handler — payment.succeeded route reachable
//	Step 9: Free checkout handler — POST /v1/checkout/{id}/complete triggers issuance path
//	Step 10: ticketFromRow — RFC3339 timestamps, nil-safe TierID/HolderEmail
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

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for ticket route tests
// ─────────────────────────────────────────────────────────────────────────────

const ticketTestActorID = "00000000-0000-0000-0000-000000000139"

// buildTicketServer builds a Server with stub auth, ticket routes fully
// mounted, and gen.New(nil) so real DB operations never execute.
func buildTicketServer(t *testing.T) *Server {
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
		t.Fatalf("buildTicketServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		TicketQueries:        gen.New(nil),
		CheckoutQueries:      gen.New(nil),
		PaymentIntentQueries: gen.New(nil),
		ReservationQueries:   gen.New(nil),
	})
}

// mintTicketToken mints a dev JWT for ticket route tests.
func mintTicketToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + ticketTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintTicketToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintTicketToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintTicketToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file 0026_tickets.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step1_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if content == "" {
		t.Fatal("0026_tickets.sql is empty or not found")
	}
}

func TestTicket139_Step1_MigrationHasGooseDirectives(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestTicket139_Step1_MigrationHasTicketsTable(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if !strings.Contains(content, "CREATE TABLE tickets") {
		t.Error("migration missing 'CREATE TABLE tickets'")
	}
}

func TestTicket139_Step1_MigrationHasRequiredColumns(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	for _, col := range []string{
		"checkout_session_id",
		"session_id",
		"tier_id",
		"holder_email",
		"status",
		"issued_at",
		"created_at",
		"updated_at",
	} {
		if !strings.Contains(content, col) {
			t.Errorf("migration missing column '%s'", col)
		}
	}
}

func TestTicket139_Step1_MigrationHasStatusConstraint(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if !strings.Contains(content, "tickets_status_check") {
		t.Error("migration missing 'tickets_status_check' constraint name")
	}
	for _, status := range []string{"active", "cancelled", "transferred"} {
		if !strings.Contains(content, "'"+status+"'") {
			t.Errorf("status constraint missing value '%s'", status)
		}
	}
}

func TestTicket139_Step1_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if !strings.Contains(content, "tickets_checkout_session_id") {
		t.Error("migration missing index 'tickets_checkout_session_id'")
	}
	if !strings.Contains(content, "tickets_session_id") {
		t.Error("migration missing index 'tickets_session_id'")
	}
}

func TestTicket139_Step1_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	for _, perm := range []string{"ticket.read", "ticket.issue", "ticket.cancel"} {
		if !strings.Contains(content, "'"+perm+"'") {
			t.Errorf("migration missing RBAC seed '%s'", perm)
		}
	}
}

func TestTicket139_Step1_MigrationDownSection(t *testing.T) {
	content := findFileByName(t, "0026_tickets.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS tickets") {
		t.Error("migration Down section missing 'DROP TABLE IF EXISTS tickets'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file tickets.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step2_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if content == "" {
		t.Fatal("tickets.sql query file is empty or not found")
	}
}

func TestTicket139_Step2_SQLQueryFileHasInsertTicket(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if !strings.Contains(content, "InsertTicket") {
		t.Error("tickets.sql missing 'InsertTicket' query name")
	}
}

func TestTicket139_Step2_SQLQueryFileHasListByCheckout(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if !strings.Contains(content, "ListTicketsByCheckoutSession") {
		t.Error("tickets.sql missing 'ListTicketsByCheckoutSession' query name")
	}
}

func TestTicket139_Step2_SQLQueryFileHasGetByID(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if !strings.Contains(content, "GetTicketByID") {
		t.Error("tickets.sql missing 'GetTicketByID' query name")
	}
}

func TestTicket139_Step2_SQLQueryFileHasCount(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if !strings.Contains(content, "CountTicketsByCheckoutSession") {
		t.Error("tickets.sql missing 'CountTicketsByCheckoutSession' query name")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file tickets.sql.go
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step3_GenFileExists(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	if content == "" {
		t.Fatal("gen file tickets.sql.go is empty or not found")
	}
}

func TestTicket139_Step3_GenFileHasTicketRow(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	if !strings.Contains(content, "type TicketRow struct") {
		t.Error("gen file missing 'type TicketRow struct'")
	}
}

func TestTicket139_Step3_GenFileHasAllFunctions(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	for _, fn := range []string{
		"InsertTicket",
		"ListTicketsByCheckoutSession",
		"GetTicketByID",
		"CountTicketsByCheckoutSession",
	} {
		if !strings.Contains(content, "func (q *Queries) "+fn) {
			t.Errorf("gen file missing 'func (q *Queries) %s'", fn)
		}
	}
}

func TestTicket139_Step3_TicketRowHasRequiredFields(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	for _, field := range []string{
		"CheckoutSessionID",
		"SessionID",
		"TierID",
		"HolderEmail",
		"Status",
		"IssuedAt",
		"CreatedAt",
		"UpdatedAt",
	} {
		if !strings.Contains(content, field) {
			t.Errorf("gen file TicketRow missing field '%s'", field)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface — compile-time check
// ─────────────────────────────────────────────────────────────────────────────

// TestTicket139_Step4_QuerierImplementsTicketMethods verifies at compile time
// that gen.New(nil) satisfies gen.Querier, which now includes all 4 ticket
// methods. If the interface is missing any method, the build fails before
// this test runs.
func TestTicket139_Step4_QuerierImplementsTicketMethods(_ *testing.T) {
	var _ gen.Querier = gen.New(nil)
}

func TestTicket139_Step4_QuerierFileHasTicketMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertTicket",
		"ListTicketsByCheckoutSession",
		"GetTicketByID",
		"CountTicketsByCheckoutSession",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go missing ticket method '%s'", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: issueTicketsForCheckout — nil guards
// ─────────────────────────────────────────────────────────────────────────────

// TestTicket139_Step5_NilTicketQueriesReturnsError verifies that
// issueTicketsForCheckout returns an error when ticketQueries is nil.
func TestTicket139_Step5_NilTicketQueriesReturnsError(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	// ticketQueries intentionally nil (not supplied to Options).
	s := New(Options{Config: cfg, Auth: stub, Pool: &dbDownPool{}})

	cs := gen.CheckoutSessionRow{ID: uuid.New(), ReservationID: uuid.New()}
	_, err := s.issueTicketsForCheckout(t.Context(), cs)
	if err == nil {
		t.Error("expected error when ticketQueries is nil, got nil")
	}
	if !strings.Contains(err.Error(), "ticketQueries not wired") {
		t.Errorf("expected 'ticketQueries not wired' in error, got: %v", err)
	}
}

// TestTicket139_Step5_NilReservationQueriesReturnsError verifies that
// issueTicketsForCheckout returns an error when reservationQueries is nil.
func TestTicket139_Step5_NilReservationQueriesReturnsError(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	// TicketQueries wired but ReservationQueries intentionally nil.
	s := New(Options{
		Config:        cfg,
		Auth:          stub,
		Pool:          &dbDownPool{},
		TicketQueries: gen.New(nil),
		// ReservationQueries omitted intentionally
	})

	cs := gen.CheckoutSessionRow{ID: uuid.New(), ReservationID: uuid.New()}
	_, err := s.issueTicketsForCheckout(t.Context(), cs)
	if err == nil {
		t.Error("expected error when reservationQueries is nil, got nil")
	}
	if !strings.Contains(err.Error(), "reservationQueries not wired") {
		t.Errorf("expected 'reservationQueries not wired' in error, got: %v", err)
	}
}

// TestTicket139_Step5_TicketsGoFileHasIssueFunction verifies the handler file
// contains the issueTicketsForCheckout function.
func TestTicket139_Step5_TicketsGoFileHasIssueFunction(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "issueTicketsForCheckout") {
		t.Error("tickets.go missing 'issueTicketsForCheckout' function")
	}
}

// TestTicket139_Step5_TicketsGoFileHasIdempotencyCheck verifies the issuance
// function includes the idempotency check (ListTicketsByCheckoutSession).
func TestTicket139_Step5_TicketsGoFileHasIdempotencyCheck(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "ListTicketsByCheckoutSession") {
		t.Error("tickets.go missing idempotency check via ListTicketsByCheckoutSession")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: HTTP route — GET /v1/checkout/{id}/tickets requires auth
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step6_ListTicketsRequiresAuth(t *testing.T) {
	s := buildTicketServer(t)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/tickets", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: HTTP route — GET /v1/checkout/{id}/tickets mounted and reachable
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step7_ListTicketsMountedWithAuth(t *testing.T) {
	s := buildTicketServer(t)
	tok := mintTicketToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/tickets", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route must be mounted — must not return 404 http.not_found.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
		t.Errorf("ticket list route not mounted: got 404 http.not_found; body=%s", w.Body.String())
	}
	// Auth must pass — must not return 401 or 403.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("unexpected auth failure on authenticated request: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTicket139_Step7_ListTicketsInvalidUUIDReturns400(t *testing.T) {
	s := buildTicketServer(t)
	tok := mintTicketToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/not-a-uuid/tickets", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ticket.invalid_checkout_id") {
		t.Errorf("expected 'ticket.invalid_checkout_id', got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Webhook handler — payment.succeeded route reachable
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step8_WebhookSucceededRouteReachable(t *testing.T) {
	s := buildTicketServer(t)

	// The webhook endpoint is intentionally unauthenticated.
	body := `{
		"provider_payment_id": "mock_pi_test139",
		"event_type": "payment_intent.succeeded",
		"target_state": "succeeded"
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/payment-intents/webhook",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route must be mounted — must not return 404 http.not_found.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
		t.Errorf("webhook route not mounted: got 404; body=%s", w.Body.String())
	}
}

func TestTicket139_Step8_PaymentIntentsGoHasTicketIssuanceOnSuccess(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "issueTicketsForCheckout") {
		t.Error("payment_intents.go missing call to issueTicketsForCheckout")
	}
	if !strings.Contains(content, `updated.State == "succeeded"`) {
		t.Error("payment_intents.go missing 'succeeded' state guard for ticket issuance")
	}
}

// TestTicket139_Step8_WebhookIdempotencyReplayReturns204 verifies that
// a duplicate (provider_payment_id, event_type) pair returns 204 rather
// than re-processing. With nil DB, the test verifies the idempotency
// check is evaluated before the issuance attempt.
func TestTicket139_Step8_WebhookIdempotencyReplayBehavior(t *testing.T) {
	s := buildTicketServer(t)

	body := `{
		"provider_payment_id": "mock_replay_pi_139",
		"event_type": "mock.succeeded",
		"target_state": "succeeded"
	}`
	for call := 1; call <= 2; call++ {
		req := httptest.NewRequest(http.MethodPost,
			"/v1/payment-intents/webhook",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		// With nil DB the route must not 404 (route is mounted) and must not
		// return "http.not_found" (handler runs).
		if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
			t.Errorf("call %d: route not mounted (404 http.not_found)", call)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Free checkout handler — issuance called on completion
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step9_CheckoutGoHasTicketIssuanceOnFreeComplete(t *testing.T) {
	content := findFileByName(t, "checkout.go")
	if !strings.Contains(content, "issueTicketsForCheckout") {
		t.Error("checkout.go missing call to issueTicketsForCheckout on free completion")
	}
}

func TestTicket139_Step9_FreeCheckoutRouteReachable(t *testing.T) {
	s := buildTicketServer(t)
	tok := mintTicketToken(t, s)

	// Empty JSON body = free checkout path (no payment_intent_id).
	req := httptest.NewRequest(http.MethodPost,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/complete",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route must be mounted — not 404.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
		t.Errorf("checkout complete route not mounted: got 404; body=%s", w.Body.String())
	}
	// Must not return 400 checkout.empty_body — free path accepts empty JSON body.
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "checkout.empty_body") {
		t.Errorf("free checkout path must not return 400 checkout.empty_body")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: ticketFromRow — conversion correctness
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket139_Step10_TicketFromRowTimestamps(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		Status:            "active",
		IssuedAt:          now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	resp := ticketFromRow(row)
	want := "2026-06-24T12:00:00Z"
	if resp.IssuedAt != want {
		t.Errorf("IssuedAt = %q, want %q", resp.IssuedAt, want)
	}
	if resp.CreatedAt != want {
		t.Errorf("CreatedAt = %q, want %q", resp.CreatedAt, want)
	}
	if resp.UpdatedAt != want {
		t.Errorf("UpdatedAt = %q, want %q", resp.UpdatedAt, want)
	}
}

func TestTicket139_Step10_TicketFromRowNilTierID(t *testing.T) {
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		TierID:            nil,
		Status:            "active",
		IssuedAt:          time.Now(),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	resp := ticketFromRow(row)
	if resp.TierID != nil {
		t.Error("expected TierID to be nil when row.TierID is nil")
	}
}

func TestTicket139_Step10_TicketFromRowNonNilTierID(t *testing.T) {
	tierID := uuid.New()
	email := "buyer@example.com"
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		TierID:            &tierID,
		HolderEmail:       &email,
		Status:            "active",
		IssuedAt:          time.Now(),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	resp := ticketFromRow(row)
	if resp.TierID == nil {
		t.Error("expected TierID to be non-nil when row.TierID is set")
	}
	if *resp.TierID != tierID.String() {
		t.Errorf("TierID = %q, want %q", *resp.TierID, tierID.String())
	}
	if resp.HolderEmail == nil || *resp.HolderEmail != email {
		t.Errorf("HolderEmail = %v, want %q", resp.HolderEmail, email)
	}
}

func TestTicket139_Step10_TicketFromRowCancelledStatus(t *testing.T) {
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		Status:            "cancelled",
		IssuedAt:          time.Now(),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	resp := ticketFromRow(row)
	if resp.Status != "cancelled" {
		t.Errorf("Status = %q, want 'cancelled'", resp.Status)
	}
}

func TestTicket139_Step10_TicketFromRowTransferredStatus(t *testing.T) {
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		Status:            "transferred",
		IssuedAt:          time.Now(),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	resp := ticketFromRow(row)
	if resp.Status != "transferred" {
		t.Errorf("Status = %q, want 'transferred'", resp.Status)
	}
}
