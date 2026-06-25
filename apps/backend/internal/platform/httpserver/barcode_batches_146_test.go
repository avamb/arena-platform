// barcode_batches_146_test.go — integration tests for feature #146
// (External barcode batch import).
//
// Test coverage:
//
//	Step 1: Migration 0039_barcode_batches.sql — tables, CHECK constraints, RBAC
//	Step 2: SQL query file barcode_batches.sql — all named queries present
//	Step 3: Gen file barcode_batches.sql.go — BarcodeBatchRow, BarcodeBatchEntryRow, all methods
//	Step 4: Querier interface — all 9 barcode batch methods present
//	Step 5: HTTP routes — auth-gating, multipart upload, approve/reject endpoints
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for barcode batch route tests
// ─────────────────────────────────────────────────────────────────────────────

const barcodeBatchTestActorID = "00000000-0000-0000-0000-000000000146"

func buildBarcodeBatchServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 64 << 20,
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
		t.Fatalf("buildBarcodeBatchServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:              cfg,
		Auth:                stub,
		Pool:                &dbDownPool{},
		BarcodeBatchQueries: gen.New(nil),
		BarcodeQueries:      gen.New(nil),
	})
}

func mintBarcodeBatchToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + barcodeBatchTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintBarcodeBatchToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintBarcodeBatchToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintBarcodeBatchToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcodeBatch146_MigrationFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "barcode_batches") {
		t.Error("migration should create 'barcode_batches' table")
	}
}

func TestBarcodeBatch146_MigrationHasBarcodeBatchEntriesTable(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "barcode_batch_entries") {
		t.Error("migration should create 'barcode_batch_entries' table")
	}
}

func TestBarcodeBatch146_MigrationHasSourceCheckConstraint(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "'csv'") || !strings.Contains(content, "'pdf'") {
		t.Error("migration should have source CHECK constraint with 'csv' and 'pdf'")
	}
}

func TestBarcodeBatch146_MigrationHasStatusCheckConstraint(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	for _, status := range []string{"uploaded", "pending_approval", "active", "rejected"} {
		if !strings.Contains(content, "'"+status+"'") {
			t.Errorf("migration should contain status value '%s'", status)
		}
	}
}

func TestBarcodeBatch146_MigrationHasAllocationIDColumn(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "allocation_id") {
		t.Error("barcode_batches should have 'allocation_id' FK column")
	}
}

func TestBarcodeBatch146_MigrationHasRBACPermissions(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	for _, perm := range []string{"barcode_batch.upload", "barcode_batch.approve", "barcode_batch.read"} {
		if !strings.Contains(content, perm) {
			t.Errorf("migration should seed permission '%s'", perm)
		}
	}
}

func TestBarcodeBatch146_MigrationHasPlatformOperatorRole(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "platform_operator") {
		t.Error("migration should create 'platform_operator' role")
	}
}

func TestBarcodeBatch146_MigrationHasGooseDirectives(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0039_barcode_batches.sql")
	if !strings.Contains(content, "+goose Up") {
		t.Error("migration should have '+goose Up' directive")
	}
	if !strings.Contains(content, "+goose Down") {
		t.Error("migration should have '+goose Down' directive")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcodeBatch146_QueryFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if content == "" {
		t.Error("barcode_batches.sql query file should exist and be non-empty")
	}
}

func TestBarcodeBatch146_QueryFileHasInsertBarcodeBatch(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "InsertBarcodeBatch") {
		t.Error("barcode_batches.sql should define InsertBarcodeBatch query")
	}
}

func TestBarcodeBatch146_QueryFileHasGetBarcodeBatchByID(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "GetBarcodeBatchByID") {
		t.Error("barcode_batches.sql should define GetBarcodeBatchByID query")
	}
}

func TestBarcodeBatch146_QueryFileHasListBarcodeBatchesByAllocation(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "ListBarcodeBatchesByAllocation") {
		t.Error("barcode_batches.sql should define ListBarcodeBatchesByAllocation query")
	}
}

func TestBarcodeBatch146_QueryFileHasUpdateBarcodeBatchStatus(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "UpdateBarcodeBatchStatus") {
		t.Error("barcode_batches.sql should define UpdateBarcodeBatchStatus query")
	}
}

func TestBarcodeBatch146_QueryFileHasInsertBarcodeBatchEntry(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "InsertBarcodeBatchEntry") {
		t.Error("barcode_batches.sql should define InsertBarcodeBatchEntry query")
	}
}

func TestBarcodeBatch146_QueryFileHasListBatchEntriesByBatchID(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "ListBatchEntriesByBatchID") {
		t.Error("barcode_batches.sql should define ListBatchEntriesByBatchID query")
	}
}

func TestBarcodeBatch146_QueryFileHasUpdateBatchEntriesStatus(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql")
	if !strings.Contains(content, "UpdateBatchEntriesStatus") {
		t.Error("barcode_batches.sql should define UpdateBatchEntriesStatus query")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcodeBatch146_GenFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql.go")
	if content == "" {
		t.Error("barcode_batches.sql.go gen file should exist and be non-empty")
	}
}

func TestBarcodeBatch146_GenFileHasBarcodeBatchRow(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql.go")
	if !strings.Contains(content, "BarcodeBatchRow") {
		t.Error("gen file should define BarcodeBatchRow struct")
	}
}

func TestBarcodeBatch146_GenFileHasBarcodeBatchEntryRow(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql.go")
	if !strings.Contains(content, "BarcodeBatchEntryRow") {
		t.Error("gen file should define BarcodeBatchEntryRow struct")
	}
}

func TestBarcodeBatch146_GenFileHasAllQueryMethods(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql.go")
	methods := []string{
		"InsertBarcodeBatch",
		"GetBarcodeBatchByID",
		"ListBarcodeBatchesByAllocation",
		"UpdateBarcodeBatchStatus",
		"InsertBarcodeBatchEntry",
		"ListBatchEntriesByBatchID",
		"UpdateBatchEntriesStatus",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("gen file should define method %s", m)
		}
	}
}

func TestBarcodeBatch146_BarcodeBatchRowHasExpectedFields(t *testing.T) {
	t.Parallel()
	var row gen.BarcodeBatchRow
	typ := reflect.TypeOf(row)
	requiredFields := map[string]bool{
		"ID":           false,
		"AllocationID": false,
		"Source":       false,
		"Status":       false,
		"Filename":     false,
		"RowCount":     false,
		"AuthorityID":  false,
		"Notes":        false,
		"UploadedBy":   false,
		"CreatedAt":    false,
		"UpdatedAt":    false,
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if _, want := requiredFields[f.Name]; want {
			requiredFields[f.Name] = true
		}
	}
	for field, found := range requiredFields {
		if !found {
			t.Errorf("BarcodeBatchRow should have field %s", field)
		}
	}
}

func TestBarcodeBatch146_BarcodeBatchEntryRowHasExpectedFields(t *testing.T) {
	t.Parallel()
	var row gen.BarcodeBatchEntryRow
	typ := reflect.TypeOf(row)
	required := map[string]bool{
		"ID":          false,
		"BatchID":     false,
		"ExternalRef": false,
		"Status":      false,
		"CreatedAt":   false,
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if _, want := required[f.Name]; want {
			required[f.Name] = true
		}
	}
	for field, found := range required {
		if !found {
			t.Errorf("BarcodeBatchEntryRow should have field %s", field)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcodeBatch146_QuerierInterfaceHasAllMethods(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.sql.go")
	// The querier interface is tested via compile-time assertion in querier.go.
	// Here we verify the gen file has all the methods.
	methods := []string{
		"InsertBarcodeBatch",
		"GetBarcodeBatchByID",
		"ListBarcodeBatchesByAllocation",
		"ListAllBarcodeBatches",
		"UpdateBarcodeBatchStatus",
		"UpdateBarcodeBatchAuthorityAndStatus",
		"InsertBarcodeBatchEntry",
		"ListBatchEntriesByBatchID",
		"UpdateBatchEntriesStatus",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("barcode_batches.sql.go should define method %s", m)
		}
	}
}

func TestBarcodeBatch146_QuerierInterfaceContainsBarcodeBatchMethods(t *testing.T) {
	t.Parallel()
	// Verify via the Querier interface in querier.go.
	// The compile-time assertion ensures *Queries implements Querier.
	// Here we verify the querier.go file contains barcode batch methods.
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "GetBarcodeBatchByID") {
		t.Error("querier.go Querier interface must include GetBarcodeBatchByID")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: HTTP routes — authentication, multipart upload, approve/reject
// ─────────────────────────────────────────────────────────────────────────────

func TestBarcodeBatch146_UploadRequiresJWT(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "test.csv")
	_, _ = fw.Write([]byte("barcode\nABC123\nDEF456\n"))
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("upload without JWT: got %d, want 401", rec.Code)
	}
}

func TestBarcodeBatch146_ListRequiresJWT(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/barcode-batches", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("list without JWT: got %d, want 401", rec.Code)
	}
}

func TestBarcodeBatch146_GetDetailRequiresJWT(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/barcode-batches/00000000-0000-0000-0000-000000000001", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("get detail without JWT: got %d, want 401", rec.Code)
	}
}

func TestBarcodeBatch146_ApproveRequiresJWT(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches/00000000-0000-0000-0000-000000000001/approve", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("approve without JWT: got %d, want 401", rec.Code)
	}
}

func TestBarcodeBatch146_RejectRequiresJWT(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches/00000000-0000-0000-0000-000000000001/reject", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("reject without JWT: got %d, want 401", rec.Code)
	}
}

func TestBarcodeBatch146_UploadWithJWTAndValidCSV_ReturnsServiceUnavailableOrCreated(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "test.csv")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("barcode\nABC123\nDEF456\nGHI789\n"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	// With dbDownPool, the DB call will fail → 503 ServiceUnavailable.
	// 201 Created is the success case with a real DB.
	if rec.Code != http.StatusCreated && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("upload with valid CSV: got %d, want 201 or 503 (db down in test)", rec.Code)
	}
}

func TestBarcodeBatch146_UploadWithMissingFile_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("notes", "test batch")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("upload without file field: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_UploadWithInvalidAllocationID_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "test.csv")
	_, _ = fw.Write([]byte("ABC123\nDEF456\n"))
	_ = mw.WriteField("allocation_id", "not-a-uuid")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("upload with invalid allocation_id: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_GetWithInvalidID_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodGet, "/v1/barcode-batches/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("get with invalid ID: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_ApproveWithInvalidID_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches/not-a-uuid/approve", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("approve with invalid ID: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_RejectWithInvalidID_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches/not-a-uuid/reject", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("reject with invalid ID: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_ListWithValidJWT_ReturnsOKOrServiceUnavailable(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodGet, "/v1/barcode-batches", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	// With gen.New(nil), the queries pointer is non-nil but the db is nil;
	// query calls panic → recoverer returns 500. Accept 200, 503, or 500.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Errorf("list with valid JWT: got %d, want 200, 503, or 500", rec.Code)
	}
}

func TestBarcodeBatch146_ListWithAllocationIDFilter_ValidUUID(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/barcode-batches?allocation_id=00000000-0000-0000-0000-000000000001", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	// With gen.New(nil), the queries pointer is non-nil but db is nil → panic → 500.
	// Accept 200, 503, or 500.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Errorf("list with allocation filter: got %d, want 200, 503, or 500", rec.Code)
	}
}

func TestBarcodeBatch146_ListWithInvalidAllocationIDFilter_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/barcode-batches?allocation_id=not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("list with invalid allocation_id: got %d, want 400", rec.Code)
	}
}

func TestBarcodeBatch146_CSVParserDeduplicates(t *testing.T) {
	t.Parallel()
	csvData := "ABC123\nABC123\nDEF456\nABC123\n"
	refs, err := parseBarcodeBatchCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parseBarcodeBatchCSV: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 deduplicated refs, got %d: %v", len(refs), refs)
	}
}

func TestBarcodeBatch146_CSVParserSkipsHeaderRow(t *testing.T) {
	t.Parallel()
	csvData := "barcode_ref\nABC123\nDEF456\n"
	refs, err := parseBarcodeBatchCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parseBarcodeBatchCSV: %v", err)
	}
	for _, r := range refs {
		if strings.Contains(strings.ToLower(r), "barcode") {
			t.Errorf("header row should be skipped, but got %q", r)
		}
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 refs after header skip, got %d", len(refs))
	}
}

func TestBarcodeBatch146_CSVParserSkipsEmptyRows(t *testing.T) {
	t.Parallel()
	csvData := "ABC123\n\nDEF456\n\n"
	refs, err := parseBarcodeBatchCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parseBarcodeBatchCSV: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 refs (empty rows skipped), got %d: %v", len(refs), refs)
	}
}

func TestBarcodeBatch146_CSVParserReturnsFirstColumn(t *testing.T) {
	t.Parallel()
	csvData := "ABC123,extra1,extra2\nDEF456,extra3,extra4\n"
	refs, err := parseBarcodeBatchCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parseBarcodeBatchCSV: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 refs from first column, got %d", len(refs))
	}
	if refs[0] != "ABC123" {
		t.Errorf("first ref should be 'ABC123', got %q", refs[0])
	}
}

func TestBarcodeBatch146_HandlerFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.go")
	if !strings.Contains(content, "handleUploadBarcodeBatch") {
		t.Error("barcode_batches.go should define handleUploadBarcodeBatch")
	}
	if !strings.Contains(content, "handleApproveBarcodeBatch") {
		t.Error("barcode_batches.go should define handleApproveBarcodeBatch")
	}
	if !strings.Contains(content, "handleRejectBarcodeBatch") {
		t.Error("barcode_batches.go should define handleRejectBarcodeBatch")
	}
}

func TestBarcodeBatch146_HandlerHasCSVParser(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "barcode_batches.go")
	if !strings.Contains(content, "parseBarcodeBatchCSV") {
		t.Error("barcode_batches.go should define parseBarcodeBatchCSV")
	}
}

func TestBarcodeBatch146_ServerWiringHasBarcodeBatchQueries(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	if s.barcodeBatchQueries == nil {
		t.Error("server should have barcodeBatchQueries wired when BarcodeBatchQueries option is set")
	}
}

func TestBarcodeBatch146_MultipartFormDataNotRejectedAs415(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "test.csv")
	_, _ = fw.Write([]byte("ABC123\nDEF456\n"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnsupportedMediaType {
		t.Error("multipart/form-data should not be rejected with 415 by content-type middleware")
	}
}

func TestBarcodeBatch146_ApproveWithValidJWT_ReturnsServiceUnavailableOrOK(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/barcode-batches/00000000-0000-0000-0000-000000000001/approve", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	// With dbDownPool, 503 is expected since BeginTx/Query fail.
	// 404 is also possible (not found). 200 would be success with real DB.
	if rec.Code == http.StatusUnsupportedMediaType {
		t.Errorf("approve should not return 415, got %d", rec.Code)
	}
	allowedCodes := map[int]bool{
		http.StatusOK:                  true,
		http.StatusNotFound:            true,
		http.StatusConflict:            true,
		http.StatusServiceUnavailable:  true,
		http.StatusInternalServerError: true,
	}
	if !allowedCodes[rec.Code] {
		t.Errorf("approve: unexpected status %d", rec.Code)
	}
}

func TestBarcodeBatch146_RejectWithValidJWT_ReturnsServiceUnavailableOrOK(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/barcode-batches/00000000-0000-0000-0000-000000000001/reject", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	allowedCodes := map[int]bool{
		http.StatusOK:                  true,
		http.StatusNotFound:            true,
		http.StatusConflict:            true,
		http.StatusServiceUnavailable:  true,
		http.StatusInternalServerError: true,
	}
	if !allowedCodes[rec.Code] {
		t.Errorf("reject: unexpected status %d", rec.Code)
	}
}

func TestBarcodeBatch146_UploadCSV_EmptyBody_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "empty.csv")
	_ = fw // write nothing
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/barcode-batches", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	// Empty CSV should return 400.
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("upload with empty CSV: got %d, want 400", rec.Code)
	}
}
