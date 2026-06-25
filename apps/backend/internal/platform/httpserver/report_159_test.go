// report_159_test.go — unit tests for feature #159 (Post-event report generation).
//
// Test coverage:
//
//	Step 1: Migration file 0032_event_reports.sql — tables, columns, state enum,
//	        RBAC seeds, Down section.
//	Step 2: Worker handler — JobType constant, Payload struct, NewHandler.
//	Step 3: SQL query file — all named queries present.
//	Step 4: Gen file — EventReportRow, EventReportLineRow, EventReportAggRow,
//	        all 10 query functions.
//	Step 5: Querier interface — all 10 event report methods present.
//	Step 6: HTTP routes — auth-gating, server wiring, response structure.
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
// Server factory for report route tests
// ─────────────────────────────────────────────────────────────────────────────

const reportTestActorID = "00000000-0000-0000-0000-000000000159"

// buildReportServer builds a Server with stub auth and report routes mounted.
// Uses dbDownPool so real DB operations never execute.
func buildReportServer(t *testing.T) *Server {
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
		t.Fatalf("buildReportServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:        cfg,
		Auth:          stub,
		Pool:          &dbDownPool{},
		ReportQueries: gen.New(nil),
	})
}

// mintReportToken mints a dev JWT with admin role for report route tests.
func mintReportToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + reportTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintReportToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintReportToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintReportToken: no token in response: %v", resp)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if content == "" {
		t.Fatal("0032_event_reports.sql is empty")
	}
}

func TestReport159_MigrationHasGooseUpDown(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("missing '-- +goose Down' directive")
	}
}

func TestReport159_MigrationHasEventReportsTable(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "CREATE TABLE event_reports") {
		t.Error("missing CREATE TABLE event_reports")
	}
}

func TestReport159_MigrationHasEventReportLinesTable(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "CREATE TABLE event_report_lines") {
		t.Error("missing CREATE TABLE event_report_lines")
	}
}

func TestReport159_MigrationEventReportsStateEnum(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	for _, state := range []string{"pending", "generating", "ready", "failed"} {
		if !strings.Contains(content, "'"+state+"'") {
			t.Errorf("missing state %q in state CHECK constraint", state)
		}
	}
}

func TestReport159_MigrationEventReportLinesCategories(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	for _, cat := range []string{"sales", "refunds", "complimentary", "scans", "commissions", "payouts"} {
		if !strings.Contains(content, "'"+cat+"'") {
			t.Errorf("missing category %q in category CHECK constraint", cat)
		}
	}
}

func TestReport159_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "event_reports_event_id") {
		t.Error("missing index event_reports_event_id")
	}
	if !strings.Contains(content, "event_reports_state_idx") {
		t.Error("missing index event_reports_state_idx")
	}
	if !strings.Contains(content, "event_report_lines_report_id") {
		t.Error("missing index event_report_lines_report_id")
	}
}

func TestReport159_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "report.read") {
		t.Error("missing permission 'report.read'")
	}
	if !strings.Contains(content, "report.generate") {
		t.Error("missing permission 'report.generate'")
	}
}

func TestReport159_MigrationDownDropsTables(t *testing.T) {
	content := findFileByName(t, "0032_event_reports.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS event_report_lines") {
		t.Error("Down section missing DROP TABLE event_report_lines")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS event_reports") {
		t.Error("Down section missing DROP TABLE event_reports")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Worker handler
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_WorkerHandlerFile(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	if content == "" {
		t.Fatal("reporting/handler.go is empty")
	}
}

func TestReport159_WorkerJobTypeConstant(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	if !strings.Contains(content, `JobType = "event.generate_report"`) {
		t.Error("reporting handler missing JobType = \"event.generate_report\"")
	}
}

func TestReport159_WorkerPayloadStruct(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	if !strings.Contains(content, "type Payload struct") {
		t.Error("reporting handler missing type Payload struct")
	}
	for _, field := range []string{"EventID", "OrgID", "ReportID", "CutoffTime"} {
		if !strings.Contains(content, field) {
			t.Errorf("Payload struct missing field %q", field)
		}
	}
}

func TestReport159_WorkerNewHandlerFunction(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	if !strings.Contains(content, "func NewHandler(") {
		t.Error("reporting handler missing func NewHandler(")
	}
	if !strings.Contains(content, "worker.HandlerFunc") {
		t.Error("reporting handler NewHandler does not return worker.HandlerFunc")
	}
}

func TestReport159_WorkerOutboxEvent(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	if !strings.Contains(content, "v1.report.generated") {
		t.Error("reporting handler missing outbox event type 'v1.report.generated'")
	}
}

func TestReport159_WorkerStateTransitions(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	for _, state := range []string{"generating", "ready", "failed"} {
		if !strings.Contains(content, `"`+state+`"`) {
			t.Errorf("reporting handler missing state transition to %q", state)
		}
	}
}

func TestReport159_WorkerAllSixCategories(t *testing.T) {
	content := findFileByName(t, "reporting_handler.go")
	for _, cat := range []string{"sales", "refunds", "complimentary", "scans", "commissions", "payouts"} {
		if !strings.Contains(content, `"`+cat+`"`) {
			t.Errorf("reporting handler missing category %q", cat)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: SQL query file
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_SQLFileExists(t *testing.T) {
	content := findFileByName(t, "event_reports.sql")
	if content == "" {
		t.Fatal("event_reports.sql is empty")
	}
}

func TestReport159_SQLFileNamedQueries(t *testing.T) {
	content := findFileByName(t, "event_reports.sql")
	for _, qName := range []string{
		"InsertEventReport",
		"GetEventReportByID",
		"GetEventReportByEventID",
		"UpdateEventReportState",
		"InsertEventReportLine",
		"ListEventReportLinesByReport",
		"AggregateSalesForEvent",
		"AggregateComplimentaryForEvent",
		"AggregateRefundsForEvent",
		"AggregateScansForEvent",
	} {
		if !strings.Contains(content, "-- name: "+qName) {
			t.Errorf("event_reports.sql missing named query %q", qName)
		}
	}
}

func TestReport159_SQLAggregationJoinsTables(t *testing.T) {
	content := findFileByName(t, "event_reports.sql")
	for _, table := range []string{"tickets", "sessions", "checkout_sessions", "refunds", "barcodes"} {
		if !strings.Contains(content, table) {
			t.Errorf("aggregation queries do not reference table %q", table)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_GenFileExists(t *testing.T) {
	content := findFileByName(t, "event_reports.sql.go")
	if content == "" {
		t.Fatal("event_reports.sql.go is empty")
	}
}

func TestReport159_GenFileEventReportRow(t *testing.T) {
	content := findFileByName(t, "event_reports.sql.go")
	if !strings.Contains(content, "type EventReportRow struct") {
		t.Error("event_reports.sql.go missing type EventReportRow struct")
	}
	for _, field := range []string{"ID", "EventID", "OrgID", "State", "GeneratedAt", "ErrorMsg"} {
		if !strings.Contains(content, field) {
			t.Errorf("EventReportRow missing field %q", field)
		}
	}
}

func TestReport159_GenFileEventReportLineRow(t *testing.T) {
	content := findFileByName(t, "event_reports.sql.go")
	if !strings.Contains(content, "type EventReportLineRow struct") {
		t.Error("event_reports.sql.go missing type EventReportLineRow struct")
	}
	for _, field := range []string{"ReportID", "Category", "Quantity", "GrossAmount", "NetAmount", "Currency"} {
		if !strings.Contains(content, field) {
			t.Errorf("EventReportLineRow missing field %q", field)
		}
	}
}

func TestReport159_GenFileEventReportAggRow(t *testing.T) {
	content := findFileByName(t, "event_reports.sql.go")
	if !strings.Contains(content, "type EventReportAggRow struct") {
		t.Error("event_reports.sql.go missing type EventReportAggRow struct")
	}
}

func TestReport159_GenFileAllFunctions(t *testing.T) {
	content := findFileByName(t, "event_reports.sql.go")
	for _, fn := range []string{
		"InsertEventReport",
		"GetEventReportByID",
		"GetEventReportByEventID",
		"UpdateEventReportState",
		"InsertEventReportLine",
		"ListEventReportLinesByReport",
		"AggregateSalesForEvent",
		"AggregateComplimentaryForEvent",
		"AggregateRefundsForEvent",
		"AggregateScansForEvent",
	} {
		if !strings.Contains(content, "func (q *Queries) "+fn+"(") {
			t.Errorf("event_reports.sql.go missing func (q *Queries) %s(", fn)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_QuerierInterfaceMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertEventReport",
		"GetEventReportByID",
		"GetEventReportByEventID",
		"UpdateEventReportState",
		"InsertEventReportLine",
		"ListEventReportLinesByReport",
		"AggregateSalesForEvent",
		"AggregateComplimentaryForEvent",
		"AggregateRefundsForEvent",
		"AggregateScansForEvent",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go missing method %q", method)
		}
	}
}

func TestReport159_QuerierInterfaceReturnTypes(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "EventReportRow") {
		t.Error("querier.go missing return type EventReportRow")
	}
	if !strings.Contains(content, "EventReportLineRow") {
		t.Error("querier.go missing return type EventReportLineRow")
	}
	if !strings.Contains(content, "EventReportAggRow") {
		t.Error("querier.go missing return type EventReportAggRow")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: HTTP routes
// ─────────────────────────────────────────────────────────────────────────────

func TestReport159_GetReportRequiresAuth(t *testing.T) {
	s := buildReportServer(t)
	eventID := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodGet, "/v1/events/"+eventID+"/report", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/events/{id}/report without auth: got %d, want 401", w.Code)
	}
}

func TestReport159_PostReportRequiresAuth(t *testing.T) {
	s := buildReportServer(t)
	eventID := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodPost, "/v1/events/"+eventID+"/report", nil)
	// Content-Type required so the body-limit/content-type middleware passes through
	// to the auth layer. Without it the router returns 415 before auth is checked.
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/events/{id}/report without auth: got %d, want 401", w.Code)
	}
}

func TestReport159_GetReportWithAuthDoesNotReturn401(t *testing.T) {
	// With a nil DB pool (gen.New(nil)), the query call panics/fails gracefully.
	// The endpoint should return 404, 500, or 503 — but NOT 401 (auth passed).
	s := buildReportServer(t)
	tok := mintReportToken(t, s)
	eventID := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodGet, "/v1/events/"+eventID+"/report", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET report with valid auth: got 401 (auth failed unexpectedly)")
	}
}

func TestReport159_GetReportInvalidUUID(t *testing.T) {
	s := buildReportServer(t)
	tok := mintReportToken(t, s)
	req := httptest.NewRequest(http.MethodGet, "/v1/events/not-a-uuid/report", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET /v1/events/not-a-uuid/report: got %d, want 400", w.Code)
	}
}

func TestReport159_ServerHasReportQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "reportQueries") {
		t.Error("server.go missing field reportQueries")
	}
}

func TestReport159_ServerOptionsHasReportQueries(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "ReportQueries") {
		t.Error("server.go Options missing field ReportQueries")
	}
}

func TestReport159_EventReportsGoFileExists(t *testing.T) {
	content := findFileByName(t, "event_reports.go")
	if content == "" {
		t.Fatal("event_reports.go is empty")
	}
}

func TestReport159_EventReportsHandlersPresent(t *testing.T) {
	content := findFileByName(t, "event_reports.go")
	if !strings.Contains(content, "handleGetEventReport") {
		t.Error("event_reports.go missing handleGetEventReport")
	}
	if !strings.Contains(content, "handleTriggerEventReport") {
		t.Error("event_reports.go missing handleTriggerEventReport")
	}
}

func TestReport159_EventReportResponseTypes(t *testing.T) {
	content := findFileByName(t, "event_reports.go")
	if !strings.Contains(content, "eventReportResponse") {
		t.Error("event_reports.go missing eventReportResponse type")
	}
	if !strings.Contains(content, "eventReportLineResponse") {
		t.Error("event_reports.go missing eventReportLineResponse type")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time checks for gen types
// ─────────────────────────────────────────────────────────────────────────────

// TestReport159_GenTypesCompileTime verifies that the gen types referenced by
// the reporting package are accessible and have the expected shape.
func TestReport159_GenTypesCompileTime(_ *testing.T) {
	// EventReportRow field existence — compile error if fields are missing.
	var r gen.EventReportRow
	_ = r.ID
	_ = r.EventID
	_ = r.OrgID
	_ = r.State
	_ = r.GeneratedAt
	_ = r.ErrorMsg
	_ = r.CreatedAt
	_ = r.UpdatedAt

	// EventReportLineRow field existence.
	var l gen.EventReportLineRow
	_ = l.ID
	_ = l.ReportID
	_ = l.Category
	_ = l.Quantity
	_ = l.GrossAmount
	_ = l.NetAmount
	_ = l.Currency

	// EventReportAggRow field existence.
	var a gen.EventReportAggRow
	_ = a.Quantity
	_ = a.GrossAmount
	_ = a.NetAmount
	_ = a.Currency
}

// TestReport159_BuildEventReportResponse verifies the response builder produces
// the expected shape without a live database.
func TestReport159_BuildEventReportResponse(t *testing.T) {
	now := time.Now().UTC()
	report := gen.EventReportRow{
		State:     "ready",
		CreatedAt: now,
		UpdatedAt: now,
	}
	lines := []gen.EventReportLineRow{
		{Category: "sales", Quantity: 10, GrossAmount: 10000, NetAmount: 9500, Currency: "usd", CreatedAt: now},
		{Category: "refunds", Quantity: 1, GrossAmount: 500, NetAmount: 500, Currency: "usd", CreatedAt: now},
	}
	resp := buildEventReportResponse(report, lines)
	if resp.State != "ready" {
		t.Errorf("state: got %q, want %q", resp.State, "ready")
	}
	if len(resp.Lines) != 2 {
		t.Errorf("lines count: got %d, want 2", len(resp.Lines))
	}
	if resp.Lines[0].Category != "sales" {
		t.Errorf("lines[0].category: got %q, want sales", resp.Lines[0].Category)
	}
	if resp.Lines[0].GrossAmount != 10000 {
		t.Errorf("lines[0].gross_amount: got %d, want 10000", resp.Lines[0].GrossAmount)
	}
}
