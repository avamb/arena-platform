// complimentary_revocation_150_test.go — unit tests for feature #150
// (Complimentary revocation flow).
//
// Test coverage:
//
//	Step 1: Migration file 0038_complimentary_revocation.sql — extended status CHECKs, RBAC seeds
//	Step 2: SQL queries in complimentary_issuances.sql — HasScannedTicketsForIssuance, RevokeComplimentaryTickets
//	Step 3: SQL query in inventory_ledger.sql — RestoreSoldCapacity
//	Step 4: Gen file complimentary_issuances.sql.go — HasScannedTicketsForIssuance, RevokeComplimentaryTickets
//	Step 5: Gen file inventory_ledger.sql.go — RestoreSoldCapacity
//	Step 6: Querier interface — three new methods
//	Step 7: HTTP route — POST /v1/complimentary/{id}/revoke registered
//	Step 8: Handler guards — 503 when deps missing, 401 when unauthenticated
//	Step 9: Handler validates UUID format
//	Step 10: Handler source file — handleRevokeComplimentaryIssuance present
//	Step 11: Nil guard behaviour — all dependency guards present
//	Step 12: manual_review response shape described in handler source
//	Step 13: Inventory restore — RestoreSoldCapacity used in handler
//	Step 14: Audit log — structured log event present
//	Step 15: Revoke ticket bulk — RevokeComplimentaryTickets used in handler
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
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for revocation route tests
// ─────────────────────────────────────────────────────────────────────────────

const revocationTestActorID = "00000000-0000-0000-0000-000000000150"

// buildRevocationServer builds a Server with stub auth, complimentary routes
// (including revoke) fully mounted, and a dbDownPool so real DB ops never execute.
func buildRevocationServer(t *testing.T) *Server {
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
		t.Fatalf("buildRevocationServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		ComplimentaryQueries: gen.New(nil),
		InventoryQueries:     gen.New(nil),
		BarcodeQueries:       gen.New(nil),
		CredentialQueries:    gen.New(nil),
	})
}

// mintRevocationToken mints a dev JWT for revocation route tests.
func mintRevocationToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + revocationTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintRevocationToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mintRevocationToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintRevocationToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0038_complimentary_revocation.sql")
	if content == "" {
		t.Fatal("0038_complimentary_revocation.sql is empty")
	}
}

func TestComplimentaryRevocation150_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0038_complimentary_revocation.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration must contain '-- +goose Up'")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must contain '-- +goose Down'")
	}
}

func TestComplimentaryRevocation150_MigrationExtendsIssuanceStatus(t *testing.T) {
	content := findFileByName(t, "0038_complimentary_revocation.sql")
	if !strings.Contains(content, "revoked") {
		t.Error("migration must add 'revoked' to complimentary_issuances status domain")
	}
	if !strings.Contains(content, "manual_review") {
		t.Error("migration must add 'manual_review' to complimentary_issuances status domain")
	}
}

func TestComplimentaryRevocation150_MigrationExtendsTicketsStatus(t *testing.T) {
	content := findFileByName(t, "0038_complimentary_revocation.sql")
	// Should add 'revoked' to tickets status check
	if !strings.Contains(content, "tickets_status_check") {
		t.Error("migration must update tickets_status_check constraint")
	}
}

func TestComplimentaryRevocation150_MigrationHasRBACPermission(t *testing.T) {
	content := findFileByName(t, "0038_complimentary_revocation.sql")
	if !strings.Contains(content, "complimentary.revoke") {
		t.Error("migration must seed 'complimentary.revoke' RBAC permission")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: complimentary_issuances.sql — new queries
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_SQLFileHasScannedCheck(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	if !strings.Contains(content, "HasScannedTicketsForIssuance") {
		t.Error("complimentary_issuances.sql must contain HasScannedTicketsForIssuance query")
	}
}

func TestComplimentaryRevocation150_SQLFileHasRevokeTickets(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	if !strings.Contains(content, "RevokeComplimentaryTickets") {
		t.Error("complimentary_issuances.sql must contain RevokeComplimentaryTickets query")
	}
}

func TestComplimentaryRevocation150_ScannedCheckJoinsBarcodes(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	if !strings.Contains(content, "barcodes") {
		t.Error("HasScannedTicketsForIssuance must JOIN the barcodes table")
	}
}

func TestComplimentaryRevocation150_RevokeTicketsUpdateStatus(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql")
	if !strings.Contains(content, "'revoked'") {
		t.Error("RevokeComplimentaryTickets must set status = 'revoked'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: inventory_ledger.sql — RestoreSoldCapacity query
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_InventoryLedgerSQLHasRestore(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "RestoreSoldCapacity") {
		t.Error("inventory_ledger.sql must contain RestoreSoldCapacity query")
	}
}

func TestComplimentaryRevocation150_RestoreDecrementsSold(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "capacity_sold = il.capacity_sold - $3::integer") {
		t.Error("RestoreSoldCapacity must decrement capacity_sold")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Gen file complimentary_issuances.sql.go
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_GenFileHasScannedFunc(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	if !strings.Contains(content, "func (q *Queries) HasScannedTicketsForIssuance") {
		t.Error("complimentary_issuances.sql.go must contain HasScannedTicketsForIssuance function")
	}
}

func TestComplimentaryRevocation150_GenFileHasRevokeTicketsFunc(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	if !strings.Contains(content, "func (q *Queries) RevokeComplimentaryTickets") {
		t.Error("complimentary_issuances.sql.go must contain RevokeComplimentaryTickets function")
	}
}

func TestComplimentaryRevocation150_GenScannedFuncReturnsBool(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	if !strings.Contains(content, "HasScannedTicketsForIssuance") {
		t.Skip("HasScannedTicketsForIssuance not yet defined")
	}
	if !strings.Contains(content, "(bool, error)") {
		t.Error("HasScannedTicketsForIssuance must return (bool, error)")
	}
}

func TestComplimentaryRevocation150_GenRevokeTicketsReturnsSlice(t *testing.T) {
	content := findFileByName(t, "complimentary_issuances.sql.go")
	if !strings.Contains(content, "RevokeComplimentaryTickets") {
		t.Skip("RevokeComplimentaryTickets not yet defined")
	}
	if !strings.Contains(content, "([]ComplimentaryTicketRow, error)") {
		t.Error("RevokeComplimentaryTickets must return ([]ComplimentaryTicketRow, error)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Gen file inventory_ledger.sql.go — RestoreSoldCapacity
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_InventoryGenHasRestoreFunc(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "func (q *Queries) RestoreSoldCapacity") {
		t.Error("inventory_ledger.sql.go must contain RestoreSoldCapacity function")
	}
}

func TestComplimentaryRevocation150_InventoryGenRestoreSignature(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "RestoreSoldCapacity") {
		t.Skip("RestoreSoldCapacity not yet defined")
	}
	if !strings.Contains(content, "(InventoryLedgerRow, error)") {
		t.Error("RestoreSoldCapacity must return (InventoryLedgerRow, error)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_QuerierHasScannedMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "HasScannedTicketsForIssuance") {
		t.Error("querier.go Querier interface must include HasScannedTicketsForIssuance")
	}
}

func TestComplimentaryRevocation150_QuerierHasRevokeTicketsMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "RevokeComplimentaryTickets") {
		t.Error("querier.go Querier interface must include RevokeComplimentaryTickets")
	}
}

func TestComplimentaryRevocation150_QuerierHasRestoreMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "RestoreSoldCapacity") {
		t.Error("querier.go Querier interface must include RestoreSoldCapacity")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Route registration — POST /v1/complimentary/{id}/revoke
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_RouteRegistered(t *testing.T) {
	s := buildRevocationServer(t)
	tok := mintRevocationToken(t, s)

	// Using a non-existent UUID should reach the handler (503 because pool is dbDownPool
	// and it returns error on BeginTx — but the handler checks complimentaryQueries first,
	// which panics on nil DB, so we get the DB-unavailable guard path. The point is the
	// route exists and is reachable with auth).
	id := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodPost, "/v1/complimentary/"+id+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route exists (not 404) and is auth-gated (not 405).
	if w.Code == http.StatusNotFound {
		t.Errorf("route POST /v1/complimentary/{id}/revoke not found (got 404)")
	}
	if w.Code == http.StatusMethodNotAllowed {
		t.Errorf("method POST not allowed on /v1/complimentary/{id}/revoke")
	}
}

func TestComplimentaryRevocation150_RouteRequiresAuth(t *testing.T) {
	s := buildRevocationServer(t)

	id := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodPost, "/v1/complimentary/"+id+"/revoke", nil)
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request should return 401, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Handler guards — 503 when deps missing
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerNilGuard_NilPool(t *testing.T) {
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
	// No Pool, no ComplimentaryQueries — dep check should trigger.
	s := New(Options{
		Config:               cfg,
		Auth:                 stub,
		ComplimentaryQueries: gen.New(nil),
		// Pool intentionally omitted.
	})

	// Mint token.
	w0 := httptest.NewRecorder()
	body := `{"actor_id":"` + revocationTestActorID + `","roles":["admin"]}`
	req0 := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req0.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w0, req0)
	var resp map[string]string
	_ = json.Unmarshal(w0.Body.Bytes(), &resp)
	tok := resp["token"]

	id := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodPost, "/v1/complimentary/"+id+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when pool is nil, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Handler validates UUID format
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerRejectsBadUUID(t *testing.T) {
	s := buildRevocationServer(t)
	tok := mintRevocationToken(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/complimentary/not-a-uuid/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid UUID should return 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Handler source file — handleRevokeComplimentaryIssuance present
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerFunctionExists(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "handleRevokeComplimentaryIssuance") {
		t.Error("complimentary.go must define handleRevokeComplimentaryIssuance")
	}
}

func TestComplimentaryRevocation150_HandlerDocumentedRoute(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "/complimentary/{id}/revoke") {
		t.Error("complimentary.go package doc must mention POST /v1/complimentary/{id}/revoke")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: Nil guards in handler source
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerGuardsComplimentaryNil(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "s.complimentaryQueries == nil") {
		t.Error("handleRevokeComplimentaryIssuance must check s.complimentaryQueries == nil")
	}
}

func TestComplimentaryRevocation150_HandlerGuardsPoolNil(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "s.pool == nil") {
		t.Error("handleRevokeComplimentaryIssuance must check s.pool == nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: manual_review response shape in handler
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerHasManualReview(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "manual_review") {
		t.Error("complimentary.go must handle manual_review branch for scanned tickets")
	}
}

func TestComplimentaryRevocation150_HandlerReturns409ForScanned(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "StatusConflict") {
		t.Error("complimentary.go must return http.StatusConflict (409) for scan-blocked revocation")
	}
}

func TestComplimentaryRevocation150_HandlerManualReviewUsesHasScanned(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "HasScannedTicketsForIssuance") {
		t.Error("handleRevokeComplimentaryIssuance must call HasScannedTicketsForIssuance")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: Inventory restore — RestoreSoldCapacity used in handler
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerRestoresInventory(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "RestoreSoldCapacity") {
		t.Error("handleRevokeComplimentaryIssuance must call RestoreSoldCapacity to restore inventory")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: Audit log — structured event
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerEmitsAuditLog(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "complimentary.revoked") {
		t.Error("handleRevokeComplimentaryIssuance must emit a structured audit log with event='complimentary.revoked'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: Revoke tickets bulk — RevokeComplimentaryTickets used
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerBulkRevokesTickets(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "RevokeComplimentaryTickets") {
		t.Error("handleRevokeComplimentaryIssuance must call RevokeComplimentaryTickets")
	}
}

func TestComplimentaryRevocation150_HandlerRevokesBarcodesIfAvailable(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "s.barcodeQueries != nil") {
		t.Error("handleRevokeComplimentaryIssuance must guard barcode revocation with s.barcodeQueries != nil")
	}
}

func TestComplimentaryRevocation150_HandlerRevokesCredentialsIfAvailable(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "s.credentialQueries != nil") {
		t.Error("handleRevokeComplimentaryIssuance must guard credential revocation with s.credentialQueries != nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface check
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_QueriesImplementsQuerier(_ *testing.T) {
	// This is a compile-time check: if *gen.Queries doesn't satisfy gen.Querier,
	// the test file will fail to compile.
	var _ gen.Querier = (*gen.Queries)(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Server.go route registration check
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_ServerGoHasRevokeRoute(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "handleRevokeComplimentaryIssuance") {
		t.Error("server.go must register handleRevokeComplimentaryIssuance route")
	}
	if !strings.Contains(content, `"/complimentary/{id}/revoke"`) {
		t.Error(`server.go must register route "/complimentary/{id}/revoke"`)
	}
}

func TestComplimentaryRevocation150_ServerGoCommentDocumentsRevoke(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "complimentary.revoke") {
		t.Error("server.go route block must document the complimentary.revoke permission")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Double-revoke guard
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerGuardsDoubleRevoke(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "already_revoked") {
		t.Error("handleRevokeComplimentaryIssuance must return error code 'complimentary.already_revoked' for double-revoke")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Transaction usage
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryRevocation150_HandlerUsesTransaction(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "s.pool.BeginTx") {
		t.Error("handleRevokeComplimentaryIssuance must use s.pool.BeginTx for atomic revocation")
	}
}

func TestComplimentaryRevocation150_HandlerCommitsTransaction(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "tx.Commit") {
		t.Error("handleRevokeComplimentaryIssuance must commit the transaction after revocation")
	}
}
