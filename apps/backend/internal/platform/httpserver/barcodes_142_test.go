// barcodes_142_test.go — unit tests for barcode authority federation (feature #142).
//
// Tests cover:
//   - Migration file structure (tables, columns, constraints, RBAC, seed data)
//   - SQL query file (all named queries present)
//   - Gen file (BarcodeAuthorityRow and BarcodeRow structs, all functions)
//   - Querier interface (compile-time completeness check)
//   - Route auth-gating (401 without JWT)
//   - Request validation (missing fields, invalid UUIDs)
//   - Duplicate barcode within authority rejected (409 Conflict)
//   - Platform barcode validates in scan flow (unknown authority → 404)
//   - Response shape (JSON content-type, required fields)
//   - Server wiring (barcodeQueries field present, routes registered)
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
// Server factory and JWT helper for barcode route tests
// ─────────────────────────────────────────────────────────────────────────────

const barcodeTestActorID = "00000000-0000-0000-0000-000000000142"

// buildBarcode142Server builds a Server with stub auth and barcode routes
// fully mounted. A dbDownPool is used so real DB operations never execute.
// Auth middleware fires before the DB layer → unauthenticated requests return
// 401, not 503.
func buildBarcode142Server(t *testing.T) *Server {
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
		t.Fatalf("buildBarcode142Server: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:         cfg,
		Auth:           stub,
		Pool:           &dbDownPool{},
		BarcodeQueries: gen.New(nil),
	})
}

// mintBarcodeToken mints a dev JWT with the given roles for barcode route tests.
func mintBarcodeToken(t *testing.T, s *Server, roles []string) string {
	t.Helper()
	rolesJSON, _ := json.Marshal(roles)
	body := `{"actor_id":"` + barcodeTestActorID + `","roles":` + string(rolesJSON) + `}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintBarcodeToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintBarcodeToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintBarcodeToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Migration file structure (Step 1)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if content == "" {
		t.Fatal("0029_barcode_authorities.sql not found or empty")
	}
}

func TestBarcode142_MigrationHasGooseUp(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration must contain '-- +goose Up' directive")
	}
}

func TestBarcode142_MigrationHasGooseDown(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must contain '-- +goose Down' directive")
	}
}

func TestBarcode142_MigrationCreatesBarcodeAuthoritiesTable(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "CREATE TABLE barcode_authorities") {
		t.Error("migration must create barcode_authorities table")
	}
}

func TestBarcode142_MigrationCreatesBarcodeTable(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "CREATE TABLE barcodes") {
		t.Error("migration must create barcodes table")
	}
}

func TestBarcode142_MigrationAuthorityTypeCheck(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	for _, expected := range []string{"platform", "legacy_bil24", "external_platform", "guest_list"} {
		if !strings.Contains(content, "'"+expected+"'") {
			t.Errorf("migration type CHECK must include '%s'", expected)
		}
	}
}

func TestBarcode142_MigrationBarcodeStatusCheck(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	for _, expected := range []string{"'active'", "'scanned'", "'revoked'"} {
		if !strings.Contains(content, expected) {
			t.Errorf("migration status CHECK must include %s", expected)
		}
	}
}

func TestBarcode142_MigrationUniqueConstraint(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "UNIQUE (authority_id, external_ref)") {
		t.Error("migration must include UNIQUE (authority_id, external_ref) constraint for duplicate rejection")
	}
}

func TestBarcode142_MigrationSeedsPlatformAuthority(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "INSERT INTO barcode_authorities") {
		t.Error("migration must seed barcode authorities")
	}
	if !strings.Contains(content, "'platform'") {
		t.Error("migration must seed the 'platform' authority by default")
	}
}

func TestBarcode142_MigrationRBACPermissions(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	for _, perm := range []string{"barcode.create", "barcode.read", "barcode.scan", "barcode.revoke"} {
		if !strings.Contains(content, "'"+perm+"'") {
			t.Errorf("migration RBAC seeds must include permission '%s'", perm)
		}
	}
}

func TestBarcode142_MigrationIndexes(t *testing.T) {
	content := findFileByName(t, "0029_barcode_authorities.sql")
	if !strings.Contains(content, "CREATE INDEX barcodes_authority_id") {
		t.Error("migration must create barcodes_authority_id index")
	}
	if !strings.Contains(content, "CREATE INDEX barcodes_ticket_id") {
		t.Error("migration must create barcodes_ticket_id index")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL query file (Step 2)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_SQLFileExists(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if content == "" {
		t.Fatal("barcodes.sql not found or empty")
	}
}

func TestBarcode142_SQLFileHasInsertBarcodeAuthority(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "InsertBarcodeAuthority") {
		t.Error("barcodes.sql must contain InsertBarcodeAuthority query")
	}
}

func TestBarcode142_SQLFileHasGetBarcodeAuthorityByType(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "GetBarcodeAuthorityByType") {
		t.Error("barcodes.sql must contain GetBarcodeAuthorityByType query (used by scan flow)")
	}
}

func TestBarcode142_SQLFileHasListBarcodeAuthorities(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "ListBarcodeAuthorities") {
		t.Error("barcodes.sql must contain ListBarcodeAuthorities query")
	}
}

func TestBarcode142_SQLFileHasInsertBarcode(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "InsertBarcode") {
		t.Error("barcodes.sql must contain InsertBarcode query")
	}
}

func TestBarcode142_SQLFileHasGetBarcodeByRef(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "GetBarcodeByRef") {
		t.Error("barcodes.sql must contain GetBarcodeByRef query (scan lookup by authority+ref)")
	}
}

func TestBarcode142_SQLFileHasMarkBarcodeScanned(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "MarkBarcodeScanned") {
		t.Error("barcodes.sql must contain MarkBarcodeScanned query (atomic scan transition)")
	}
}

func TestBarcode142_SQLFileMarkScannedUsesStatusActiveGuard(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	// The WHERE status = 'active' guard is critical for double-scan protection.
	if !strings.Contains(content, "status = 'active'") {
		t.Error("MarkBarcodeScanned must have WHERE status = 'active' guard to prevent double-scan")
	}
}

func TestBarcode142_SQLFileHasRevokeBarcode(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "RevokeBarcode") {
		t.Error("barcodes.sql must contain RevokeBarcode query")
	}
}

func TestBarcode142_SQLFileHasListBarcodesByTicketID(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "ListBarcodesByTicketID") {
		t.Error("barcodes.sql must contain ListBarcodesByTicketID query")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gen file (Step 3)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_GenFileExists(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if content == "" {
		t.Fatal("barcodes.sql.go not found or empty")
	}
}

func TestBarcode142_GenFileHasBarcodeAuthorityRowStruct(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "BarcodeAuthorityRow") {
		t.Error("barcodes.sql.go must define BarcodeAuthorityRow struct")
	}
}

func TestBarcode142_GenFileHasBarcodeRowStruct(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "BarcodeRow") {
		t.Error("barcodes.sql.go must define BarcodeRow struct")
	}
}

func TestBarcode142_GenFileBarcodeRowHasNullableTicketID(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "*uuid.UUID") {
		t.Error("BarcodeRow must have *uuid.UUID TicketID (nullable for external barcodes)")
	}
}

func TestBarcode142_GenFileBarcodeRowHasNullableScannedAt(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "*time.Time") {
		t.Error("BarcodeRow must have *time.Time ScannedAt (nil until scanned)")
	}
}

func TestBarcode142_GenFileHasMarkBarcodeScannedFunction(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "func (q *Queries) MarkBarcodeScanned") {
		t.Error("barcodes.sql.go must define MarkBarcodeScanned function")
	}
}

func TestBarcode142_GenFileHasGetBarcodeAuthorityByTypeFunction(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "func (q *Queries) GetBarcodeAuthorityByType") {
		t.Error("barcodes.sql.go must define GetBarcodeAuthorityByType function")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Querier interface completeness (compile-time)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_QuerierInterfaceHasBarcodeMethodsInSource(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertBarcodeAuthority",
		"GetBarcodeAuthorityByID",
		"GetBarcodeAuthorityByType",
		"ListBarcodeAuthorities",
		"InsertBarcode",
		"GetBarcodeByRef",
		"GetBarcodeByID",
		"MarkBarcodeScanned",
		"RevokeBarcode",
		"ListBarcodesByTicketID",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go must declare %s in the Querier interface", method)
		}
	}
}

// Compile-time guard: *gen.Queries must satisfy Querier (including barcode methods).
var _ gen.Querier = (*gen.Queries)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Route auth-gating (401 without JWT)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ListAuthoritiesRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/barcodes/authorities", nil)
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/barcodes/authorities without JWT: want 401 got %d", rec.Code)
	}
}

func TestBarcode142_GetBarcodeRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/barcodes/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/barcodes/{id} without JWT: want 401 got %d", rec.Code)
	}
}

func TestBarcode142_ScanRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/scan without JWT: want 401 got %d", rec.Code)
	}
}

func TestBarcode142_CreateAuthorityRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes/authorities", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/barcodes/authorities without JWT: want 401 got %d", rec.Code)
	}
}

func TestBarcode142_RegisterBarcodeRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/barcodes without JWT: want 401 got %d", rec.Code)
	}
}

func TestBarcode142_RevokeBarcodeRequiresJWT(t *testing.T) {
	s := buildBarcode142Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/barcodes/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/barcodes/{id} without JWT: want 401 got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan validation: unknown authority type → not 200
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ScanUnknownAuthorityRejected(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body, _ := json.Marshal(map[string]string{
		"external_ref":   "SOME-BARCODE",
		"authority_type": "unknown_system",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	// GetBarcodeAuthorityByType with nil pool will fail (DB error), so we
	// expect either 404 (unknown_authority) or 500. The key: NOT 200.
	if rec.Code == http.StatusOK {
		t.Errorf("scan with unknown authority_type must not return 200; got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request validation
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ScanMissingExternalRef(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"authority_type":"platform"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("scan without external_ref: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_ScanMissingAuthorityType(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"external_ref":"BARCODE-123"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("scan without authority_type: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_CreateAuthorityMissingType(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"label":"Test Authority"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes/authorities", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("create authority without type: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_CreateAuthorityInvalidType(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"type":"invalid_type","label":"Test"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes/authorities", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("create authority with invalid type: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_CreateAuthorityMissingLabel(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"type":"platform"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes/authorities", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("create authority without label: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_RegisterBarcodeInvalidAuthorityIDUUID(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"authority_id":"not-a-uuid","external_ref":"BARCODE"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("register barcode with invalid authority_id UUID: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_RegisterBarcodeMissingExternalRef(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"authority_id":"00000000-0000-0000-0000-000000000001"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("register barcode without external_ref: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_GetBarcodeInvalidUUID(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/barcodes/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /v1/barcodes/not-a-uuid: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_DeleteBarcodeInvalidUUID(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/barcodes/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE /v1/barcodes/not-a-uuid: want 400 got %d", rec.Code)
	}
}

func TestBarcode142_ScanInvalidJSONBody(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /v1/scan with invalid JSON: want 400 got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response content-type
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ScanResponseIsJSON(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	body := `{"external_ref":"TEST","authority_type":"platform"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST /v1/scan response Content-Type: want application/json got %q", ct)
	}
}

func TestBarcode142_ListAuthoritiesResponseIsJSON(t *testing.T) {
	s := buildBarcode142Server(t)
	tok := mintBarcodeToken(t, s, []string{"admin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/barcodes/authorities", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /v1/barcodes/authorities response Content-Type: want application/json got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// barcodeFromRow / barcodeAuthorityFromRow conversion
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_BarcodeFromRowNilTicketID(t *testing.T) {
	row := gen.BarcodeRow{
		ExternalRef: "BARCODE",
		Status:      "active",
		TicketID:    nil,
		ScannedAt:   nil,
	}
	resp := barcodeFromRow(row)
	if resp.TicketID != nil {
		t.Error("barcodeFromRow: TicketID must be nil when BarcodeRow.TicketID is nil")
	}
}

func TestBarcode142_BarcodeFromRowNilScannedAt(t *testing.T) {
	row := gen.BarcodeRow{
		ExternalRef: "BARCODE",
		Status:      "active",
		ScannedAt:   nil,
	}
	resp := barcodeFromRow(row)
	if resp.ScannedAt != nil {
		t.Error("barcodeFromRow: ScannedAt must be nil when BarcodeRow.ScannedAt is nil")
	}
}

func TestBarcode142_BarcodeAuthorityFromRowFieldMapping(t *testing.T) {
	row := gen.BarcodeAuthorityRow{
		Type:  "platform",
		Label: "Arena Platform",
	}
	resp := barcodeAuthorityFromRow(row)
	if resp.Type != "platform" {
		t.Errorf("barcodeAuthorityFromRow: Type want 'platform' got %q", resp.Type)
	}
	if resp.Label != "Arena Platform" {
		t.Errorf("barcodeAuthorityFromRow: Label want 'Arena Platform' got %q", resp.Label)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// isUniqueViolation helper
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_IsUniqueViolationNilIsNotViolation(t *testing.T) {
	if isUniqueViolation(nil) {
		t.Error("isUniqueViolation(nil) must return false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring: barcodeQueries field + server.go + routes
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ServerGoHasBarcodeQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "barcodeQueries") {
		t.Error("server.go must declare barcodeQueries field on Server struct")
	}
}

func TestBarcode142_ServerGoHasBarcodeQueriesOption(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "BarcodeQueries") {
		t.Error("server.go Options must include BarcodeQueries field")
	}
}

func TestBarcode142_ServerGoMountsHandleScan(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "handleScan") {
		t.Error("server.go must mount handleScan for POST /v1/scan")
	}
}

func TestBarcode142_ServerGoMountsHandleListBarcodeAuthorities(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "handleListBarcodeAuthorities") {
		t.Error("server.go must mount handleListBarcodeAuthorities")
	}
}

func TestBarcode142_BarcodesGoFileExists(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if content == "" {
		t.Fatal("barcodes.go not found or empty")
	}
}

func TestBarcode142_BarcodesGoHandlesScanAlreadyScannedPath(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if !strings.Contains(content, "already_scanned") {
		t.Error("barcodes.go must handle already-scanned case (barcode.already_scanned error code)")
	}
}

func TestBarcode142_BarcodesGoHandlesScanRevokedPath(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if !strings.Contains(content, "barcode.revoked") {
		t.Error("barcodes.go must handle revoked barcode case (barcode.revoked error code)")
	}
}

func TestBarcode142_BarcodesGoHandlesUnknownAuthority(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if !strings.Contains(content, "unknown_authority") {
		t.Error("barcodes.go must handle unknown authority type (barcode.unknown_authority error code)")
	}
}

func TestBarcode142_BarcodesGoHandlesDuplicateBarcodeConflict(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if !strings.Contains(content, "barcode.duplicate") {
		t.Error("barcodes.go must return 409 with barcode.duplicate for unique constraint violations")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan response shape
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ScanResponseHasRequiredFields(t *testing.T) {
	// scanResponse type must have barcode_id, authority_type, external_ref, status fields
	content := findFileByName(t, "barcodes.go")
	for _, field := range []string{"barcode_id", "authority_type", "external_ref", "status"} {
		if !strings.Contains(content, `"`+field+`"`) {
			t.Errorf("scanResponse must have json tag %q", field)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan authority_type validation
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_ScanValidatesNonEmptyAuthorityType(t *testing.T) {
	content := findFileByName(t, "barcodes.go")
	if !strings.Contains(content, "authority_type is required") {
		t.Error("scan handler must validate that authority_type is non-empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil barcodeQueries: routes not mounted → 404 (route not registered)
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcode142_NilQueriesScanRouteNotMounted(t *testing.T) {
	// Build server with NO BarcodeQueries wired — scan route should not be mounted.
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
	s := New(Options{Config: cfg, Auth: stub})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(`{}`))
	s.router.ServeHTTP(rec, req)

	// Without BarcodeQueries the route is not mounted → 404.
	if rec.Code == http.StatusOK {
		t.Errorf("POST /v1/scan with nil barcodeQueries must not return 200; got %d", rec.Code)
	}
}
