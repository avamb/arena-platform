// credentials_140_test.go — unit tests for feature #140 (Ticket credentials: static QR + PDF).
//
// Test coverage:
//   Step 1: Migration file 0027_ticket_credentials.sql — table, type check, RBAC
//   Step 2: SQL query file ticket_credentials.sql — all 4 named queries present
//   Step 3: Gen file ticket_credentials.sql.go — TicketCredentialRow type, all 4 functions
//   Step 4: Querier interface — credential methods present (compile-time)
//   Step 5: QR token generator — generateQRToken produces valid 64-char hex token
//   Step 6: PDF renderer — renderTicketPDF produces valid PDF bytes
//   Step 7: HTTP route — GET /v1/tickets/{id}/credential requires auth (401)
//   Step 8: HTTP route — GET /v1/tickets/{id}/credential mounted and reachable with auth
//   Step 9: credentialFromRow — correct JSON shape, RFC3339 timestamps, nil-safe RevokedAt
//   Step 10: Route validation — invalid UUID returns 400; invalid type returns 400
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
// Server factory for credential route tests
// ─────────────────────────────────────────────────────────────────────────────

const credentialTestActorID = "00000000-0000-0000-0000-000000000140"

// buildCredentialServer builds a Server with stub auth, credential routes fully
// mounted, and gen.New(nil) so real DB operations never execute.
func buildCredentialServer(t *testing.T) *Server {
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
		t.Fatalf("buildCredentialServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		TicketQueries:     gen.New(nil),
		CredentialQueries: gen.New(nil),
	})
}

// mintCredentialToken mints a dev JWT for credential route tests.
func mintCredentialToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + credentialTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintCredentialToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintCredentialToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintCredentialToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file 0027_ticket_credentials.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step1_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if content == "" {
		t.Fatal("0027_ticket_credentials.sql is empty or not found")
	}
}

func TestCredential140_Step1_MigrationHasGooseDirectives(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestCredential140_Step1_MigrationHasCredentialsTable(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "CREATE TABLE ticket_credentials") {
		t.Error("migration missing 'CREATE TABLE ticket_credentials'")
	}
}

func TestCredential140_Step1_MigrationHasRequiredColumns(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	for _, col := range []string{
		"ticket_id",
		"type",
		"payload",
		"issued_at",
		"revoked_at",
	} {
		if !strings.Contains(content, col) {
			t.Errorf("migration missing column '%s'", col)
		}
	}
}

func TestCredential140_Step1_MigrationHasTypeCheckConstraint(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "ticket_credentials_type_check") {
		t.Error("migration missing 'ticket_credentials_type_check' constraint name")
	}
	for _, credType := range []string{"static_qr", "pdf"} {
		if !strings.Contains(content, "'"+credType+"'") {
			t.Errorf("type check constraint missing value '%s'", credType)
		}
	}
}

func TestCredential140_Step1_MigrationHasUniqueConstraint(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "ticket_credentials_ticket_type_unique") {
		t.Error("migration missing 'ticket_credentials_ticket_type_unique' unique constraint")
	}
}

func TestCredential140_Step1_MigrationHasIndex(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "ticket_credentials_ticket_id") {
		t.Error("migration missing index 'ticket_credentials_ticket_id'")
	}
}

func TestCredential140_Step1_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	for _, perm := range []string{"credential.read", "credential.revoke"} {
		if !strings.Contains(content, "'"+perm+"'") {
			t.Errorf("migration missing RBAC seed '%s'", perm)
		}
	}
}

func TestCredential140_Step1_MigrationDownSection(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS ticket_credentials") {
		t.Error("migration Down section missing 'DROP TABLE IF EXISTS ticket_credentials'")
	}
}

func TestCredential140_Step1_MigrationFKCascade(t *testing.T) {
	content := findFileByName(t, "0027_ticket_credentials.sql")
	if !strings.Contains(content, "ON DELETE CASCADE") {
		t.Error("migration missing 'ON DELETE CASCADE' on ticket_id FK")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file ticket_credentials.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step2_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if content == "" {
		t.Fatal("ticket_credentials.sql query file is empty or not found")
	}
}

func TestCredential140_Step2_SQLQueryFileHasInsertTicketCredential(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if !strings.Contains(content, "InsertTicketCredential") {
		t.Error("ticket_credentials.sql missing 'InsertTicketCredential' query name")
	}
}

func TestCredential140_Step2_SQLQueryFileHasOnConflict(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if !strings.Contains(content, "ON CONFLICT") {
		t.Error("ticket_credentials.sql missing ON CONFLICT (required for idempotent upsert)")
	}
}

func TestCredential140_Step2_SQLQueryFileHasGetCredentialByTicketID(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if !strings.Contains(content, "GetCredentialByTicketID") {
		t.Error("ticket_credentials.sql missing 'GetCredentialByTicketID' query name")
	}
}

func TestCredential140_Step2_SQLQueryFileHasRevokeCredential(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if !strings.Contains(content, "RevokeCredential") {
		t.Error("ticket_credentials.sql missing 'RevokeCredential' query name")
	}
}

func TestCredential140_Step2_SQLQueryFileHasListCredentialsByTicketID(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql")
	if !strings.Contains(content, "ListCredentialsByTicketID") {
		t.Error("ticket_credentials.sql missing 'ListCredentialsByTicketID' query name")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file ticket_credentials.sql.go
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step3_GenFileExists(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql.go")
	if content == "" {
		t.Fatal("gen file ticket_credentials.sql.go is empty or not found")
	}
}

func TestCredential140_Step3_GenFileHasTicketCredentialRow(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql.go")
	if !strings.Contains(content, "type TicketCredentialRow struct") {
		t.Error("gen file missing 'type TicketCredentialRow struct'")
	}
}

func TestCredential140_Step3_GenFileHasAllFunctions(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql.go")
	for _, fn := range []string{
		"InsertTicketCredential",
		"GetCredentialByTicketID",
		"RevokeCredential",
		"ListCredentialsByTicketID",
	} {
		if !strings.Contains(content, "func (q *Queries) "+fn) {
			t.Errorf("gen file missing 'func (q *Queries) %s'", fn)
		}
	}
}

func TestCredential140_Step3_TicketCredentialRowHasRequiredFields(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql.go")
	for _, field := range []string{
		"ID",
		"TicketID",
		"Type",
		"Payload",
		"IssuedAt",
		"RevokedAt",
	} {
		if !strings.Contains(content, field) {
			t.Errorf("gen file TicketCredentialRow missing field '%s'", field)
		}
	}
}

func TestCredential140_Step3_RevokedAtIsNullable(t *testing.T) {
	content := findFileByName(t, "ticket_credentials.sql.go")
	// RevokedAt must be a pointer (*time.Time) to represent NULL revocation.
	if !strings.Contains(content, "*time.Time") {
		t.Error("gen file missing '*time.Time' — RevokedAt must be nullable (*time.Time)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface — compile-time check
// ─────────────────────────────────────────────────────────────────────────────

// TestCredential140_Step4_QuerierImplementsCredentialMethods verifies at
// compile time that gen.New(nil) satisfies gen.Querier, which now includes all
// 4 credential methods. If the interface is missing any method, the build fails
// before this test runs.
func TestCredential140_Step4_QuerierImplementsCredentialMethods(t *testing.T) {
	var _ gen.Querier = gen.New(nil)
}

func TestCredential140_Step4_QuerierFileHasCredentialMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertTicketCredential",
		"GetCredentialByTicketID",
		"RevokeCredential",
		"ListCredentialsByTicketID",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go missing credential method '%s'", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: QR token generator
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step5_GenerateQRTokenReturns64CharHex(t *testing.T) {
	token, err := generateQRToken()
	if err != nil {
		t.Fatalf("generateQRToken: unexpected error: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("QR token length = %d, want 64 (32 bytes as hex)", len(token))
	}
}

func TestCredential140_Step5_GenerateQRTokenIsHexLowercase(t *testing.T) {
	token, err := generateQRToken()
	if err != nil {
		t.Fatalf("generateQRToken: unexpected error: %v", err)
	}
	for i, c := range token {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("QR token contains non-lowercase-hex character %q at index %d", c, i)
			break
		}
	}
}

func TestCredential140_Step5_GenerateQRTokenIsUnique(t *testing.T) {
	// Statistical uniqueness: two independently generated tokens must differ.
	token1, err1 := generateQRToken()
	token2, err2 := generateQRToken()
	if err1 != nil || err2 != nil {
		t.Fatalf("generateQRToken errors: %v, %v", err1, err2)
	}
	if token1 == token2 {
		t.Error("two generated QR tokens are identical — not cryptographically random")
	}
}

func TestCredential140_Step5_CredentialsGoHasGenerateQRToken(t *testing.T) {
	content := findFileByName(t, "credentials.go")
	if !strings.Contains(content, "generateQRToken") {
		t.Error("credentials.go missing 'generateQRToken' function")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: PDF renderer
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step6_RenderTicketPDFReturnsPDFBytes(t *testing.T) {
	ticketID := uuid.New().String()
	token := "deadbeef" + strings.Repeat("0", 56)
	issuedAt := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	pdfBytes := renderTicketPDF(ticketID, token, issuedAt)
	if len(pdfBytes) == 0 {
		t.Fatal("renderTicketPDF returned empty byte slice")
	}
}

func TestCredential140_Step6_RenderTicketPDFHasPDFHeader(t *testing.T) {
	ticketID := uuid.New().String()
	token := "cafebabe" + strings.Repeat("0", 56)
	issuedAt := time.Now()

	pdfBytes := renderTicketPDF(ticketID, token, issuedAt)
	if !strings.HasPrefix(string(pdfBytes), "%PDF-") {
		t.Errorf("PDF output does not start with %%PDF- header; first bytes: %q",
			string(pdfBytes[:min(20, len(pdfBytes))]))
	}
}

func TestCredential140_Step6_RenderTicketPDFHasEOFMarker(t *testing.T) {
	pdfBytes := renderTicketPDF(uuid.New().String(), "aabbccdd"+strings.Repeat("0", 56), time.Now())
	content := string(pdfBytes)
	if !strings.Contains(content, "%%EOF") {
		t.Error("PDF output missing end-of-file trailer marker")
	}
}

func TestCredential140_Step6_RenderTicketPDFContainsTicketID(t *testing.T) {
	ticketID := uuid.New().String()
	pdfBytes := renderTicketPDF(ticketID, "aabbccdd"+strings.Repeat("0", 56), time.Now())
	if !strings.Contains(string(pdfBytes), ticketID) {
		t.Error("PDF output does not contain the ticket ID")
	}
}

func TestCredential140_Step6_CredentialsGoHasRenderTicketPDF(t *testing.T) {
	content := findFileByName(t, "credentials.go")
	if !strings.Contains(content, "renderTicketPDF") {
		t.Error("credentials.go missing 'renderTicketPDF' function")
	}
}

// min returns the smaller of a and b (stdlib min is Go 1.21+).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: HTTP route — GET /v1/tickets/{id}/credential requires auth
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step7_GetCredentialRequiresAuth(t *testing.T) {
	s := buildCredentialServer(t)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/00000000-0000-0000-0000-000000000001/credential", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: HTTP route — mounted and reachable with auth
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step8_GetCredentialMountedWithAuth(t *testing.T) {
	s := buildCredentialServer(t)
	tok := mintCredentialToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/00000000-0000-0000-0000-000000000001/credential", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route must be mounted — must not return 404 http.not_found.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
		t.Errorf("credential route not mounted: got 404 http.not_found; body=%s", w.Body.String())
	}
	// Auth must pass — must not return 401 or 403.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("unexpected auth failure on authenticated request: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCredential140_Step8_GetCredentialWithTypePDFMounted(t *testing.T) {
	s := buildCredentialServer(t)
	tok := mintCredentialToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/00000000-0000-0000-0000-000000000001/credential?type=pdf", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route must be mounted.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "http.not_found") {
		t.Errorf("credential route (type=pdf) not mounted: got 404; body=%s", w.Body.String())
	}
	// Must not be auth-gated.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("unexpected auth failure: got %d body=%s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: credentialFromRow — conversion correctness
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step9_CredentialFromRowTimestamps(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	row := gen.TicketCredentialRow{
		ID:       uuid.New(),
		TicketID: uuid.New(),
		Type:     "static_qr",
		Payload:  "deadbeef" + strings.Repeat("0", 56),
		IssuedAt: now,
	}
	resp := credentialFromRow(row)

	want := "2026-06-24T12:00:00Z"
	if resp.IssuedAt != want {
		t.Errorf("IssuedAt = %q, want %q", resp.IssuedAt, want)
	}
}

func TestCredential140_Step9_CredentialFromRowNilRevokedAt(t *testing.T) {
	row := gen.TicketCredentialRow{
		ID:        uuid.New(),
		TicketID:  uuid.New(),
		Type:      "static_qr",
		Payload:   "aabbccdd" + strings.Repeat("0", 56),
		IssuedAt:  time.Now(),
		RevokedAt: nil,
	}
	resp := credentialFromRow(row)
	if resp.RevokedAt != nil {
		t.Error("expected RevokedAt to be nil when row.RevokedAt is nil")
	}
}

func TestCredential140_Step9_CredentialFromRowNonNilRevokedAt(t *testing.T) {
	now := time.Date(2026, 6, 24, 15, 30, 0, 0, time.UTC)
	row := gen.TicketCredentialRow{
		ID:        uuid.New(),
		TicketID:  uuid.New(),
		Type:      "static_qr",
		Payload:   "11223344" + strings.Repeat("0", 56),
		IssuedAt:  now.Add(-1 * time.Hour),
		RevokedAt: &now,
	}
	resp := credentialFromRow(row)
	if resp.RevokedAt == nil {
		t.Error("expected RevokedAt to be non-nil when row.RevokedAt is set")
	}
	want := "2026-06-24T15:30:00Z"
	if *resp.RevokedAt != want {
		t.Errorf("RevokedAt = %q, want %q", *resp.RevokedAt, want)
	}
}

func TestCredential140_Step9_CredentialFromRowType(t *testing.T) {
	for _, credType := range []string{"static_qr", "pdf"} {
		row := gen.TicketCredentialRow{
			ID:       uuid.New(),
			TicketID: uuid.New(),
			Type:     credType,
			Payload:  "aabbccdd",
			IssuedAt: time.Now(),
		}
		resp := credentialFromRow(row)
		if resp.Type != credType {
			t.Errorf("Type = %q, want %q", resp.Type, credType)
		}
	}
}

func TestCredential140_Step9_CredentialFromRowPayloadPassthrough(t *testing.T) {
	payload := "cafebabe" + strings.Repeat("ff", 28)
	row := gen.TicketCredentialRow{
		ID:       uuid.New(),
		TicketID: uuid.New(),
		Type:     "static_qr",
		Payload:  payload,
		IssuedAt: time.Now(),
	}
	resp := credentialFromRow(row)
	if resp.Payload != payload {
		t.Errorf("Payload = %q, want %q", resp.Payload, payload)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Route validation — invalid inputs return 400
// ─────────────────────────────────────────────────────────────────────────────

func TestCredential140_Step10_InvalidUUIDReturns400(t *testing.T) {
	s := buildCredentialServer(t)
	tok := mintCredentialToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/not-a-uuid/credential", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "credential.invalid_ticket_id") {
		t.Errorf("expected 'credential.invalid_ticket_id', got: %s", w.Body.String())
	}
}

func TestCredential140_Step10_InvalidTypeReturns400(t *testing.T) {
	s := buildCredentialServer(t)
	tok := mintCredentialToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/00000000-0000-0000-0000-000000000001/credential?type=nfc", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid type, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "credential.invalid_type") {
		t.Errorf("expected 'credential.invalid_type', got: %s", w.Body.String())
	}
}

func TestCredential140_Step10_DefaultTypeIsStaticQR(t *testing.T) {
	content := findFileByName(t, "credentials.go")
	if !strings.Contains(content, `"static_qr"`) {
		t.Error("credentials.go missing 'static_qr' as default credential type")
	}
}

func TestCredential140_Step10_NilCredentialQueriesReturns503(t *testing.T) {
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
	// CredentialQueries intentionally nil (not supplied to Options).
	s := New(Options{Config: cfg, Auth: stub, Pool: &dbDownPool{}})

	// The handler should return 503 when credentialQueries is nil.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/00000000-0000-0000-0000-000000000001/credential", nil)
	s.handleGetCredential(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when credentialQueries nil, got %d body=%s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dependency.database_unavailable") {
		t.Errorf("expected 'dependency.database_unavailable', got: %s", w.Body.String())
	}
}
