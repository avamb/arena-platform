// not_found_test.go verifies feature #12: requests for unknown paths must
// return the standard JSON error envelope instead of chi's default plain-text
// "404 page not found\n" response.
//
// All five feature steps are covered:
//
//  1. GET /this/does/not/exist → 404 status
//  2. Content-Type: application/json
//  3. Body matches { "error": { "code":"http.not_found", "message":...,
//     "request_id":..., "trace_id":... } }
//  4. X-Request-Id header is set
//  5. Body is NOT chi's default "404 page not found\n"
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
)

// buildNotFoundRouter returns a chi router with the canonical middleware chain
// (so X-Request-Id / X-Trace-Id are written) and handleNotFound registered as
// the NotFound handler — exactly the setup used by Server.mountOperationalRoutes.
//
// At least one real route (/probe) must be registered so that chi builds
// mx.handler (the middleware chain + route table). Without a real route,
// chi's ServeHTTP detects mx.handler == nil and calls NotFoundHandler()
// directly, bypassing the entire middleware chain.
func buildNotFoundRouter() http.Handler {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	// A real route so chi builds mx.handler and the middleware chain wraps
	// all requests, including the NotFound path.
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.NotFound(handleNotFound)
	return r
}

// TestNotFound_StatusIs404 verifies step 1: a request to an unknown path
// returns HTTP 404.
func TestNotFound_StatusIs404(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
}

// TestNotFound_ContentTypeIsJSON verifies step 2: the response carries
// Content-Type: application/json.
func TestNotFound_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type to contain 'application/json', got %q", ct)
	}
}

// TestNotFound_BodyMatchesErrorEnvelope verifies step 3: the response body
// contains the standard error envelope with code "http.not_found".
func TestNotFound_BodyMatchesErrorEnvelope(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			TraceID   string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rr.Body.String())
	}

	if body.Error.Code != "http.not_found" {
		t.Errorf("error.code: want %q, got %q", "http.not_found", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("error.message must be non-empty")
	}
	if body.Error.RequestID == "" {
		t.Error("error.request_id must be non-empty")
	}
	if body.Error.TraceID == "" {
		t.Error("error.trace_id must be non-empty")
	}
}

// TestNotFound_RequestIDHeaderIsSet verifies step 4: the response carries a
// non-empty X-Request-Id header.
func TestNotFound_RequestIDHeaderIsSet(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	if rr.Header().Get(httpadapter.HeaderRequestID) == "" {
		t.Fatal("X-Request-Id header must be non-empty on a 404 response")
	}
}

// TestNotFound_NotChiDefault verifies step 5: the body is NOT chi's built-in
// plain-text "404 page not found\n" response.
func TestNotFound_NotChiDefault(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	body := rr.Body.String()
	chiDefault := "404 page not found\n"
	if body == chiDefault {
		t.Fatalf("response body must not be chi's default plain-text %q", chiDefault)
	}
}

// TestNotFound_RequestIDMatchesBetweenHeaderAndBody verifies that the
// X-Request-Id response header value matches the request_id field inside the
// JSON error envelope — clients can correlate the two without parsing both.
func TestNotFound_RequestIDMatchesBetweenHeaderAndBody(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/this/does/not/exist", nil)
	buildNotFoundRouter().ServeHTTP(rr, req)

	headerID := rr.Header().Get(httpadapter.HeaderRequestID)
	if headerID == "" {
		t.Fatal("X-Request-Id header is empty")
	}

	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}

	if body.Error.RequestID != headerID {
		t.Errorf("error.request_id %q != X-Request-Id header %q", body.Error.RequestID, headerID)
	}
}
