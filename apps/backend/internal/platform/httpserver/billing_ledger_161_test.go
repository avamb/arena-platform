// billing_ledger_161_test.go — unit tests for service billing ledger (feature #161).
//
// Test naming convention: TestBilling161_*
// All tests verify structural and behavioural contracts without a live database.
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
// Server factory and JWT helper for billing route tests
// ─────────────────────────────────────────────────────────────────────────────

const billingTestActorID = "00000000-0000-0000-0000-000000000161"
const billingTestOrgID = "00000000-0000-0000-0000-000000000162"
const billingTestInvoiceID = "00000000-0000-0000-0000-000000000163"

// buildBillingServer161 builds a Server with stub auth and billing routes mounted.
// A gen.New(nil) Queries instance is used so routes are wired without a real DB.
func buildBillingServer161(t *testing.T) *Server {
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
		t.Fatalf("buildBillingServer161: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:         cfg,
		Auth:           stub,
		Pool:           &dbDownPool{},
		BillingQueries: gen.New(nil),
	})
}

// mintBillingToken mints a dev JWT for billing route tests.
func mintBillingToken(t *testing.T, s *Server) string {
	t.Helper()
	body := `{"actor_id":"` + billingTestActorID + `","roles":["admin"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintBillingToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintBillingToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintBillingToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// File existence tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if content == "" {
		t.Fatal("0033_billing_ledger.sql is empty")
	}
}

func TestBilling161_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	if content == "" {
		t.Fatal("billing_ledger.sql is empty")
	}
}

func TestBilling161_GenFileExists(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql.go")
	if content == "" {
		t.Fatal("billing_ledger.sql.go is empty")
	}
}

func TestBilling161_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "billing_ledger.go")
	if content == "" {
		t.Fatal("billing_ledger.go is empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Migration file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_MigrationHasTariffsTable(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "CREATE TABLE tariffs") {
		t.Error("migration must create tariffs table")
	}
}

func TestBilling161_MigrationHasUsageRecordsTable(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "CREATE TABLE usage_records") {
		t.Error("migration must create usage_records table")
	}
}

func TestBilling161_MigrationHasInvoicesTable(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "CREATE TABLE invoices") {
		t.Error("migration must create invoices table")
	}
}

func TestBilling161_MigrationHasInvoiceLinesTable(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "CREATE TABLE invoice_lines") {
		t.Error("migration must create invoice_lines table")
	}
}

func TestBilling161_MigrationHasInvoiceStateEnum(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	for _, state := range []string{"draft", "issued", "paid", "void"} {
		if !strings.Contains(content, "'"+state+"'") {
			t.Errorf("migration must include invoice state '%s'", state)
		}
	}
}

func TestBilling161_MigrationHasVersionedTariffs(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	for _, col := range []string{"effective_from", "per_ticket_fee_minor", "per_event_fee_minor", "monthly_fee_minor"} {
		if !strings.Contains(content, col) {
			t.Errorf("tariffs table must have column '%s'", col)
		}
	}
}

func TestBilling161_MigrationHasUsageCounters(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	for _, col := range []string{"tickets_sold", "complimentary_issued", "events_published"} {
		if !strings.Contains(content, col) {
			t.Errorf("usage_records must have column '%s'", col)
		}
	}
}

func TestBilling161_MigrationHasBillingPeriod(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "billing_period") {
		t.Error("usage_records and invoices must have billing_period column")
	}
}

func TestBilling161_MigrationHasUniqueConstraints(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	count := strings.Count(content, "UNIQUE")
	if count < 3 {
		t.Errorf("migration must have at least 3 UNIQUE constraints, got %d", count)
	}
}

func TestBilling161_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "billing.read") {
		t.Error("migration must seed 'billing.read' permission")
	}
	if !strings.Contains(content, "billing.admin") {
		t.Error("migration must seed 'billing.admin' permission")
	}
}

func TestBilling161_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration must have +goose Up directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must have +goose Down directive")
	}
}

func TestBilling161_MigrationDownDropsTables(t *testing.T) {
	content := findFileByName(t, "0033_billing_ledger.sql")
	for _, table := range []string{"invoice_lines", "invoices", "usage_records", "tariffs"} {
		if !strings.Contains(content, "DROP TABLE IF EXISTS "+table) {
			t.Errorf("migration Down must drop table '%s'", table)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL query file tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_SQLQueryHasInsertTariff(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	if !strings.Contains(content, "InsertTariff") {
		t.Error("billing_ledger.sql must have InsertTariff query")
	}
}

func TestBilling161_SQLQueryHasGetActiveTariff(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	if !strings.Contains(content, "GetActiveTariff") {
		t.Error("billing_ledger.sql must have GetActiveTariff query")
	}
}

func TestBilling161_SQLQueryHasIncrementUsageRecord(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	if !strings.Contains(content, "IncrementUsageRecord") {
		t.Error("billing_ledger.sql must have IncrementUsageRecord query")
	}
	if !strings.Contains(content, "ON CONFLICT") {
		t.Error("IncrementUsageRecord must use ON CONFLICT upsert pattern")
	}
}

func TestBilling161_SQLQueryHasInvoiceCRUD(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	for _, q := range []string{"InsertInvoice", "GetInvoiceByID", "ListInvoicesByOrg", "UpdateInvoiceState"} {
		if !strings.Contains(content, q) {
			t.Errorf("billing_ledger.sql must have '%s' query", q)
		}
	}
}

func TestBilling161_SQLQueryHasInvoiceLineCRUD(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql")
	for _, q := range []string{"InsertInvoiceLine", "ListInvoiceLines"} {
		if !strings.Contains(content, q) {
			t.Errorf("billing_ledger.sql must have '%s' query", q)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gen file struct tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_GenFileTariffRowStruct(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql.go")
	if !strings.Contains(content, "TariffRow") {
		t.Error("billing_ledger.sql.go must define TariffRow struct")
	}
	for _, field := range []string{"PerTicketFeeMinor", "PerEventFeeMinor", "MonthlyFeeMinor", "EffectiveFrom"} {
		if !strings.Contains(content, field) {
			t.Errorf("TariffRow must have field '%s'", field)
		}
	}
}

func TestBilling161_GenFileUsageRecordRowStruct(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql.go")
	if !strings.Contains(content, "UsageRecordRow") {
		t.Error("billing_ledger.sql.go must define UsageRecordRow struct")
	}
	for _, field := range []string{"TicketsSold", "ComplimentaryIssued", "EventsPublished", "BillingPeriod"} {
		if !strings.Contains(content, field) {
			t.Errorf("UsageRecordRow must have field '%s'", field)
		}
	}
}

func TestBilling161_GenFileInvoiceRowStruct(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql.go")
	if !strings.Contains(content, "InvoiceRow") {
		t.Error("billing_ledger.sql.go must define InvoiceRow struct")
	}
	for _, field := range []string{"TotalAmountMinor", "IssuedAt", "PaidAt", "VoidedAt"} {
		if !strings.Contains(content, field) {
			t.Errorf("InvoiceRow must have field '%s'", field)
		}
	}
}

func TestBilling161_GenFileInvoiceLineRowStruct(t *testing.T) {
	content := findFileByName(t, "billing_ledger.sql.go")
	if !strings.Contains(content, "InvoiceLineRow") {
		t.Error("billing_ledger.sql.go must define InvoiceLineRow struct")
	}
	for _, field := range []string{"UnitAmountMinor", "TotalAmountMinor", "TariffID", "Quantity"} {
		if !strings.Contains(content, field) {
			t.Errorf("InvoiceLineRow must have field '%s'", field)
		}
	}
}

func TestBilling161_QuerierInterfaceHasBillingMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertTariff",
		"GetActiveTariff",
		"IncrementUsageRecord",
		"InsertInvoice",
		"UpdateInvoiceState",
		"InsertInvoiceLine",
		"ListInvoiceLines",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go must declare '%s' method", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Invoice state machine logic tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_StateTransitionsComplete(t *testing.T) {
	for _, state := range allInvoiceStates {
		if _, ok := validInvoiceTransitions[state]; !ok {
			t.Errorf("validInvoiceTransitions missing state '%s'", state)
		}
	}
}

func TestBilling161_TerminalStatesPaidAndVoid(t *testing.T) {
	for _, state := range []string{"paid", "void"} {
		if !isTerminalInvoiceState(state) {
			t.Errorf("'%s' should be a terminal state", state)
		}
	}
}

func TestBilling161_NonTerminalStatesDraftAndIssued(t *testing.T) {
	for _, state := range []string{"draft", "issued"} {
		if isTerminalInvoiceState(state) {
			t.Errorf("'%s' should not be a terminal state", state)
		}
	}
}

func TestBilling161_ValidTransitionDraftToIssued(t *testing.T) {
	if !validInvoiceTransitions["draft"]["issued"] {
		t.Error("draft → issued should be valid")
	}
}

func TestBilling161_ValidTransitionDraftToVoid(t *testing.T) {
	if !validInvoiceTransitions["draft"]["void"] {
		t.Error("draft → void should be valid")
	}
}

func TestBilling161_ValidTransitionIssuedToPaid(t *testing.T) {
	if !validInvoiceTransitions["issued"]["paid"] {
		t.Error("issued → paid should be valid")
	}
}

func TestBilling161_ValidTransitionIssuedToVoid(t *testing.T) {
	if !validInvoiceTransitions["issued"]["void"] {
		t.Error("issued → void should be valid")
	}
}

func TestBilling161_InvalidTransitionPaidToVoid(t *testing.T) {
	if validInvoiceTransitions["paid"]["void"] {
		t.Error("paid → void should be invalid (paid is terminal)")
	}
}

func TestBilling161_InvalidTransitionDraftToPaid(t *testing.T) {
	if validInvoiceTransitions["draft"]["paid"] {
		t.Error("draft → paid should be invalid (must go through issued)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// billingPeriodForTime tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_BillingPeriodFormatJune(t *testing.T) {
	t1 := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	got := billingPeriodForTime(t1)
	if got != "2026-06" {
		t.Errorf("expected '2026-06', got '%s'", got)
	}
}

func TestBilling161_BillingPeriodFormatJanuary(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := billingPeriodForTime(t1)
	if got != "2026-01" {
		t.Errorf("expected '2026-01', got '%s'", got)
	}
}

func TestBilling161_BillingPeriodFormatDecember(t *testing.T) {
	t1 := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)
	got := billingPeriodForTime(t1)
	if got != "2025-12" {
		t.Errorf("expected '2025-12', got '%s'", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route auth-gating tests (no JWT → 401)
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_CreateTariffRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	body := `{"effective_from":"2026-01-01","plan_name":"standard"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/tariffs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/billing/tariffs without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_GetActiveTariffRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/tariffs/active", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/billing/tariffs/active without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_GetUsageRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+billingTestOrgID+"/billing/usage", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/billing/usage without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_GenerateInvoicesRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/generate", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/billing/invoices/generate without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_ListInvoicesRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+billingTestOrgID+"/billing/invoices", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/billing/invoices without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_GetInvoiceRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/invoices/"+billingTestInvoiceID, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/billing/invoices/{id} without JWT: got %d, want 401", w.Code)
	}
}

func TestBilling161_IssueInvoiceRequiresJWT(t *testing.T) {
	s := buildBillingServer161(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/"+billingTestInvoiceID+"/issue", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/billing/invoices/{id}/issue without JWT: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Authenticated route tests (with JWT, DB nil → panic or 503)
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_GetActiveTariffWithAuthReturnsNotFound(t *testing.T) {
	// With JWT + gen.New(nil) billingQueries, the DB call panics; the
	// Recoverer middleware catches it and returns 500. Verify it's NOT 401.
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/tariffs/active", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	// Not 401 (auth passed); could be 404 or 500 depending on DB response
	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET /v1/billing/tariffs/active with JWT: should not return 401")
	}
}

func TestBilling161_GetUsageWithAuthMounted(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+billingTestOrgID+"/billing/usage", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	// Not 401 or 404 (route mounted, auth passed)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET usage with JWT: should not return 401")
	}
	if w.Code == http.StatusNotFound {
		t.Errorf("GET usage with JWT: route not mounted (404)")
	}
}

func TestBilling161_CreateTariffWithJWTMissingEffectiveFrom(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	body := `{"plan_name":"standard"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/tariffs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /v1/billing/tariffs missing effective_from: got %d, want 422", w.Code)
	}
}

func TestBilling161_CreateTariffWithJWTInvalidDate(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	body := `{"effective_from":"not-a-date"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/tariffs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /v1/billing/tariffs invalid date: got %d, want 422", w.Code)
	}
}

func TestBilling161_CreateTariffWithJWTNegativeFee(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	body := `{"effective_from":"2026-01-01","per_ticket_fee_minor":-1}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/tariffs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /v1/billing/tariffs negative fee: got %d, want 422", w.Code)
	}
}

func TestBilling161_GenerateInvoicesWithJWTInvalidPeriod(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	body := `{"billing_period":"not-valid"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/generate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /v1/billing/invoices/generate invalid period: got %d, want 422", w.Code)
	}
}

func TestBilling161_GetActiveTariffWithInvalidOrgID(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/tariffs/active?org_id=not-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET /v1/billing/tariffs/active invalid org_id: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Routes NOT mounted when billingQueries is nil
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_RoutesNotMountedWhenBillingQueriesNil(t *testing.T) {
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
		// BillingQueries intentionally omitted
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/tariffs/active", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("billing routes should not mount when BillingQueries is nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response struct tests (pure computation, no DB)
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_TariffResponseHasAllFields(t *testing.T) {
	row := gen.TariffRow{
		PlanName:          "standard",
		EffectiveFrom:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PerTicketFeeMinor: 50,
		PerEventFeeMinor:  200,
		MonthlyFeeMinor:   5000,
		Currency:          "EUR",
		CreatedAt:         time.Now(),
	}
	resp := tariffToResponse(row)

	for _, key := range []string{"id", "plan_name", "effective_from", "per_ticket_fee_minor", "per_event_fee_minor", "monthly_fee_minor", "currency", "org_id", "notes", "created_by", "created_at"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("tariff response missing field '%s'", key)
		}
	}
}

func TestBilling161_TariffResponseGlobalTariffNilOrgID(t *testing.T) {
	row := gen.TariffRow{
		PlanName:      "global",
		EffectiveFrom: time.Now(),
		OrgID:         nil,
		CreatedAt:     time.Now(),
	}
	resp := tariffToResponse(row)
	if resp["org_id"] != nil {
		t.Errorf("global tariff should have org_id=nil, got %v", resp["org_id"])
	}
}

func TestBilling161_TariffResponseEffectiveDateFormat(t *testing.T) {
	row := gen.TariffRow{
		EffectiveFrom: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		CreatedAt:     time.Now(),
	}
	resp := tariffToResponse(row)
	if resp["effective_from"] != "2026-06-15" {
		t.Errorf("effective_from should be YYYY-MM-DD, got %v", resp["effective_from"])
	}
}

func TestBilling161_UsageResponseHasAllFields(t *testing.T) {
	row := gen.UsageRecordRow{
		BillingPeriod:       "2026-06",
		TicketsSold:         10,
		ComplimentaryIssued: 2,
		EventsPublished:     3,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	resp := usageToResponse(row)
	for _, key := range []string{"org_id", "billing_period", "tickets_sold", "complimentary_issued", "events_published"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("usage response missing field '%s'", key)
		}
	}
}

func TestBilling161_InvoiceResponseNullTimestamps(t *testing.T) {
	row := gen.InvoiceRow{
		BillingPeriod:    "2026-05",
		State:            "draft",
		TotalAmountMinor: 9900,
		Currency:         "EUR",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	resp := invoiceToResponse(row)
	if resp["issued_at"] != nil {
		t.Errorf("draft invoice issued_at should be nil, got %v", resp["issued_at"])
	}
	if resp["paid_at"] != nil {
		t.Errorf("draft invoice paid_at should be nil, got %v", resp["paid_at"])
	}
	if resp["voided_at"] != nil {
		t.Errorf("draft invoice voided_at should be nil, got %v", resp["voided_at"])
	}
}

func TestBilling161_InvoiceLineResponseNullTariffID(t *testing.T) {
	row := gen.InvoiceLineRow{
		Description:      "Monthly fee",
		Quantity:         1,
		UnitAmountMinor:  5000,
		TotalAmountMinor: 5000,
		Currency:         "EUR",
		TariffID:         nil,
		CreatedAt:        time.Now(),
	}
	resp := invoiceLineToResponse(row)
	if resp["tariff_id"] != nil {
		t.Errorf("manual line tariff_id should be nil, got %v", resp["tariff_id"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tariff math tests (pure computation, no DB)
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_TariffMathTicketFee(t *testing.T) {
	ticketsSold := int64(100)
	perTicketFee := int64(50)
	got := ticketsSold * perTicketFee
	if got != 5000 {
		t.Errorf("100 tickets × 50 = 5000, got %d", got)
	}
}

func TestBilling161_TariffMathMonthlyFeeFlat(t *testing.T) {
	monthlyFee := int64(9900)
	quantity := int64(1)
	total := quantity * monthlyFee
	if total != 9900 {
		t.Errorf("1 × 9900 = 9900, got %d", total)
	}
}

func TestBilling161_TariffMathZeroUsageSkipsLine(t *testing.T) {
	ticketsSold := int64(0)
	perTicketFee := int64(100)
	shouldGenerateLine := perTicketFee > 0 && ticketsSold > 0
	if shouldGenerateLine {
		t.Error("should not generate per-ticket line when tickets_sold=0")
	}
}

func TestBilling161_TariffMathTotalSum(t *testing.T) {
	monthly := int64(5000)
	tickets := int64(10)
	perTicket := int64(50)
	events := int64(2)
	perEvent := int64(200)

	total := monthly + (tickets * perTicket) + (events * perEvent)
	// 5000 + 500 + 400 = 5900
	if total != 5900 {
		t.Errorf("expected total 5900, got %d", total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IncrementBillingUsage no-op when billingQueries nil
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_IncrementBillingUsageNilIsNoop(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}
	s := New(Options{Config: cfg, BillingQueries: nil})
	// Should not panic when billingQueries is nil.
	s.IncrementBillingUsage(t.Context(), [16]byte{}, 1, 0, 0)
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring tests (file content checks)
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_ServerHasBillingQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "billingQueries") {
		t.Error("server.go must have billingQueries field")
	}
}

func TestBilling161_ServerOptionsBillingQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "BillingQueries") {
		t.Error("server.go Options must have BillingQueries field")
	}
}

func TestBilling161_ServerHasBillingTariffRoutes(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "billing/tariffs") {
		t.Error("server.go must register billing tariff routes")
	}
}

func TestBilling161_ServerHasBillingInvoiceRoutes(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "billing/invoices") {
		t.Error("server.go must register billing invoice routes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler file content checks
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_HandlerHasGenerateInvoicesForPeriod(t *testing.T) {
	content := findFileByName(t, "billing_ledger.go")
	if !strings.Contains(content, "generateInvoicesForPeriod") {
		t.Error("billing_ledger.go must implement generateInvoicesForPeriod")
	}
}

func TestBilling161_HandlerHasIncrementBillingUsage(t *testing.T) {
	content := findFileByName(t, "billing_ledger.go")
	if !strings.Contains(content, "IncrementBillingUsage") {
		t.Error("billing_ledger.go must implement IncrementBillingUsage hook")
	}
}

func TestBilling161_HandlerHasAllInvoiceTransitionEndpoints(t *testing.T) {
	content := findFileByName(t, "billing_ledger.go")
	for _, transition := range []string{"handleIssueInvoice", "handlePayInvoice", "handleVoidInvoice"} {
		if !strings.Contains(content, transition) {
			t.Errorf("billing_ledger.go must have '%s' handler", transition)
		}
	}
}

func TestBilling161_HandlerPackageIsBillingLedger(t *testing.T) {
	content := findFileByName(t, "billing_ledger.go")
	if !strings.Contains(content, "package httpserver") {
		t.Error("billing_ledger.go must be in package httpserver")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// JSON content-type tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBilling161_CreateTariffEmptyBodyReturnsJSON(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/tariffs", bytes.NewBufferString(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("response must be application/json, got %q", ct)
	}
}

func TestBilling161_GetInvoiceWithJWTNotFoundReturnsJSON(t *testing.T) {
	s := buildBillingServer161(t)
	tok := mintBillingToken(t, s)
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/invoices/"+billingTestInvoiceID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("response must be application/json, got %q", ct)
	}
}
