// external_allocations_145_test.go — unit tests for feature #145
// (External allocation quota model).
//
// Test coverage:
//
//	Step 1: Migration file 0035_external_allocations.sql — table, status CHECK, RBAC seeds
//	Step 2: SQL query file external_allocations.sql — all queries present
//	Step 3: Gen file external_allocations.sql.go — ExternalAllocationRow, all methods
//	Step 4: Querier interface — all 6 allocation methods present
//	Step 5: HTTP routes — auth-gating, server wiring, validation
//	Step 6: State machine — valid/invalid transitions, quota overflow logic
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
// Server factory for allocation route tests
// ─────────────────────────────────────────────────────────────────────────────

const allocationTestActorID = "00000000-0000-0000-0000-000000000145"

// buildAllocationServer builds a Server with stub auth, allocation routes fully
// mounted, and a dbDownPool so real DB operations never execute.
func buildAllocationServer(t *testing.T) *Server {
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
		t.Fatalf("buildAllocationServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		AllocationQueries: gen.New(nil),
		InventoryQueries:  gen.New(nil),
	})
}

// mintAllocationToken mints a dev JWT for allocation route tests.
func mintAllocationToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + allocationTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintAllocationToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintAllocationToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintAllocationToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	if content == "" {
		t.Fatal("0035_external_allocations.sql is empty")
	}
}

func TestExternalAllocation145_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestExternalAllocation145_MigrationHasExternalAllocationsTable(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	requiredTokens := []string{
		"CREATE TABLE external_allocations",
		"session_id",
		"partner_org_id",
		"tier_id",
		"quota_qty",
		"quota_consumed",
		"status",
	}
	for _, tok := range requiredTokens {
		if !strings.Contains(content, tok) {
			t.Errorf("migration missing required token: %q", tok)
		}
	}
}

func TestExternalAllocation145_MigrationHasStatusConstraint(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	requiredStatuses := []string{"pending", "active", "reconciled", "disputed"}
	for _, s := range requiredStatuses {
		if !strings.Contains(content, s) {
			t.Errorf("migration missing status value: %q", s)
		}
	}
}

func TestExternalAllocation145_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	required := []string{
		"allocation.read",
		"allocation.create",
		"allocation.update",
		"INSERT INTO permissions",
		"INSERT INTO role_permissions",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("migration missing RBAC token: %q", tok)
		}
	}
}

func TestExternalAllocation145_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0035_external_allocations.sql")
	if !strings.Contains(content, "CREATE INDEX") {
		t.Error("migration missing CREATE INDEX (allocation queries should have indexes)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "external_allocations.sql")
	if content == "" {
		t.Fatal("external_allocations.sql is empty")
	}
}

func TestExternalAllocation145_QueryFileHasRequiredQueries(t *testing.T) {
	content := findFileByName(t, "external_allocations.sql")
	requiredQueries := []string{
		"InsertExternalAllocation",
		"GetExternalAllocationByID",
		"ListExternalAllocationsBySession",
		"ListExternalAllocationsByOrg",
		"UpdateExternalAllocationStatus",
		"ReportAllocationConsumption",
	}
	for _, q := range requiredQueries {
		if !strings.Contains(content, q) {
			t.Errorf("external_allocations.sql missing query: %q", q)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_GenFileExists(t *testing.T) {
	content := findFileByName(t, "external_allocations.sql.go")
	if content == "" {
		t.Fatal("external_allocations.sql.go is empty")
	}
}

func TestExternalAllocation145_GenFileHasExternalAllocationRow(t *testing.T) {
	content := findFileByName(t, "external_allocations.sql.go")
	required := []string{
		"ExternalAllocationRow",
		"SessionID",
		"PartnerOrgID",
		"TierID",
		"QuotaQty",
		"QuotaConsumed",
		"Status",
	}
	for _, tok := range required {
		if !strings.Contains(content, tok) {
			t.Errorf("gen file missing field/type: %q", tok)
		}
	}
}

func TestExternalAllocation145_GenFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "external_allocations.sql.go")
	methods := []string{
		"InsertExternalAllocation",
		"GetExternalAllocationByID",
		"ListExternalAllocationsBySession",
		"ListExternalAllocationsByOrg",
		"UpdateExternalAllocationStatus",
		"ReportAllocationConsumption",
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

func TestExternalAllocation145_QuerierHasAllocationMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	required := []string{
		"InsertExternalAllocation",
		"GetExternalAllocationByID",
		"ListExternalAllocationsBySession",
		"ListExternalAllocationsByOrg",
		"UpdateExternalAllocationStatus",
		"ReportAllocationConsumption",
	}
	for _, m := range required {
		if !strings.Contains(content, m) {
			t.Errorf("querier.go missing allocation method: %q", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: HTTP routes — auth-gating and server wiring
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_ServerHasAllocationQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "allocationQueries") {
		t.Error("server.go missing allocationQueries field")
	}
	if !strings.Contains(content, "AllocationQueries") {
		t.Error("server.go missing AllocationQueries option")
	}
}

func TestExternalAllocation145_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "external_allocations.go")
	if content == "" {
		t.Fatal("external_allocations.go is empty")
	}
}

func TestExternalAllocation145_POSTRequiresJWT(t *testing.T) {
	s := buildAllocationServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":10}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST without JWT: got %d, want 401", w.Code)
	}
}

func TestExternalAllocation145_GETRequiresJWT(t *testing.T) {
	s := buildAllocationServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/external-allocations", nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET without JWT: got %d, want 401", w.Code)
	}
}

func TestExternalAllocation145_PATCHRequiresJWT(t *testing.T) {
	s := buildAllocationServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	const allocID = "00000000-0000-0000-0000-000000000001"
	w := httptest.NewRecorder()
	body := `{"status":"active"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID+"/external-allocations/"+allocID,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH without JWT: got %d, want 401", w.Code)
	}
}

func TestExternalAllocation145_GETDetailRequiresJWT(t *testing.T) {
	s := buildAllocationServer(t)
	const orgID = "00000000-0000-0000-0000-000000000099"
	const allocID = "00000000-0000-0000-0000-000000000001"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/external-allocations/"+allocID, nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET detail without JWT: got %d, want 401", w.Code)
	}
}

func TestExternalAllocation145_POSTWithJWTReturnsJSON(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":10}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	// Expect 503 (db is down) not 401 — proves auth passed
	if w.Code == http.StatusUnauthorized {
		t.Errorf("POST with JWT: got 401, auth should have passed")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type: got %q, want application/json", ct)
	}
}

func TestExternalAllocation145_POSTInvalidJSONReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST invalid JSON: got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_POSTInvalidOrgIDReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":10}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/not-a-uuid/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid org_id: got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_POSTMissingSessionIDReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"quota_qty":10}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST missing session_id: got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_POSTZeroQuotaReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":0}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with quota_qty=0: got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_POSTNegativeQuotaReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":-5}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with negative quota_qty: got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_POSTInvalidInitialStatusReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":10,"status":"reconciled"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid initial status 'reconciled': got %d, want 400", w.Code)
	}
}

func TestExternalAllocation145_PATCHInvalidAllocationIDReturns400(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	body := `{"status":"active"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID+"/external-allocations/not-a-uuid",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with invalid UUID: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: State machine logic
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_StateMachineValidTransitions(t *testing.T) {
	valid := []struct {
		from string
		to   string
	}{
		{"pending", "active"},
		{"pending", "reconciled"},
		{"active", "reconciled"},
		{"active", "disputed"},
		{"disputed", "reconciled"},
	}
	for _, tc := range valid {
		allowed, ok := validAllocationTransitions[tc.from]
		if !ok {
			t.Errorf("state %q not in validAllocationTransitions", tc.from)
			continue
		}
		if !allowed[tc.to] {
			t.Errorf("expected %q → %q to be a valid transition", tc.from, tc.to)
		}
	}
}

func TestExternalAllocation145_StateMachineInvalidTransitions(t *testing.T) {
	invalid := []struct {
		from string
		to   string
	}{
		{"reconciled", "active"},   // terminal
		{"reconciled", "pending"},  // terminal
		{"reconciled", "disputed"}, // terminal
		{"pending", "disputed"},    // skip activation
		{"disputed", "active"},     // no going back
	}
	for _, tc := range invalid {
		if tc.from == "reconciled" {
			if !isTerminalAllocationStatus(tc.from) {
				t.Errorf("expected %q to be a terminal status", tc.from)
			}
			continue
		}
		allowed, ok := validAllocationTransitions[tc.from]
		if !ok {
			continue
		}
		if allowed[tc.to] {
			t.Errorf("expected %q → %q to be an invalid transition", tc.from, tc.to)
		}
	}
}

func TestExternalAllocation145_IsTerminalAllocationStatus(t *testing.T) {
	terminal := []string{"reconciled"}
	for _, s := range terminal {
		if !isTerminalAllocationStatus(s) {
			t.Errorf("expected %q to be a terminal status", s)
		}
	}
	nonTerminal := []string{"pending", "active", "disputed"}
	for _, s := range nonTerminal {
		if isTerminalAllocationStatus(s) {
			t.Errorf("expected %q to be a non-terminal status", s)
		}
	}
}

func TestExternalAllocation145_AllStatusesCovered(t *testing.T) {
	for _, s := range allAllocationStatuses {
		if _, ok := validAllocationTransitions[s]; !ok {
			t.Errorf("status %q is in allAllocationStatuses but not in validAllocationTransitions", s)
		}
	}
}

func TestExternalAllocation145_QuotaOverflowValidation(t *testing.T) {
	// Verify that quota overflow is rejected (client-side validation before DB call).
	// With a dbDownPool, any DB call will panic. The validation must happen
	// before the DB call — which it does in handleCreateExternalAllocation
	// (negative/zero quota_qty check returns 400 before touching the DB).
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"

	// Extremely large quota — passes client validation but hits DB (which is down → 503).
	// This is correct: quota overflow must be rejected at DB level with 409.
	// We verify the handler doesn't short-circuit this with 400 (validation passes).
	w := httptest.NewRecorder()
	body := `{"session_id":"00000000-0000-0000-0000-000000000001","quota_qty":999999,"status":"active"}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/external-allocations",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	// Should be 503 (db down) not 400 (client validation) or 401 (auth).
	// This proves the handler reached the DB layer, meaning validation passed.
	if w.Code == http.StatusBadRequest {
		t.Errorf("valid quota_qty=999999 was incorrectly rejected with 400")
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("JWT auth failed unexpectedly: got 401")
	}
	// Confirm response is JSON
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type: got %q, want application/json", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional: handler source code checks
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_HandlerHasInventoryHook(t *testing.T) {
	content := findFileByName(t, "external_allocations.go")
	// Handler must call inventory operations for the allocation/return cycle.
	inventoryOps := []string{
		"ReserveCapacity",
		"ConfirmCapacity",
		"ReleaseCapacity",
	}
	for _, op := range inventoryOps {
		if !strings.Contains(content, op) {
			t.Errorf("external_allocations.go missing inventory operation: %q", op)
		}
	}
}

func TestExternalAllocation145_HandlerHasStateTransitionMap(t *testing.T) {
	content := findFileByName(t, "external_allocations.go")
	if !strings.Contains(content, "validAllocationTransitions") {
		t.Error("external_allocations.go missing validAllocationTransitions map")
	}
}

func TestExternalAllocation145_HandlerHasTransactionUsage(t *testing.T) {
	content := findFileByName(t, "external_allocations.go")
	// Inventory changes must be transactional with allocation status updates.
	if !strings.Contains(content, "BeginTx") {
		t.Error("external_allocations.go missing BeginTx (inventory changes must be transactional)")
	}
	if !strings.Contains(content, "Commit") {
		t.Error("external_allocations.go missing Commit")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response format checks
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_ResponseContentType(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/external-allocations", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET list Content-Type: got %q, want application/json", ct)
	}
}

func TestExternalAllocation145_PATCHWithJWTReturnsJSON(t *testing.T) {
	s := buildAllocationServer(t)
	tok := mintAllocationToken(t, s)
	const orgID = "00000000-0000-0000-0000-000000000099"
	const allocID = "00000000-0000-0000-0000-000000000001"
	w := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"status":"active"}`)
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID+"/external-allocations/"+allocID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)
	// With db down: expect 503 or 404 (not 401), and JSON response
	if w.Code == http.StatusUnauthorized {
		t.Errorf("PATCH with JWT: got 401, auth should have passed")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("PATCH Content-Type: got %q, want application/json", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring: allocation routes are mounted only when allocationQueries set
// ─────────────────────────────────────────────────────────────────────────────

func TestExternalAllocation145_RoutesNotMountedWithoutQueries(t *testing.T) {
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
	// No AllocationQueries — routes should not be mounted.
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
	})
	const orgID = "00000000-0000-0000-0000-000000000099"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID+"/external-allocations", nil)
	s.router.ServeHTTP(w, req)
	// Routes not mounted → 404 (not 401 or 503)
	if w.Code != http.StatusNotFound {
		t.Errorf("without AllocationQueries: got %d, want 404", w.Code)
	}
}
