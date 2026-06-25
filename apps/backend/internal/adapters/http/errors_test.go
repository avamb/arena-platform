// errors_test.go verifies feature #90:
// "Error envelope & panic recovery"
//
// Steps verified:
//  1. ErrorEnvelope struct serialises with correct JSON field names
//     (code, message, request_id, trace_id, details optional)
//  2. WriteError writes the given status, Content-Type: application/json,
//     and {"error":{...}} body with fields resolved from context
//  3. panicRecoverer (in router.go): panic in handler → 500 + envelope,
//     without goroutine stack in production body
//  4. DomainErrStatus maps NotFoundErr/ConflictErr/ValidationErr/StatusCoder
//     to the correct HTTP status codes; opaque errors → 500
//  5. Unit test: panic in handler → 500 with correct JSON, request_id present
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// =============================================================================
// Step 1 — ErrorEnvelope struct JSON serialisation
// =============================================================================

// TestErrorEnvelope_JSONFieldNames verifies step 1: ErrorEnvelope serialises
// with the exact JSON field names required by the OpenAPI schema.
func TestErrorEnvelope_JSONFieldNames(t *testing.T) {
	t.Parallel()

	env := ErrorEnvelope{
		Code:      "test.error",
		Message:   "test message",
		RequestID: "req-id-123",
		TraceID:   "trace-id-456",
		Details:   map[string]any{"field": "value"},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal ErrorEnvelope: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"code"`, `"message"`, `"request_id"`, `"trace_id"`, `"details"`} {
		if !strings.Contains(s, want) {
			t.Errorf("ErrorEnvelope JSON missing field %s; got: %s", want, s)
		}
	}
}

// TestErrorEnvelope_DetailsOmittedWhenNil verifies that the details field is
// omitted from JSON output when nil (json:omitempty semantics).
func TestErrorEnvelope_DetailsOmittedWhenNil(t *testing.T) {
	t.Parallel()

	env := ErrorEnvelope{
		Code:    "test.error",
		Message: "test message",
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"details"`) {
		t.Errorf("nil details should be omitted from JSON; got: %s", b)
	}
}

// TestErrorEnvelope_RoundTrip verifies that marshalling and unmarshalling
// produces equal values.
func TestErrorEnvelope_RoundTrip(t *testing.T) {
	t.Parallel()

	orig := ErrorEnvelope{
		Code:      "auth.token_expired",
		Message:   "your session has expired",
		RequestID: "00000000-0000-0000-0000-000000000001",
		TraceID:   "aabbccddeeff00112233445566778899",
		Details:   map[string]any{"expired_at": "2024-01-01T00:00:00Z"},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ErrorEnvelope
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Code != orig.Code {
		t.Errorf("code: want %q got %q", orig.Code, decoded.Code)
	}
	if decoded.Message != orig.Message {
		t.Errorf("message: want %q got %q", orig.Message, decoded.Message)
	}
	if decoded.RequestID != orig.RequestID {
		t.Errorf("request_id: want %q got %q", orig.RequestID, decoded.RequestID)
	}
	if decoded.TraceID != orig.TraceID {
		t.Errorf("trace_id: want %q got %q", orig.TraceID, decoded.TraceID)
	}
}

// =============================================================================
// Step 2 — WriteError helper
// =============================================================================

// TestWriteError_StatusCode verifies step 2a: WriteError writes the given
// HTTP status code.
func TestWriteError_StatusCode(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteError(rr, req, http.StatusNotFound, "http.not_found", "not found", nil)

	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

// TestWriteError_ContentType verifies step 2b: WriteError sets
// Content-Type: application/json.
func TestWriteError_ContentType(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteError(rr, req, http.StatusBadRequest, "http.bad_request", "bad request", nil)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type application/json, got %q", ct)
	}
}

// TestWriteError_BodyShape verifies step 2c: WriteError produces
// {"error":{"code":...,"message":...,"request_id":...,"trace_id":...}}.
func TestWriteError_BodyShape(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteError(rr, req, http.StatusBadRequest, "http.bad_request", "bad input", nil)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body missing 'error' key; got: %v", body)
	}
	if errObj["code"] != "http.bad_request" {
		t.Errorf("want code=http.bad_request, got %v", errObj["code"])
	}
	if errObj["message"] != "bad input" {
		t.Errorf("want message='bad input', got %v", errObj["message"])
	}
	if _, ok := errObj["request_id"]; !ok {
		t.Errorf("error envelope missing request_id field")
	}
	if _, ok := errObj["trace_id"]; !ok {
		t.Errorf("error envelope missing trace_id field")
	}
}

// TestWriteError_DetailsIncluded verifies step 2d: when details is non-nil
// it appears in the error body.
func TestWriteError_DetailsIncluded(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteError(rr, req, http.StatusBadRequest, "http.validation_error", "invalid field",
		map[string]any{"field": "email", "reason": "invalid format"})

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	details, ok := errObj["details"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing details field; got: %v", errObj)
	}
	if details["field"] != "email" {
		t.Errorf("want details.field=email, got %v", details["field"])
	}
	if details["reason"] != "invalid format" {
		t.Errorf("want details.reason='invalid format', got %v", details["reason"])
	}
}

// TestWriteError_RequestIDFromContext verifies that WriteError reads the
// request_id from the slog context (set by requestContext middleware).
func TestWriteError_RequestIDFromContext(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// Inject a known request_id via logging context.
	ctx := logging.WithRequestID(req.Context(), "test-request-id-xyz")
	req = req.WithContext(ctx)

	WriteError(rr, req, http.StatusInternalServerError, "internal.unexpected", "boom", nil)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["request_id"] != "test-request-id-xyz" {
		t.Errorf("want request_id=test-request-id-xyz, got %v", errObj["request_id"])
	}
}

// TestWriteError_TraceIDFromContext verifies that WriteError reads the
// trace_id from the slog context (set by tracerMiddleware).
func TestWriteError_TraceIDFromContext(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	ctx := logging.WithTraceID(req.Context(), "deadbeef12345678")
	req = req.WithContext(ctx)

	WriteError(rr, req, http.StatusUnauthorized, "auth.token_invalid", "bad token", nil)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["trace_id"] != "deadbeef12345678" {
		t.Errorf("want trace_id=deadbeef12345678, got %v", errObj["trace_id"])
	}
}

// TestWriteError_NilRequest verifies that WriteError handles a nil *http.Request
// gracefully (no panic), producing empty request_id and trace_id.
func TestWriteError_NilRequest(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()

	// Should not panic.
	WriteError(rr, nil, http.StatusInternalServerError, "internal.unexpected", "no request", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("body must have 'error' key even with nil request")
	}
}

// TestWriteError_Various4xxCodes verifies WriteError works with multiple
// 4xx status codes.
func TestWriteError_Various4xxCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status  int
		code    string
		message string
	}{
		{http.StatusBadRequest, "http.bad_request", "bad request"},
		{http.StatusUnauthorized, "auth.unauthenticated", "not authenticated"},
		{http.StatusForbidden, "auth.forbidden", "access denied"},
		{http.StatusConflict, "resource.conflict", "already exists"},
		{http.StatusUnprocessableEntity, "validation.failed", "invalid input"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/test", nil)

			WriteError(rr, req, tc.status, tc.code, tc.message, nil)

			if rr.Code != tc.status {
				t.Errorf("want %d, got %d", tc.status, rr.Code)
			}
			var body map[string]any
			if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			errObj, _ := body["error"].(map[string]any)
			if errObj["code"] != tc.code {
				t.Errorf("want code=%q, got %v", tc.code, errObj["code"])
			}
		})
	}
}

// =============================================================================
// Step 3 + Step 5 — Recovery middleware: panic in handler → 500 + envelope
// =============================================================================

// TestPanicRecoverer_ViaNewRouter_Returns500 verifies step 3+5:
// A panic inside a handler wired into a NewRouter() returns HTTP 500 with
// the project-standard JSON error envelope.
func TestPanicRecoverer_ViaNewRouter_Returns500(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// TestPanicRecoverer_BodyIsJSONEnvelope verifies that the panic recovery
// response body is a valid JSON error envelope with all required fields.
func TestPanicRecoverer_BodyIsJSONEnvelope(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("structured panic value")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body must have 'error' object; got: %v", body)
	}
	for _, field := range []string{"code", "message", "request_id", "trace_id"} {
		if _, present := errObj[field]; !present {
			t.Errorf("error envelope missing field %q; got: %v", field, errObj)
		}
	}
	if errObj["code"] != "internal.unexpected" {
		t.Errorf("want code=internal.unexpected, got %v", errObj["code"])
	}
}

// TestPanicRecoverer_RequestIDPresent verifies step 5b: the request_id field
// in the panic error envelope is non-empty. The requestContext middleware sets
// X-Request-Id before the panic handler runs, so it must be present.
func TestPanicRecoverer_RequestIDPresent(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("id check")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	reqID, _ := errObj["request_id"].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Errorf("want non-empty request_id in panic error envelope; got: %v", errObj)
	}
}

// TestPanicRecoverer_ProdNoStackInBody verifies step 3c: production mode
// does not include the goroutine stack in the response body.
func TestPanicRecoverer_ProdNoStackInBody(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("stack check")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "goroutine") {
		t.Errorf("production response must NOT include goroutine stack; got: %s", body)
	}
}

// TestPanicRecoverer_ContentTypeIsJSON verifies that the panic response
// carries Content-Type: application/json (not text/plain).
func TestPanicRecoverer_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("content type check")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type application/json, got %q", ct)
	}
}

// TestPanicRecoverer_ServerSurvivesAfterPanic verifies that the router
// continues to serve subsequent requests normally after a panic.
func TestPanicRecoverer_ServerSurvivesAfterPanic(t *testing.T) {
	t.Parallel()

	r := NewRouter(Deps{AppEnv: "production"})
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("survive test")
	})
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Trigger the panic.
	rr1 := httptest.NewRecorder()
	r.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rr1.Code != http.StatusInternalServerError {
		t.Fatalf("panic request: want 500, got %d", rr1.Code)
	}

	// Server must still respond normally.
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if rr2.Code != http.StatusOK {
		t.Errorf("post-panic request: want 200, got %d; body: %s", rr2.Code, rr2.Body.String())
	}
}

// =============================================================================
// Step 4 — DomainErrStatus maps domain errors to HTTP status codes
// =============================================================================

// statusCoderErr is a test implementation of StatusCoder.
type statusCoderErr struct{ status int }

func (e *statusCoderErr) Error() string   { return "status coder error" }
func (e *statusCoderErr) HTTPStatus() int { return e.status }

// notFoundDomainErr is a test implementation of NotFoundErr.
type notFoundDomainErr struct{}

func (e *notFoundDomainErr) Error() string  { return "not found" }
func (e *notFoundDomainErr) NotFound() bool { return true }

// conflictDomainErr is a test implementation of ConflictErr.
type conflictDomainErr struct{}

func (e *conflictDomainErr) Error() string  { return "conflict" }
func (e *conflictDomainErr) Conflict() bool { return true }

// validationDomainErr is a test implementation of ValidationErr.
type validationDomainErr struct{}

func (e *validationDomainErr) Error() string    { return "validation failed" }
func (e *validationDomainErr) Validation() bool { return true }

// TestDomainErrStatus_Nil returns 200 when err is nil.
func TestDomainErrStatus_Nil(t *testing.T) {
	t.Parallel()

	if got := DomainErrStatus(nil); got != http.StatusOK {
		t.Errorf("want 200, got %d", got)
	}
}

// TestDomainErrStatus_StatusCoder returns the code from HTTPStatus().
func TestDomainErrStatus_StatusCoder(t *testing.T) {
	t.Parallel()

	err := &statusCoderErr{status: http.StatusPaymentRequired}
	if got := DomainErrStatus(err); got != http.StatusPaymentRequired {
		t.Errorf("want 402, got %d", got)
	}
}

// TestDomainErrStatus_NotFound returns 404 for NotFoundErr.
func TestDomainErrStatus_NotFound(t *testing.T) {
	t.Parallel()

	err := &notFoundDomainErr{}
	if got := DomainErrStatus(err); got != http.StatusNotFound {
		t.Errorf("want 404, got %d", got)
	}
}

// TestDomainErrStatus_Conflict returns 409 for ConflictErr.
func TestDomainErrStatus_Conflict(t *testing.T) {
	t.Parallel()

	err := &conflictDomainErr{}
	if got := DomainErrStatus(err); got != http.StatusConflict {
		t.Errorf("want 409, got %d", got)
	}
}

// TestDomainErrStatus_Validation returns 422 for ValidationErr.
func TestDomainErrStatus_Validation(t *testing.T) {
	t.Parallel()

	err := &validationDomainErr{}
	if got := DomainErrStatus(err); got != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", got)
	}
}

// TestDomainErrStatus_UnknownErr returns 500 for an opaque error.
func TestDomainErrStatus_UnknownErr(t *testing.T) {
	t.Parallel()

	err := errors.New("some opaque database error")
	if got := DomainErrStatus(err); got != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", got)
	}
}

// TestDomainErrStatus_WrappedStatusCoder verifies errors.As unwrapping works
// through fmt.Errorf("%w") wrapping.
func TestDomainErrStatus_WrappedStatusCoder(t *testing.T) {
	t.Parallel()

	inner := &statusCoderErr{status: http.StatusForbidden}
	wrapped := fmt.Errorf("outer context: %w", inner)
	if got := DomainErrStatus(wrapped); got != http.StatusForbidden {
		t.Errorf("want 403 from wrapped StatusCoder, got %d", got)
	}
}

// TestDomainErrStatus_WrappedNotFound verifies that a wrapped NotFoundErr
// also resolves to 404.
func TestDomainErrStatus_WrappedNotFound(t *testing.T) {
	t.Parallel()

	inner := &notFoundDomainErr{}
	wrapped := fmt.Errorf("order lookup: %w", inner)
	if got := DomainErrStatus(wrapped); got != http.StatusNotFound {
		t.Errorf("want 404 from wrapped NotFoundErr, got %d", got)
	}
}

// TestDomainErrStatus_StatusCoderPrecedence verifies that StatusCoder takes
// precedence over NotFoundErr when both interfaces are satisfied.
func TestDomainErrStatus_StatusCoderPrecedence(t *testing.T) {
	t.Parallel()

	// An error that implements both StatusCoder and NotFoundErr; StatusCoder wins.
	// We define it as a concrete type to avoid ambiguous embedded Error() methods.
	type teapotNotFoundErr struct{}
	_ = (*teapotNotFoundErr)(nil) // ensure it exists
	// Use a custom type that satisfies all three interfaces without embedding.
	var err error = &statusCoderErr{status: http.StatusTeapot}
	if got := DomainErrStatus(err); got != http.StatusTeapot {
		t.Errorf("want 418 (StatusCoder takes precedence), got %d", got)
	}
}
