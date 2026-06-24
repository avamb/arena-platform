// complimentary_148_test.go — unit tests for feature #148
// (Complimentary ticket issuance flow).
//
// Test coverage:
//   Step 1: Migration file 0036_complimentary_issuances.sql — table, status CHECK, ticket ALTER, RBAC seeds
//   Step 2: SQL query file complimentary_issuances.sql — all queries present
//   Step 3: Gen file complimentary_issuances.sql.go — all types and methods
//   Step 4: Querier interface — all 7 complimentary methods present
//   Step 5: HTTP routes — auth-gating, server wiring, request validation
//   Step 6: Idempotency — batch_id replay, server wiring
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
// Server factory for complimentary route tests
// ─────────────────────────────────────────────────────────────────────────────

const complimentaryTestActorID = "00000000-0000-0000-0000-000000000148"

// buildComplimentaryServer builds a Server with stub auth, complimentary routes
// fully mounted, and a dbDownPool so real DB operations never execute.
func buildComplimentaryServer(t *testing.T) *Server {
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
		t.Fatalf("buildComplimentaryServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		ComplimentaryQueries: gen.New(nil),
		InventoryQueries:     gen.New(nil),
	})
}

// mintComplimentaryToken mints a dev JWT for complimentary route tests.
func mintComplimentaryToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + complimentaryTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintComplimentaryToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintComplimentaryToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintComplimentaryToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	if content == "" {
		t.Fatal("0036_complimentary_issuances.sql is empty")
	}
}

func TestComplimentary148_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestComplimentary148_MigrationHasComplimentaryIssuancesTable(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	requiredTokens := []string{
		"CREATE TABLE complimentary_issuances",
		"org_id",
		"session_id",
		"tier_id",
		"qty",
		"recipients",
		"batch_id",
		"status",
	}
	for _, tok := range requiredTokens {
		if !strings.Contains(content, tok) {
			t.Errorf("migration missing required token: %q", tok)
		}
	}
}

func TestComplimentary148_MigrationHasStatusConstraint(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	requiredStatuses := []string{"pending", "issued", "failed"}
	for _, s := range requiredStatuses {
		if !strings.Contains(content, s) {
			t.Errorf("migration missing status value: %q", s)
		}
	}
}

func TestComplimentary148_MigrationHasIdempotencyIndex(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	if !strings.Contains(content, "CREATE UNIQUE INDEX") {
		t.Error("migration missing CREATE UNIQUE INDEX (batch_id idempotency index required)")
	}
	if !strings.Contains(content, "batch_id") {
		t.Error("migration idempotency index must reference batch_id column")
	}
}

func TestComplimentary148_MigrationAltersTicketsTable(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	// Migration must extend the tickets table with complimentary_issuance_id FK.
	required := []string{
		"ALTER TABLE tickets",
		"complimentary_issuance_id",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("migration missing tickets ALTER token: %q", tok)
		}
	}
}

func TestComplimentary148_MigrationHasTicketSourceConstraint(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	// A CHECK constraint must ensure exactly one of checkout_session_id or
	// complimentary_issuance_id is set on a ticket row.
	if !strings.Contains(content, "tickets_source_check") {
		t.Error("migration missing tickets_source_check constraint name")
	}
}

func TestComplimentary148_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	required := []string{
		"complimentary.issue",
		"complimentary.read",
		"INSERT INTO permissions",
		"INSERT INTO role_permissions",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("migration missing RBAC token: %q", tok)
		}
	}
}

func TestComplimentary148_MigrationDownSectionExists(t *testing.T) {
	content := findFileByName(t, "0036_complimentary_issuances.sql")
	// Down migration must reverse all changes.
	required := []string{
		"DROP TABLE IF EXISTS complimentary_issuances",
		"DROP CONSTRAINT IF EXISTS tickets_source_check",
		"DROP COLUMN IF EXISTS complimentary_issuance_id",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("migration Down section missing: %q", tok)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	if content == "" {
		t.Fatal("complimentary_issuances.sql is empty")
	}
}

func TestComplimentary148_QueryFileHasRequiredQueries(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	requiredQueries := []string{
		"InsertComplimentaryIssuance",
		"GetComplimentaryIssuanceByBatchID",
		"GetComplimentaryIssuanceByID",
		"ListComplimentaryIssuancesByOrg",
		"UpdateComplimentaryIssuanceStatus",
		"InsertComplimentaryTicket",
		"ListTicketsByComplimentaryIssuance",
	}
	for _, q := range requiredQueries {
		if !strings.Contains(content, q) {
			t.Errorf("complimentary_issuances.sql missing query: %q", q)
		}
	}
}

func TestComplimentary148_QueryFileHasBatchIDIdempotency(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	// GetComplimentaryIssuanceByBatchID must filter on BOTH org_id AND batch_id.
	if !strings.Contains(content, "org_id = $1") {
		t.Error("idempotency query must filter by org_id")
	}
	if !strings.Contains(content, "batch_id = $2") {
		t.Error("idempotency query must filter by batch_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_GenFileExists(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	if content == "" {
		t.Fatal("complimentary_issuances.sql.go is empty")
	}
}

func TestComplimentary148_GenFileHasComplimentaryIssuanceRow(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	required := []string{
		"ComplimentaryIssuanceRow",
		"OrgID",
		"SessionID",
		"TierID",
		"Qty",
		"Recipients",
		"BatchID",
		"Status",
		"IssuedBy",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("gen file missing field/type: %q", tok)
		}
	}
}

func TestComplimentary148_GenFileHasComplimentaryTicketRow(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	required := []string{
		"ComplimentaryTicketRow",
		"ComplimentaryIssuanceID",
		"HolderEmail",
		"IssuedAt",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("gen file missing ComplimentaryTicketRow field: %q", tok)
		}
	}
}

func TestComplimentary148_GenFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	methods := []string{
		"InsertComplimentaryIssuance",
		"GetComplimentaryIssuanceByBatchID",
		"GetComplimentaryIssuanceByID",
		"ListComplimentaryIssuancesByOrg",
		"UpdateComplimentaryIssuanceStatus",
		"InsertComplimentaryTicket",
		"ListTicketsByComplimentaryIssuance",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("gen file missing method: %q", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_QuerierHasComplimentaryMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	required := []string{
		"InsertComplimentaryIssuance",
		"GetComplimentaryIssuanceByBatchID",
		"GetComplimentaryIssuanceByID",
		"ListComplimentaryIssuancesByOrg",
		"UpdateComplimentaryIssuanceStatus",
		"InsertComplimentaryTicket",
		"ListTicketsByComplimentaryIssuance",
	}
	for _, m := range required {
		if !strings.Contains(content, m) {
			t.Errorf("querier.go missing complimentary method: %q", m)
		}
	}
}

func TestComplimentary148_QuerierHasComplimentaryRowTypes(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "ComplimentaryIssuanceRow") {
		t.Error("querier.go missing ComplimentaryIssuanceRow return type")
	}
	if !strings.Contains(content, "ComplimentaryTicketRow") {
		t.Error("querier.go missing ComplimentaryTicketRow return type")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: HTTP routes — auth-gating and server wiring
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_ServerHasComplimentaryQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "complimentaryQueries") {
		t.Error("server.go missing complimentaryQueries field")
	}
	if !strings.Contains(content, "ComplimentaryQueries") {
		t.Error("server.go missing ComplimentaryQueries option")
	}
}

func TestComplimentary148_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if content == "" {
		t.Fatal("complimentary.go is empty")
	}
}

func TestComplimentary148_POSTRequiresJWT(t *testing.T) {
	s := buildComplimentaryServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":5,"batch_id":"batch-001"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST without JWT: got %d, want 401", w.Code)
	}
}

func TestComplimentary148_GETRequiresJWT(t *testing.T) {
	s := buildComplimentaryServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/complimentary", nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET without JWT: got %d, want 401", w.Code)
	}
}

func TestComplimentary148_GETDetailRequiresJWT(t *testing.T) {
	s := buildComplimentaryServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	const issuanceID = "00000000-0000-0000-0000-000000000001"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/complimentary/"+issuanceID, nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET detail without JWT: got %d, want 401", w.Code)
	}
}

func TestComplimentary148_POSTWithJWTPassesAuthReturnsJSON(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":5,"batch_id":"batch-001"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	// Expect not 401 (auth passed) — DB is down so 503 or similar
	if w.Code == http.StatusUnauthorized {
		t.Errorf("POST with JWT: got 401, auth should have passed")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type: got %q, want application/json", ct)
	}
}

func TestComplimentary148_GETWithJWTPassesAuth(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/complimentary", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET with JWT: got 401, auth should have passed")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET list Content-Type: got %q, want application/json", ct)
	}
}

func TestComplimentary148_RoutesNotMountedWithoutQueries(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	// No ComplimentaryQueries — routes should not be mounted.
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
	})
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/complimentary", nil)
	s.router.ServeHTTP(w, req)
	// Routes not mounted → 404 (not 401 or 503)
	if w.Code != http.StatusNotFound {
		t.Errorf("without ComplimentaryQueries: got %d, want 404", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request validation — Step 5 extended
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_POSTInvalidJSONReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST invalid JSON: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTInvalidOrgIDReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":5,"batch_id":"b1"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/not-a-uuid/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST invalid org_id: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTMissingSessionIDReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"qty":5,"batch_id":"batch-001"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST missing session_id: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTZeroQtyReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":0,"batch_id":"batch-001"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST qty=0: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTNegativeQtyReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":-3,"batch_id":"batch-001"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST negative qty: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTMissingBatchIDReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":5}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST missing batch_id: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_POSTInvalidTierIDReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","qty":5,"batch_id":"b1","tier_id":"not-a-uuid"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST invalid tier_id: got %d, want 400", w.Code)
	}
}

func TestComplimentary148_GETDetailInvalidIDReturns400(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/complimentary/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET detail invalid UUID: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Idempotency and inventory integration checks
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_HandlerHasIdempotencyCheck(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Handler must perform idempotency check via GetComplimentaryIssuanceByBatchID.
	if !strings.Contains(content, "GetComplimentaryIssuanceByBatchID") {
		t.Error("complimentary.go missing idempotency check (GetComplimentaryIssuanceByBatchID)")
	}
	if !strings.Contains(content, "idempotent_replay") {
		t.Error("complimentary.go missing idempotent_replay field in response")
	}
}

func TestComplimentary148_HandlerHasInventoryIntegration(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Handler must call ReserveCapacity + ConfirmCapacity for inventory decrement.
	if !strings.Contains(content, "ReserveCapacity") {
		t.Error("complimentary.go missing ReserveCapacity (inventory decrement step 4)")
	}
	if !strings.Contains(content, "ConfirmCapacity") {
		t.Error("complimentary.go missing ConfirmCapacity (inventory confirm step 5)")
	}
}

func TestComplimentary148_HandlerHasTransactionUsage(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Inventory + ticket creation must be transactional.
	if !strings.Contains(content, "BeginTx") {
		t.Error("complimentary.go missing BeginTx (issuance must be transactional)")
	}
	if !strings.Contains(content, "Commit") {
		t.Error("complimentary.go missing Commit")
	}
}

func TestComplimentary148_HandlerHasTicketCreationHook(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Handler must create tickets via InsertComplimentaryTicket.
	if !strings.Contains(content, "InsertComplimentaryTicket") {
		t.Error("complimentary.go missing InsertComplimentaryTicket (ticket creation hook)")
	}
}

func TestComplimentary148_HandlerHasStatusTransitions(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Issuance should transition pending → issued on success.
	if !strings.Contains(content, "\"issued\"") {
		t.Error("complimentary.go missing 'issued' status transition")
	}
	if !strings.Contains(content, "UpdateComplimentaryIssuanceStatus") {
		t.Error("complimentary.go missing UpdateComplimentaryIssuanceStatus call")
	}
}

func TestComplimentary148_HandlerHasCapacityOverflowHandling(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Over-capacity must be surfaced as a conflict (409).
	if !strings.Contains(content, "capacity_overflow") {
		t.Error("complimentary.go missing capacity_overflow error code")
	}
	if !strings.Contains(content, "StatusConflict") {
		t.Error("complimentary.go missing http.StatusConflict for over-capacity")
	}
}

func TestComplimentary148_POSTValidRequestPassesValidation(t *testing.T) {
	s := buildComplimentaryServer(t)
	tok := mintComplimentaryToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"

	// Valid request — should pass all validation and hit the DB layer (which is down → 503).
	w := httptest.NewRecorder()
	body := bytes.NewBufferString(`{
		"session_id": "00000000-0000-0000-0000-000000000001",
		"qty": 10,
		"batch_id": "my-unique-batch-id-2024",
		"recipients": ["alice@example.com", "bob@example.com"]
	}`)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/complimentary", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	// Should NOT be 400 (validation passed) or 401 (auth passed).
	if w.Code == http.StatusBadRequest {
		t.Errorf("valid request incorrectly rejected with 400; body: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("JWT auth failed unexpectedly: got 401")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type: got %q, want application/json", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional: compile-time type checks via gen package
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentary148_ComplimentaryIssuanceRowCompiles(t *testing.T) {
	// Ensure ComplimentaryIssuanceRow is accessible and has the expected fields.
	var row gen.ComplimentaryIssuanceRow
	row.BatchID = "test-batch"
	row.Status = "issued"
	row.Qty = 10
	if row.BatchID == "" || row.Status == "" {
		t.Error("ComplimentaryIssuanceRow fields not properly typed")
	}
}

func TestComplimentary148_ComplimentaryTicketRowCompiles(t *testing.T) {
	// Ensure ComplimentaryTicketRow is accessible and has the expected fields.
	var row gen.ComplimentaryTicketRow
	row.Status = "active"
	if row.Status == "" {
		t.Error("ComplimentaryTicketRow Status field not properly typed")
	}
}
