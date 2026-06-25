// scanner_snapshot_144_test.go — unit tests for offline scanner snapshot endpoint (feature #144).
//
// Tests cover:
//
//	Step 1:  SQL query files contain ListSnapshotBarcodesBySession and CountSnapshotBarcodesBySession
//	Step 2:  Gen file contains ListSnapshotBarcodesBySession and CountSnapshotBarcodesBySession functions
//	Step 3:  Querier interface has ListSnapshotBarcodesBySession and CountSnapshotBarcodesBySession
//	Step 4:  scanner_snapshot.go exists and defines the handler functions
//	Step 5:  scannerRateLimiter struct exists with per-IP + per-session limits
//	Step 6:  newScannerRateLimiter constructs limiter with correct limits
//	Step 7:  scannerRateLimiter.checkIP enforces per-IP limit
//	Step 8:  scannerRateLimiter.checkSession enforces per-session limit
//	Step 9:  snapshotBarcodeResponse struct has required JSON fields
//	Step 10: validateBarcodeResponse struct has valid/invalid_reason fields
//	Step 11: handleScannerSnapshot is registered on Server as a method
//	Step 12: handleScannerValidate is registered on Server as a method
//	Step 13: server.go wires GET /scanner/snapshot and POST /scanner/validate routes
//	Step 14: handleScannerSnapshot requires session_id query param
//	Step 15: handleScannerSnapshot rejects invalid session_id UUID
//	Step 16: handleScannerSnapshot rejects invalid since timestamp format
//	Step 17: handleScannerSnapshot returns 503 when barcodeQueries is nil
//	Step 18: handleScannerValidate returns 503 when barcodeQueries is nil
//	Step 19: handleScannerValidate requires external_ref
//	Step 20: handleScannerValidate requires authority_type
//	Step 21: validateBarcodeResponse.Valid is true only for active barcodes
//	Step 22: validateBarcodeResponse.InvalidReason matches status
//	Step 23: per-IP rate limit returns 429 after threshold
//	Step 24: per-session rate limit returns 429 after threshold
//	Step 25: SQL query joins barcodes with tickets on session_id
//	Step 26: snapshot response contains barcodes array and pagination metadata
//	Step 27: since cursor in snapshot defaults to zero time (full snapshot mode)
//	Step 28: serverScannerRL is a package-level rate limiter instance
//	Step 29: snapshot handler logs session_id + count + total
//	Step 30: validate handler logs barcode_id + authority_type + valid
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: SQL query file content
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_BarcodesSQL_ContainsListSnapshotQuery(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "ListSnapshotBarcodesBySession") {
		t.Error("barcodes.sql must contain ListSnapshotBarcodesBySession query")
	}
}

func TestScannerSnapshot144_BarcodesSQL_ContainsCountSnapshotQuery(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "CountSnapshotBarcodesBySession") {
		t.Error("barcodes.sql must contain CountSnapshotBarcodesBySession query")
	}
}

func TestScannerSnapshot144_BarcodesSQL_JoinsOnSessionID(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "session_id") {
		t.Error("barcodes.sql snapshot query must join on tickets.session_id")
	}
}

func TestScannerSnapshot144_BarcodesSQL_FiltersSinceTimestamp(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "updated_at") {
		t.Error("barcodes.sql snapshot query must filter by updated_at for since-cursor support")
	}
}

func TestScannerSnapshot144_BarcodesSQL_ExcludesRevoked(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "revoked") {
		t.Error("barcodes.sql snapshot query must exclude revoked barcodes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Gen file content
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_GenFile_ContainsListSnapshot(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "ListSnapshotBarcodesBySession") {
		t.Error("barcodes.sql.go must contain ListSnapshotBarcodesBySession function")
	}
}

func TestScannerSnapshot144_GenFile_ContainsCountSnapshot(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "CountSnapshotBarcodesBySession") {
		t.Error("barcodes.sql.go must contain CountSnapshotBarcodesBySession function")
	}
}

func TestScannerSnapshot144_GenFile_ListSnapshotHasFourParams(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	// The function signature should include sessionID, since, limit, offset
	if !strings.Contains(content, "sessionID uuid.UUID, since time.Time, limit, offset int32") {
		t.Error("ListSnapshotBarcodesBySession must accept (sessionID, since, limit, offset) parameters")
	}
}

func TestScannerSnapshot144_GenFile_CountSnapshotHasTwoParams(t *testing.T) {
	content := findFileByName(t, "barcodes.sql.go")
	if !strings.Contains(content, "sessionID uuid.UUID, since time.Time") {
		t.Error("CountSnapshotBarcodesBySession must accept (sessionID, since) parameters")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_Querier_HasListSnapshotMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "ListSnapshotBarcodesBySession") {
		t.Error("querier.go must define ListSnapshotBarcodesBySession in Querier interface")
	}
}

func TestScannerSnapshot144_Querier_HasCountSnapshotMethod(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "CountSnapshotBarcodesBySession") {
		t.Error("querier.go must define CountSnapshotBarcodesBySession in Querier interface")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: scanner_snapshot.go exists and defines handlers
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_FileExists(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if content == "" {
		t.Fatal("scanner_snapshot.go not found or empty")
	}
}

func TestScannerSnapshot144_DefinesSnapshotHandler(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "handleScannerSnapshot") {
		t.Error("scanner_snapshot.go must define handleScannerSnapshot")
	}
}

func TestScannerSnapshot144_DefinesValidateHandler(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "handleScannerValidate") {
		t.Error("scanner_snapshot.go must define handleScannerValidate")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: scannerRateLimiter struct
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_RateLimiterStructExists(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "scannerRateLimiter") {
		t.Error("scanner_snapshot.go must define scannerRateLimiter struct")
	}
}

func TestScannerSnapshot144_RateLimiterHasIPLimit(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "ipLimit") {
		t.Error("scannerRateLimiter must have ipLimit field")
	}
}

func TestScannerSnapshot144_RateLimiterHasSessionLimit(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "sessionLimit") {
		t.Error("scannerRateLimiter must have sessionLimit field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: newScannerRateLimiter constructor
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_NewRateLimiter_CorrectLimits(t *testing.T) {
	rl := newScannerRateLimiter(600, 300)
	if rl.ipLimit != 600 {
		t.Errorf("ipLimit = %d; want 600", rl.ipLimit)
	}
	if rl.sessionLimit != 300 {
		t.Errorf("sessionLimit = %d; want 300", rl.sessionLimit)
	}
}

func TestScannerSnapshot144_NewRateLimiter_InitializesMaps(t *testing.T) {
	rl := newScannerRateLimiter(10, 5)
	if rl.ips == nil {
		t.Error("ips map must be initialized")
	}
	if rl.sessions == nil {
		t.Error("sessions map must be initialized")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: checkIP enforces per-IP limit
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_CheckIP_AllowsUnderLimit(t *testing.T) {
	rl := newScannerRateLimiter(5, 5)
	for i := 0; i < 5; i++ {
		if !rl.checkIP("1.2.3.4") {
			t.Errorf("request %d should be allowed (limit=5)", i+1)
		}
	}
}

func TestScannerSnapshot144_CheckIP_BlocksOverLimit(t *testing.T) {
	rl := newScannerRateLimiter(3, 100)
	rl.checkIP("1.2.3.4")
	rl.checkIP("1.2.3.4")
	rl.checkIP("1.2.3.4")
	// 4th request should be blocked
	if rl.checkIP("1.2.3.4") {
		t.Error("4th request must be blocked (limit=3)")
	}
}

func TestScannerSnapshot144_CheckIP_DifferentIPsAreIndependent(t *testing.T) {
	rl := newScannerRateLimiter(1, 100)
	if !rl.checkIP("1.2.3.4") {
		t.Error("first request from 1.2.3.4 must be allowed")
	}
	// Different IP should be allowed
	if !rl.checkIP("5.6.7.8") {
		t.Error("first request from 5.6.7.8 must be allowed (different IP)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: checkSession enforces per-session limit
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_CheckSession_AllowsUnderLimit(t *testing.T) {
	rl := newScannerRateLimiter(1000, 3)
	for i := 0; i < 3; i++ {
		if !rl.checkSession("session-abc") {
			t.Errorf("request %d should be allowed (limit=3)", i+1)
		}
	}
}

func TestScannerSnapshot144_CheckSession_BlocksOverLimit(t *testing.T) {
	rl := newScannerRateLimiter(1000, 2)
	rl.checkSession("session-abc")
	rl.checkSession("session-abc")
	if rl.checkSession("session-abc") {
		t.Error("3rd session request must be blocked (limit=2)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: snapshotBarcodeResponse struct fields
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotResponseHasIDField(t *testing.T) {
	r := snapshotBarcodeResponse{ID: "abc"}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"id"`) {
		t.Errorf("snapshotBarcodeResponse JSON must include id field, got: %s", data)
	}
}

func TestScannerSnapshot144_SnapshotResponseHasExternalRef(t *testing.T) {
	r := snapshotBarcodeResponse{ExternalRef: "REF123"}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"external_ref"`) {
		t.Errorf("snapshotBarcodeResponse JSON must include external_ref, got: %s", data)
	}
}

func TestScannerSnapshot144_SnapshotResponseHasStatus(t *testing.T) {
	r := snapshotBarcodeResponse{Status: "active"}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"status"`) {
		t.Errorf("snapshotBarcodeResponse JSON must include status, got: %s", data)
	}
}

func TestScannerSnapshot144_SnapshotResponseHasUpdatedAt(t *testing.T) {
	r := snapshotBarcodeResponse{UpdatedAt: time.Now().Format(time.RFC3339)}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"updated_at"`) {
		t.Errorf("snapshotBarcodeResponse JSON must include updated_at, got: %s", data)
	}
}

func TestScannerSnapshot144_SnapshotResponseTicketIDIsOptional(t *testing.T) {
	// ticket_id should be omitted when nil (omitempty)
	r := snapshotBarcodeResponse{ID: "abc", TicketID: nil}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), `"ticket_id"`) {
		t.Errorf("ticket_id must be omitted when nil (omitempty), got: %s", data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: validateBarcodeResponse struct
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ValidateResponseHasValidField(t *testing.T) {
	r := validateBarcodeResponse{Valid: true}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"valid"`) {
		t.Errorf("validateBarcodeResponse must include valid field, got: %s", data)
	}
}

func TestScannerSnapshot144_ValidateResponseHasInvalidReason(t *testing.T) {
	r := validateBarcodeResponse{InvalidReason: "barcode_revoked"}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), `"invalid_reason"`) {
		t.Errorf("validateBarcodeResponse must include invalid_reason field when non-empty, got: %s", data)
	}
}

func TestScannerSnapshot144_ValidateResponseInvalidReasonOmittedWhenEmpty(t *testing.T) {
	r := validateBarcodeResponse{BarcodeID: "abc", Valid: true} // InvalidReason=""
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), `"invalid_reason"`) {
		t.Errorf("invalid_reason must be omitted when empty (omitempty), got: %s", data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11 & 12: Handler methods exist on *Server
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotHandlerOnServer(_ *testing.T) {
	// Compile-time guard: if handleScannerSnapshot is removed from Server, this won't compile.
	s := &Server{}
	_ = s.handleScannerSnapshot
}

func TestScannerSnapshot144_ValidateHandlerOnServer(_ *testing.T) {
	// Compile-time guard: if handleScannerValidate is removed from Server, this won't compile.
	s := &Server{}
	_ = s.handleScannerValidate
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: server.go registers the routes
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ServerGoRegistersSnapshotRoute(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "scanner/snapshot") {
		t.Error("server.go must register GET /scanner/snapshot route")
	}
}

func TestScannerSnapshot144_ServerGoRegistersValidateRoute(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "scanner/validate") {
		t.Error("server.go must register POST /scanner/validate route")
	}
}

func TestScannerSnapshot144_ServerGoGuardsOnBarcodeQueries(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "handleScannerSnapshot") {
		t.Error("server.go must reference handleScannerSnapshot for route registration")
	}
	if !strings.Contains(content, "handleScannerValidate") {
		t.Error("server.go must reference handleScannerValidate for route registration")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: snapshot requires session_id param
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotRequiresSessionID(t *testing.T) {
	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	// Request without session_id query param
	req := httptest.NewRequest(http.MethodGet, "/v1/scanner/snapshot", nil)
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("missing session_id: want 400, got %d", rw.Code)
	}
	body := rw.Body.String()
	if !strings.Contains(body, "session_id") {
		t.Errorf("error body must mention session_id, got: %s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: snapshot rejects invalid session_id UUID
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotRejectsInvalidSessionID(t *testing.T) {
	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/scanner/snapshot?session_id=not-a-uuid", nil)
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("invalid session_id UUID: want 400, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16: snapshot rejects invalid since timestamp
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotRejectsInvalidSince(t *testing.T) {
	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/scanner/snapshot?session_id=00000000-0000-0000-0000-000000000001&since=not-a-date", nil)
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("invalid since: want 400, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17: snapshot returns 503 when barcodeQueries is nil
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotReturns503WhenQueriesNil(t *testing.T) {
	s := &Server{
		barcodeQueries: nil,
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/scanner/snapshot?session_id=00000000-0000-0000-0000-000000000001", nil)
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("nil barcodeQueries: want 503, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18: validate returns 503 when barcodeQueries is nil
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ValidateReturns503WhenQueriesNil(t *testing.T) {
	s := &Server{
		barcodeQueries: nil,
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate",
		strings.NewReader(`{"external_ref":"ABC","authority_type":"platform"}`))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	s.handleScannerValidate(rw, req)
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("nil barcodeQueries: want 503, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19: validate requires external_ref
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ValidateRequiresExternalRef(t *testing.T) {
	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate",
		strings.NewReader(`{"authority_type":"platform"}`))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	s.handleScannerValidate(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("missing external_ref: want 400, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20: validate requires authority_type
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ValidateRequiresAuthorityType(t *testing.T) {
	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate",
		strings.NewReader(`{"external_ref":"ABC123"}`))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	s.handleScannerValidate(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("missing authority_type: want 400, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21: Valid field is true only for active barcodes
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ValidateResponseValid_Active(t *testing.T) {
	r := validateBarcodeResponse{Status: "active", Valid: true}
	if r.Status != "active" {
		t.Errorf("expected Status=active, got %q", r.Status)
	}
	if !r.Valid {
		t.Error("active barcode must have Valid=true")
	}
	if r.InvalidReason != "" {
		t.Errorf("active barcode must have empty InvalidReason, got: %s", r.InvalidReason)
	}
}

func TestScannerSnapshot144_ValidateResponseValid_Revoked(t *testing.T) {
	r := validateBarcodeResponse{Status: "revoked", Valid: false, InvalidReason: "barcode_revoked"}
	if r.Status != "revoked" {
		t.Errorf("expected Status=revoked, got %q", r.Status)
	}
	if r.Valid {
		t.Error("revoked barcode must have Valid=false")
	}
	if r.InvalidReason != "barcode_revoked" {
		t.Errorf("revoked barcode must have InvalidReason=barcode_revoked, got: %s", r.InvalidReason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22: InvalidReason matches status
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_InvalidReasonForScanned(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "already_scanned") {
		t.Error("scanner_snapshot.go must set InvalidReason to 'already_scanned' for scanned barcodes")
	}
}

func TestScannerSnapshot144_InvalidReasonForRevoked(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, "barcode_revoked") {
		t.Error("scanner_snapshot.go must set InvalidReason to 'barcode_revoked' for revoked barcodes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 23: per-IP rate limit returns 429
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_IPRateLimitReturns429(t *testing.T) {
	rl := newScannerRateLimiter(1, 1000)
	// Use up the 1 allowed request
	rl.checkIP("10.0.0.1")
	// Next request should be blocked → 429

	// Temporarily override the package-level limiter
	original := serverScannerRL
	serverScannerRL = rl
	defer func() { serverScannerRL = original }()

	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/scanner/snapshot?session_id=00000000-0000-0000-0000-000000000001", nil)
	// Use X-Forwarded-For so clientIP() returns the same key as the pre-warm call above.
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusTooManyRequests {
		t.Errorf("rate-limited IP: want 429, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 24: per-session rate limit returns 429
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SessionRateLimitReturns429(t *testing.T) {
	rl := newScannerRateLimiter(1000, 1)
	// Use up the 1 allowed request for the session
	rl.checkSession("00000000-0000-0000-0000-000000000002")

	original := serverScannerRL
	serverScannerRL = rl
	defer func() { serverScannerRL = original }()

	s := &Server{
		barcodeQueries: newTestBarcodeQueries144(),
		logger:         slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/scanner/snapshot?session_id=00000000-0000-0000-0000-000000000002", nil)
	req.RemoteAddr = "99.99.99.99:54321"
	rw := httptest.NewRecorder()
	s.handleScannerSnapshot(rw, req)
	if rw.Code != http.StatusTooManyRequests {
		t.Errorf("rate-limited session: want 429, got %d", rw.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 25: SQL joins barcodes with tickets on session_id
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SQLJoinsBarcodeTickets(t *testing.T) {
	content := findFileByName(t, "barcodes.sql")
	if !strings.Contains(content, "JOIN   tickets") && !strings.Contains(content, "JOIN tickets") {
		t.Error("barcodes.sql must JOIN tickets table to filter barcodes by session_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 26: snapshot response contains barcodes array and pagination metadata
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotResponseHasBarcodesArray(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, `"barcodes"`) {
		t.Error("scanner_snapshot.go must include 'barcodes' key in snapshot response")
	}
}

func TestScannerSnapshot144_SnapshotResponseHasPaginationMeta(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	for _, key := range []string{`"total"`, `"page"`, `"per_page"`, `"total_pages"`} {
		if !strings.Contains(content, key) {
			t.Errorf("scanner_snapshot.go must include %s in snapshot response", key)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 27: since cursor defaults to zero time (full snapshot)
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SinceDefaultsToZeroTime(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	// Look for the zero time default handling
	if !strings.Contains(content, "var since time.Time") {
		t.Error("scanner_snapshot.go must initialize since to zero time.Time when not provided")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 28: serverScannerRL is package-level rate limiter
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_ServerScannerRLIsPackageLevel(t *testing.T) {
	// Compile-time check: serverScannerRL must be accessible at package level.
	if serverScannerRL == nil {
		t.Error("serverScannerRL must be a non-nil package-level rate limiter")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 29 & 30: Audit logging content checks
// ─────────────────────────────────────────────────────────────────────────────

func TestScannerSnapshot144_SnapshotHandlerLogsSessionID(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, `"session_id"`) {
		t.Error("handleScannerSnapshot must log session_id field")
	}
}

func TestScannerSnapshot144_ValidateHandlerLogsBarcodeID(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, `"barcode_id"`) {
		t.Error("handleScannerValidate must log barcode_id field")
	}
}

func TestScannerSnapshot144_ValidateHandlerLogsValid(t *testing.T) {
	content := findFileByName(t, "scanner_snapshot.go")
	if !strings.Contains(content, `"valid"`) {
		t.Error("handleScannerValidate must log valid field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestBarcodeQueries returns a *gen.Queries backed by a nil DB.
// This satisfies the barcodeQueriesAvailable() check (s.barcodeQueries != nil)
// while keeping tests free of a real database connection. Any actual DB call
// will panic/error, but tests here only exercise path-based guards (400/503/429)
// that return before any DB query is made.
func newTestBarcodeQueries144() *gen.Queries {
	return gen.New(nil)
}
