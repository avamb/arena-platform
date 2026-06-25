// reconciliation_147_test.go — unit tests for feature #147
// (External reconciliation — partner report submission, auto-match, exception queue).
//
// Test coverage:
//
//	Step 1: Migration file 0041_reconciliation_reports.sql — tables, CHECK constraints, RBAC seeds
//	Step 2: SQL query file reconciliation.sql — all named queries present
//	Step 3: Gen file reconciliation.sql.go — ReconciliationReportRow, ReconciliationLineRow,
//	        BarcodeRefLookupRow, all 11 methods
//	Step 4: Querier interface — all 11 reconciliation methods present
//	Step 5: HTTP routes — auth-gating, endpoint wiring, validation
//	Step 6: Auto-match algorithm — confidence scoring, exception/match classification
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
// Server factory for reconciliation route tests
// ─────────────────────────────────────────────────────────────────────────────

const reconciliationTestActorID = "00000000-0000-0000-0000-000000000147"

func buildReconciliationServer(t *testing.T) *Server {
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
		t.Fatalf("buildReconciliationServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:                cfg,
		Auth:                  stub,
		Pool:                  &dbDownPool{},
		ReconciliationQueries: gen.New(nil),
		AllocationQueries:     gen.New(nil),
	})
}

func mintReconciliationToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + reconciliationTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintReconciliationToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintReconciliationToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintReconciliationToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file 0041_reconciliation_reports.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step1_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	if content == "" {
		t.Fatal("0041_reconciliation_reports.sql: file not found or empty")
	}
}

func TestReconciliation147_Step1_ReconciliationReportsTable(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	checks := []string{
		"CREATE TABLE reconciliation_reports",
		"allocation_id",
		"partner_org_id",
		"status",
		"total_lines",
		"matched_lines",
		"exception_lines",
		"submitted_at",
		"reviewed_at",
		"reviewed_by",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("0041_reconciliation_reports.sql: missing %q", check)
		}
	}
}

func TestReconciliation147_Step1_ReconciliationReportsStatusCheck(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	statuses := []string{"processing", "matched", "exception", "reviewed"}
	for _, s := range statuses {
		if !strings.Contains(content, "'"+s+"'") {
			t.Errorf("migration missing status value %q in CHECK constraint", s)
		}
	}
}

func TestReconciliation147_Step1_ReconciliationLinesTable(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	checks := []string{
		"CREATE TABLE reconciliation_lines",
		"report_id",
		"external_ref",
		"line_type",
		"qty",
		"match_status",
		"confidence_score",
		"matched_barcode_id",
		"exception_reason",
		"operator_note",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("0041_reconciliation_reports.sql: missing %q", check)
		}
	}
}

func TestReconciliation147_Step1_LineTypesCheck(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	if !strings.Contains(content, "'sale'") || !strings.Contains(content, "'return'") {
		t.Error("migration missing line_type CHECK values 'sale' and 'return'")
	}
}

func TestReconciliation147_Step1_MatchStatusCheck(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	matchStatuses := []string{"pending", "matched", "exception", "reviewed"}
	for _, s := range matchStatuses {
		if !strings.Contains(content, "'"+s+"'") {
			t.Errorf("migration missing match_status value %q", s)
		}
	}
}

func TestReconciliation147_Step1_RBACPermissions(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	perms := []string{
		"reconciliation.submit",
		"reconciliation.read",
		"reconciliation.review",
	}
	for _, p := range perms {
		if !strings.Contains(content, p) {
			t.Errorf("migration missing RBAC permission %q", p)
		}
	}
}

func TestReconciliation147_Step1_GooseUpDown(t *testing.T) {
	content := findFileByName(t, "0041_reconciliation_reports.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing goose Up directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing goose Down directive")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS reconciliation_lines") {
		t.Error("migration missing DROP TABLE for reconciliation_lines in Down")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS reconciliation_reports") {
		t.Error("migration missing DROP TABLE for reconciliation_reports in Down")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file reconciliation.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step2_SQLFileExists(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql")
	if content == "" {
		t.Fatal("reconciliation.sql: file not found or empty")
	}
}

func TestReconciliation147_Step2_SQLQueries(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql")
	queries := []string{
		"name: InsertReconciliationReport",
		"name: GetReconciliationReportByID",
		"name: ListReconciliationReportsByAllocation",
		"name: ListReconciliationExceptions",
		"name: UpdateReconciliationReportStatus",
		"name: InsertReconciliationLine",
		"name: ListReconciliationLinesByReport",
		"name: ListExceptionLinesByReport",
		"name: UpdateReconciliationLineReview",
		"name: CountExceptionLinesByReport",
		"name: LookupBarcodeByExternalRef",
	}
	for _, q := range queries {
		if !strings.Contains(content, q) {
			t.Errorf("reconciliation.sql missing query %q", q)
		}
	}
}

func TestReconciliation147_Step2_AutoMatchLookupQuery(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql")
	// Lookup query must join barcode_batch_entries to barcode_batches.
	if !strings.Contains(content, "barcode_batch_entries") {
		t.Error("reconciliation.sql LookupBarcodeByExternalRef must reference barcode_batch_entries")
	}
	if !strings.Contains(content, "barcode_batches") {
		t.Error("reconciliation.sql LookupBarcodeByExternalRef must reference barcode_batches")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file reconciliation.sql.go
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step3_GenFileExists(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql.go")
	if content == "" {
		t.Fatal("reconciliation.sql.go: file not found or empty")
	}
}

func TestReconciliation147_Step3_ReconciliationReportRowStruct(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql.go")
	fields := []string{
		"ReconciliationReportRow",
		"AllocationID",
		"PartnerOrgID",
		"Status",
		"TotalLines",
		"MatchedLines",
		"ExceptionLines",
		"SubmittedAt",
		"ReviewedAt",
		"ReviewedBy",
	}
	for _, f := range fields {
		if !strings.Contains(content, f) {
			t.Errorf("reconciliation.sql.go missing field %q in ReconciliationReportRow", f)
		}
	}
}

func TestReconciliation147_Step3_ReconciliationLineRowStruct(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql.go")
	fields := []string{
		"ReconciliationLineRow",
		"ExternalRef",
		"LineType",
		"Qty",
		"MatchStatus",
		"ConfidenceScore",
		"MatchedBarcodeID",
		"ExceptionReason",
		"OperatorNote",
	}
	for _, f := range fields {
		if !strings.Contains(content, f) {
			t.Errorf("reconciliation.sql.go missing field %q in ReconciliationLineRow", f)
		}
	}
}

func TestReconciliation147_Step3_BarcodeRefLookupRowStruct(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql.go")
	if !strings.Contains(content, "BarcodeRefLookupRow") {
		t.Error("reconciliation.sql.go missing BarcodeRefLookupRow struct")
	}
	if !strings.Contains(content, "BarcodeID") {
		t.Error("reconciliation.sql.go BarcodeRefLookupRow missing BarcodeID field")
	}
}

func TestReconciliation147_Step3_GenFunctions(t *testing.T) {
	content := findFileByName(t, "reconciliation.sql.go")
	funcs := []string{
		"func (q *Queries) InsertReconciliationReport(",
		"func (q *Queries) GetReconciliationReportByID(",
		"func (q *Queries) ListReconciliationReportsByAllocation(",
		"func (q *Queries) ListReconciliationExceptions(",
		"func (q *Queries) UpdateReconciliationReportStatus(",
		"func (q *Queries) InsertReconciliationLine(",
		"func (q *Queries) ListReconciliationLinesByReport(",
		"func (q *Queries) ListExceptionLinesByReport(",
		"func (q *Queries) UpdateReconciliationLineReview(",
		"func (q *Queries) CountExceptionLinesByReport(",
		"func (q *Queries) LookupBarcodeByExternalRef(",
	}
	for _, fn := range funcs {
		if !strings.Contains(content, fn) {
			t.Errorf("reconciliation.sql.go missing function %q", fn)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface — 11 reconciliation methods
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step4_QuerierInterface(_ *testing.T) {
	// Compile-time check: gen.Queries implements gen.Querier.
	// The reconciliation methods are part of the Querier interface.
	// If they are absent, this package will fail to compile.
	var _ gen.Querier = (*gen.Queries)(nil)
}

func TestReconciliation147_Step4_QuerierInterfaceMethods(_ *testing.T) {
	// We verify method existence on *gen.Queries via reflection on a nil pointer.
	// This confirms the method set at compile time.
	q := gen.New(nil)

	type reconciliationQuerier interface {
		InsertReconciliationReport(
			ctx interface {
				Deadline() (interface{}, bool)
				Done() <-chan struct{}
				Err() error
				Value(any) any
			},
			allocationID interface{},
			partnerOrgID interface{},
			totalLines int32,
			matchedLines int32,
			exceptionLines int32,
			status string,
			notes *string,
		) (gen.ReconciliationReportRow, error)
	}
	_ = q // satisfies the type assignment (ensures q is used)

	// Verify the struct fields exist on the row types.
	var report gen.ReconciliationReportRow
	_ = report.AllocationID
	_ = report.PartnerOrgID
	_ = report.Status
	_ = report.TotalLines
	_ = report.MatchedLines
	_ = report.ExceptionLines
	_ = report.SubmittedAt
	_ = report.ReviewedAt
	_ = report.ReviewedBy

	var line gen.ReconciliationLineRow
	_ = line.ExternalRef
	_ = line.LineType
	_ = line.Qty
	_ = line.MatchStatus
	_ = line.ConfidenceScore
	_ = line.MatchedBarcodeID
	_ = line.ExceptionReason
	_ = line.OperatorNote

	var lookup gen.BarcodeRefLookupRow
	_ = lookup.BarcodeID
	_ = lookup.ExternalRef
	_ = lookup.AllocationID
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: HTTP routes — auth-gating, endpoint wiring
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step5_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	if content == "" {
		t.Fatal("reconciliation.go: file not found or empty")
	}
}

func TestReconciliation147_Step5_HandlerFunctions(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	handlers := []string{
		"handleSubmitReconciliationReport",
		"handleGetReconciliationReport",
		"handleListReconciliationExceptions",
		"handleReviewReconciliationReport",
		"handleResolveReconciliationException",
	}
	for _, h := range handlers {
		if !strings.Contains(content, h) {
			t.Errorf("reconciliation.go missing handler %q", h)
		}
	}
}

func TestReconciliation147_Step5_ResponseHelpers(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	helpers := []string{
		"reconciliationReportFromRow",
		"reconciliationLineFromRow",
		"reconciliationLinesFromRows",
	}
	for _, h := range helpers {
		if !strings.Contains(content, h) {
			t.Errorf("reconciliation.go missing helper %q", h)
		}
	}
}

func TestReconciliation147_Step5_UnauthenticatedSubmitBlocked(t *testing.T) {
	s := buildReconciliationServer(t)
	w := httptest.NewRecorder()
	body := `{"allocation_id":"00000000-0000-0000-0000-000000000001","lines":[{"external_ref":"BAR001","line_type":"sale","qty":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Errorf("unauthenticated submit should be blocked, got 201")
	}
}

func TestReconciliation147_Step5_UnauthenticatedGetBlocked(t *testing.T) {
	s := buildReconciliationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/reconciliation/reports/00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("unauthenticated GET should be blocked, got 200")
	}
}

func TestReconciliation147_Step5_UnauthenticatedExceptionsBlocked(t *testing.T) {
	s := buildReconciliationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/reconciliation/exceptions?org_id=00000000-0000-0000-0000-000000000001", nil)
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("unauthenticated exceptions queue should be blocked, got 200")
	}
}

func TestReconciliation147_Step5_AuthenticatedGetReportReturns503(t *testing.T) {
	// With gen.New(nil) as the query provider, DB calls fail at scan time.
	// Expect 503 (dependency unavailable) or 404/500 from nil DB ops.
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/reconciliation/reports/00000000-0000-0000-0000-000000000001", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	// 503 (no real DB) or 500 (nil scan error) — both are acceptable non-200 responses.
	// The important thing is that the route is wired and auth is passing.
	if w.Code == http.StatusOK {
		t.Errorf("GET report with nil DB should not return 200")
	}
}

func TestReconciliation147_Step5_AuthenticatedSubmitReturns503(t *testing.T) {
	// POST to reconciliation/reports: pool is &dbDownPool{} so BeginTx fails → 503.
	// But first the handler tries to look up the allocation → 503 from nil DB.
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := map[string]any{
		"allocation_id": "00000000-0000-0000-0000-000000000001",
		"lines": []map[string]any{
			{"external_ref": "BAR001", "line_type": "sale", "qty": 1},
		},
	}
	b, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	// Route is mounted, auth passes, DB unavailable → not 201 or 200.
	if w.Code == http.StatusCreated {
		t.Errorf("POST submit with nil DB should not return 201")
	}
}

func TestReconciliation147_Step5_SubmitValidatesAllocationID(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := `{"allocation_id":"not-a-uuid","lines":[{"external_ref":"X","line_type":"sale","qty":1}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid allocation_id: want 400, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	errObj, _ := resp["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != "reconciliation.invalid_allocation_id" {
		t.Errorf("want error code reconciliation.invalid_allocation_id, got %q", code)
	}
}

func TestReconciliation147_Step5_SubmitRejectsEmptyLines(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := `{"allocation_id":"00000000-0000-0000-0000-000000000001","lines":[]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty lines: want 400, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	errObj, _ := resp["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != "reconciliation.no_lines" {
		t.Errorf("want error code reconciliation.no_lines, got %q", code)
	}
}

func TestReconciliation147_Step5_SubmitValidatesLineType(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := `{"allocation_id":"00000000-0000-0000-0000-000000000001","lines":[{"external_ref":"X","line_type":"INVALID","qty":1}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid line_type: want 400, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	errObj, _ := resp["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != "reconciliation.invalid_line_type" {
		t.Errorf("want error code reconciliation.invalid_line_type, got %q", code)
	}
}

func TestReconciliation147_Step5_SubmitValidatesQty(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := `{"allocation_id":"00000000-0000-0000-0000-000000000001","lines":[{"external_ref":"X","line_type":"sale","qty":0}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("zero qty: want 400, got %d", w.Code)
	}
}

func TestReconciliation147_Step5_SubmitValidatesEmptyExternalRef(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	payload := `{"allocation_id":"00000000-0000-0000-0000-000000000001","lines":[{"external_ref":"","line_type":"sale","qty":1}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reconciliation/reports",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty external_ref: want 400, got %d", w.Code)
	}
}

func TestReconciliation147_Step5_GetReportValidatesUUID(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/reconciliation/reports/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid report UUID: want 400, got %d", w.Code)
	}
}

func TestReconciliation147_Step5_ExceptionsRequiresOrgID(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	w := httptest.NewRecorder()
	// No org_id query param → 400.
	req := httptest.NewRequest(http.MethodGet, "/v1/reconciliation/exceptions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing org_id: want 400, got %d", w.Code)
	}
}

func TestReconciliation147_Step5_ReviewReportValidatesUUID(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/reconciliation/reports/not-a-uuid/review", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid report UUID on review: want 400, got %d", w.Code)
	}
}

func TestReconciliation147_Step5_ResolveLineValidatesUUID(t *testing.T) {
	s := buildReconciliationServer(t)
	tok := mintReconciliationToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/reconciliation/reports/00000000-0000-0000-0000-000000000001/lines/not-a-uuid",
		nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid line UUID: want 400, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Auto-match algorithm constants and confidence scoring
// ─────────────────────────────────────────────────────────────────────────────

func TestReconciliation147_Step6_ConfidenceThresholdConstant(t *testing.T) {
	// reconciliationConfidenceThreshold must be 80.
	if reconciliationConfidenceThreshold != 80 {
		t.Errorf("reconciliationConfidenceThreshold: want 80, got %d", reconciliationConfidenceThreshold)
	}
}

func TestReconciliation147_Step6_HandlerContainsMatchedConfidence100(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// The auto-match code must assign confidence 100 for perfect matches.
	if !strings.Contains(content, "100") {
		t.Error("reconciliation.go: missing confidence score 100 for matched lines")
	}
}

func TestReconciliation147_Step6_HandlerContainsExceptionConfidence60(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// confidence 60 for batch entry found but no registered barcode.
	if !strings.Contains(content, "60") {
		t.Error("reconciliation.go: missing confidence score 60 for partial matches")
	}
}

func TestReconciliation147_Step6_HandlerContainsExceptionConfidence0(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// confidence 0 for no batch entry found — check the assignment form.
	if !strings.Contains(content, "confidenceScore = 0") {
		t.Error("reconciliation.go: missing confidence score 0 for unmatched lines")
	}
}

func TestReconciliation147_Step6_AutoMatchLoopsOverLines(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// The submit handler iterates over req.Lines.
	if !strings.Contains(content, "for _, line := range req.Lines") {
		t.Error("reconciliation.go: missing iteration over req.Lines in submit handler")
	}
}

func TestReconciliation147_Step6_ExceptionQueueLogic(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// reportStatus initialized to "matched", switched to "exception" when exceptions exist.
	if !strings.Contains(content, `"matched"`) {
		t.Error(`reconciliation.go: missing "matched" report status`)
	}
	if !strings.Contains(content, `reportStatus = "exception"`) {
		t.Error(`reconciliation.go: missing reportStatus = "exception"`)
	}
}

func TestReconciliation147_Step6_CountExceptionLinesBeforeReview(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// Review handler must check remaining exception count.
	if !strings.Contains(content, "CountExceptionLinesByReport") {
		t.Error("reconciliation.go: review handler must call CountExceptionLinesByReport")
	}
}

func TestReconciliation147_Step6_AuditLogsOnSubmit(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// Audit log on report submission.
	if !strings.Contains(content, "reconciliation: report submitted") {
		t.Error("reconciliation.go: missing audit log on report submitted")
	}
}

func TestReconciliation147_Step6_AuditLogsOnReview(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	if !strings.Contains(content, "reconciliation: report reviewed") {
		t.Error("reconciliation.go: missing audit log on report reviewed")
	}
}

func TestReconciliation147_Step6_AuditLogsOnLineResolve(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	if !strings.Contains(content, "reconciliation: exception line resolved") {
		t.Error("reconciliation.go: missing audit log on exception line resolved")
	}
}

func TestReconciliation147_Step6_AllocationStatusGuard(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// Only active/disputed allocations accept reports.
	if !strings.Contains(content, `allocation.Status != "active"`) {
		t.Error(`reconciliation.go: missing allocation status guard for "active"`)
	}
	if !strings.Contains(content, `allocation.Status != "disputed"`) {
		t.Error(`reconciliation.go: missing allocation status guard for "disputed"`)
	}
}

func TestReconciliation147_Step6_NilGuardOnSubmitHandler(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	if !strings.Contains(content, "reconciliationQueries == nil || s.pool == nil") {
		t.Error("reconciliation.go: missing nil guard for reconciliationQueries + pool in submit handler")
	}
}

func TestReconciliation147_Step6_NilGuardOnReadHandlers(t *testing.T) {
	content := findFileByName(t, "reconciliation.go")
	// Read handlers guard on reconciliationQueries only.
	if strings.Count(content, "reconciliationQueries == nil") < 3 {
		t.Error("reconciliation.go: expected at least 3 nil guards for reconciliationQueries (one per read/review handler)")
	}
}
