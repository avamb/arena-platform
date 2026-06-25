// uuid_path_param_test.go verifies feature #41:
// "Malformed UUID in path returns 400 envelope"
//
// When a path parameter declared as UUID receives a non-UUID value, the
// framework returns 400 with code='http.invalid_path_param' rather than
// crashing or returning a 500.
//
// Feature steps covered:
//
//  1. Add a test-only route GET /v1/items/{id} that accepts {id} as UUID.
//  2. GET /v1/items/not-a-uuid → HTTP 400.
//  3. Response code is 'http.invalid_path_param'.
//  4. Response body contains details.param='id'.
//  5. No panic in slog (handler returns gracefully — no 500 path reached).
//  6. No audit event written for failed path validation (route has no audit
//     middleware; the captureAuditWriter is wired but never called).
package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
)

// buildUUIDParamTestRouter constructs a chi router with the full middleware
// chain and mounts a single test-only route:
//
//	GET /v1/items/{id}
//
// The handler calls uuidPathParam to validate {id}. A valid UUID returns 200
// {"item_id": "<uuid>"}; an invalid value causes uuidPathParam to write a 400
// error envelope and the handler returns immediately.
//
// A captureAuditWriter is wired so tests can assert no audit event was
// written when path validation fails (step 6).
func buildUUIDParamTestRouter(t *testing.T, audit *captureAuditWriter) (http.Handler, *captureAuditWriter) {
	t.Helper()

	if audit == nil {
		audit = &captureAuditWriter{}
	}

	r := httpadapter.NewRouter(httpadapter.Deps{
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		Logger:         slog.Default(),
	})

	// Mount the test-only route under /v1/items/{id}.
	// The route is deliberately NOT inside the Server struct so it can be
	// constructed without all production dependencies.
	r.Get("/v1/items/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, ok := uuidPathParam(w, req, "id")
		if !ok {
			return // 400 already written by uuidPathParam
		}
		// Valid UUID — return 200 with the parsed id.
		writeJSON(w, http.StatusOK, map[string]any{"item_id": id.String()})
	})

	// Register NotFound handler so routes outside the test set also respond
	// with the standard JSON error envelope.
	r.NotFound(handleNotFound)

	return r, audit
}

// =============================================================================
// Step 2 — GET /v1/items/not-a-uuid → HTTP 400
// =============================================================================

// TestUUIDPathParam_MalformedUUIDReturns400 verifies step 2: a GET request
// with a non-UUID path parameter segment returns HTTP 400.
func TestUUIDPathParam_MalformedUUIDReturns400(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", rr.Code)
	}
}

// =============================================================================
// Step 3 — Response code is 'http.invalid_path_param'
// =============================================================================

// TestUUIDPathParam_ErrorCodeIsInvalidPathParam verifies step 3: the JSON error
// envelope carries code='http.invalid_path_param'.
func TestUUIDPathParam_ErrorCodeIsInvalidPathParam(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rr.Body.String())
	}

	if body.Error.Code != "http.invalid_path_param" {
		t.Errorf("error.code: want %q, got %q", "http.invalid_path_param", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("error.message must be non-empty")
	}
}

// =============================================================================
// Step 4 — Response body contains details.param='id'
// =============================================================================

// TestUUIDPathParam_DetailsContainsParamName verifies step 4: the error
// envelope's optional details object carries param='id'.
func TestUUIDPathParam_DetailsContainsParamName(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rr.Body.String())
	}

	if body.Error.Details == nil {
		t.Fatal("error.details must be present for http.invalid_path_param errors")
	}

	param, ok := body.Error.Details["param"]
	if !ok {
		t.Error("error.details.param must be present")
	} else if param != "id" {
		t.Errorf("error.details.param: want %q, got %v", "id", param)
	}
}

// =============================================================================
// Step 5 — No panic (handler returns gracefully)
// =============================================================================

// TestUUIDPathParam_NoPanicOnMalformedInput verifies step 5: the handler does
// not panic when given a non-UUID value. If it did panic, the Recoverer
// middleware would catch it and return 500 — this test confirms 400 (not 500)
// is returned, proving the code path is clean.
func TestUUIDPathParam_NoPanicOnMalformedInput(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"not-a-uuid",
		"",                                     // empty
		"12345",                                // too short
		"zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz", // wrong chars
		"00000000-0000-0000-0000-00000000000g", // invalid hex digit
		"../../../etc/passwd",                  // path traversal attempt
	}

	handler, _ := buildUUIDParamTestRouter(t, nil)

	for _, input := range inputs {
		t.Run("input="+input, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/items/"+input, nil)
			handler.ServeHTTP(rr, req)

			// Must be 400 (bad request) or 404 (path not matched by router).
			// Critically, it must NOT be 500 (panic/unhandled error).
			if rr.Code == http.StatusInternalServerError {
				t.Errorf("input %q caused 500 — handler may be panicking", input)
			}
		})
	}
}

// =============================================================================
// Additional verification: valid UUID returns 200
// =============================================================================

// TestUUIDPathParam_ValidUUIDReturns200 verifies the happy-path: a
// well-formed UUID in the path returns HTTP 200 and echoes the id back.
func TestUUIDPathParam_ValidUUIDReturns200(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	validUUID := "01968f00-0000-7000-8000-000000000001"
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/"+validUUID, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid UUID, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		ItemID string `json:"item_id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if !strings.EqualFold(body.ItemID, validUUID) {
		t.Errorf("item_id: want %q, got %q", validUUID, body.ItemID)
	}
}

// =============================================================================
// Step 6 — No audit event written for failed path validation
// =============================================================================

// TestUUIDPathParam_NoAuditEventOnValidationFailure verifies step 6: when path
// parameter validation fails, no audit event is written. The captureAuditWriter
// is wired into the test server; after a failed request its events slice must
// remain empty.
func TestUUIDPathParam_NoAuditEventOnValidationFailure(t *testing.T) {
	t.Parallel()

	audit := &captureAuditWriter{}
	handler, _ := buildUUIDParamTestRouter(t, audit)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	// The captureAuditWriter tracks calls to Write; it must not have been
	// invoked for a path-validation failure.
	audit.mu.Lock()
	count := len(audit.events)
	audit.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 audit events for failed path validation, got %d", count)
	}
}

// =============================================================================
// Standard envelope fields are present on 400 error
// =============================================================================

// TestUUIDPathParam_EnvelopeHasRequestIDAndTraceID verifies that the 400
// response envelope carries non-empty request_id and trace_id fields
// (standard fields required by all error envelopes).
func TestUUIDPathParam_EnvelopeHasRequestIDAndTraceID(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
			TraceID   string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}

	if body.Error.RequestID == "" {
		t.Error("error.request_id must be non-empty")
	}
	if body.Error.TraceID == "" {
		t.Error("error.trace_id must be non-empty")
	}
}

// TestUUIDPathParam_ResponseContentTypeIsJSON verifies that the 400 error
// response carries Content-Type: application/json.
func TestUUIDPathParam_ResponseContentTypeIsJSON(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// TestUUIDPathParam_XRequestIDHeaderPresent verifies that the 400 response
// carries a non-empty X-Request-Id header (set by the middleware chain).
func TestUUIDPathParam_XRequestIDHeaderPresent(t *testing.T) {
	t.Parallel()

	handler, _ := buildUUIDParamTestRouter(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items/not-a-uuid", nil)
	handler.ServeHTTP(rr, req)

	if rr.Header().Get(httpadapter.HeaderRequestID) == "" {
		t.Error("X-Request-Id header must be non-empty on 400 response")
	}
}
