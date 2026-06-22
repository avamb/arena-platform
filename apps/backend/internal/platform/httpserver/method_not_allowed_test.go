// method_not_allowed_test.go verifies feature #13:
// "405 handler returns error envelope with Allow header"
//
// When an HTTP request uses the wrong method on an existing route, the server
// must:
//  1. Return HTTP 405 Method Not Allowed
//  2. Include an Allow header listing the method(s) that ARE supported
//  3. Include the standard JSON error envelope with code "http.method_not_allowed"
//  4. Set Content-Type: application/json
//
// Covered cases:
//   - DELETE /v1/info   (only GET registered)  → 405, Allow includes GET
//   - PATCH  /v1/echo   (only POST registered)  → 405, Allow includes POST
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
)

// buildMethodNotAllowedRouter returns a chi router with:
//   - GET  /probe     — existing route (provides a GET-only target)
//   - POST /post-only — existing route (provides a POST-only target)
//   - handleMethodNotAllowed registered as the MethodNotAllowed handler
//
// The canonical middleware chain is applied so X-Request-Id and X-Trace-Id
// are present in the response context when errorEnvelope reads them.
func buildMethodNotAllowedRouter() http.Handler {
	r := httpadapter.NewRouter(httpadapter.Deps{})
	r.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Post("/post-only", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.NotFound(handleNotFound)
	r.MethodNotAllowed(handleMethodNotAllowed)
	return r
}

// ----------------------------------------------------------------------------
// Feature steps 1–3: DELETE on GET-only /probe → 405, Allow=GET, body has code
// ----------------------------------------------------------------------------

// TestMethodNotAllowed_WrongMethodReturns405 verifies step 1:
// a wrong-method request on an existing path returns HTTP 405.
func TestMethodNotAllowed_WrongMethodReturns405(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rr.Code)
	}
}

// TestMethodNotAllowed_AllowHeaderIncludesGET verifies step 2:
// the Allow response header includes the method that IS registered.
func TestMethodNotAllowed_AllowHeaderIncludesGET(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	allow := rr.Header().Get("Allow")
	if allow == "" {
		t.Fatal("Allow header must be present in a 405 response")
	}
	if !strings.Contains(allow, "GET") {
		t.Errorf("Allow header must include GET for a GET-only route; got %q", allow)
	}
}

// TestMethodNotAllowed_BodyHasMethodNotAllowedCode verifies step 3:
// the response body contains the standard error envelope with
// code = "http.method_not_allowed".
func TestMethodNotAllowed_BodyHasMethodNotAllowedCode(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

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

	if body.Error.Code != "http.method_not_allowed" {
		t.Errorf("error.code: want %q, got %q", "http.method_not_allowed", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("error.message must be non-empty")
	}
}

// TestMethodNotAllowed_ContentTypeIsJSON verifies step 5 (feature step 5):
// Content-Type is application/json.
func TestMethodNotAllowed_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type must contain 'application/json', got %q", ct)
	}
}

// ----------------------------------------------------------------------------
// Step 4: PATCH /post-only → 405, Allow includes POST
// ----------------------------------------------------------------------------

// TestMethodNotAllowed_PatchOnPostOnlyReturns405 verifies that PATCH on a
// POST-only route also returns 405.
func TestMethodNotAllowed_PatchOnPostOnlyReturns405(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/post-only", nil)
	// RequireJSONContentType middleware fires on PATCH; set Content-Type so
	// chi can route the request and respond with 405 rather than 415.
	req.Header.Set("Content-Type", "application/json")
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rr.Code)
	}
}

// TestMethodNotAllowed_AllowHeaderIncludesPOST verifies step 4:
// Allow header includes POST for a POST-only route.
func TestMethodNotAllowed_AllowHeaderIncludesPOST(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/post-only", nil)
	req.Header.Set("Content-Type", "application/json")
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	allow := rr.Header().Get("Allow")
	if allow == "" {
		t.Fatal("Allow header must be present in a 405 response")
	}
	if !strings.Contains(allow, "POST") {
		t.Errorf("Allow header must include POST for a POST-only route; got %q", allow)
	}
}

// TestMethodNotAllowed_PatchBodyHasMethodNotAllowedCode verifies step 4:
// body has code='http.method_not_allowed' for PATCH on POST-only route.
func TestMethodNotAllowed_PatchBodyHasMethodNotAllowedCode(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/post-only", nil)
	req.Header.Set("Content-Type", "application/json")
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rr.Body.String())
	}
	if body.Error.Code != "http.method_not_allowed" {
		t.Errorf("error.code: want %q, got %q", "http.method_not_allowed", body.Error.Code)
	}
}

// ----------------------------------------------------------------------------
// Additional robustness checks
// ----------------------------------------------------------------------------

// TestMethodNotAllowed_NotAffectNotFound ensures that a request to an
// unknown path still gets 404 (not 405), so the two handlers don't interfere.
func TestMethodNotAllowed_NotAffectNotFound(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/unknown-path", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown path should return 404, got %d", rr.Code)
	}
}

// TestMethodNotAllowed_AllowHeaderPreservedAlongside405Body ensures that
// the Allow header and the JSON body are BOTH present — i.e. writeJSON
// does not strip the Allow header when it sets Content-Type.
func TestMethodNotAllowed_AllowHeaderPreservedAlongside405Body(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

	allow := rr.Header().Get("Allow")
	if allow == "" {
		t.Fatal("Allow header must not be stripped by the 405 handler")
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("JSON body must contain an 'error' key")
	}
}

// TestMethodNotAllowed_RequestIDAndTraceIDPresentInBody verifies that the
// error envelope propagates request_id and trace_id from the middleware chain.
func TestMethodNotAllowed_RequestIDAndTraceIDPresentInBody(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/probe", nil)
	buildMethodNotAllowedRouter().ServeHTTP(rr, req)

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
		t.Error("error.request_id must be non-empty on a 405 response")
	}
	if body.Error.TraceID == "" {
		t.Error("error.trace_id must be non-empty on a 405 response")
	}
}
