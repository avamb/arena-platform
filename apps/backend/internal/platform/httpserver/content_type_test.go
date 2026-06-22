// content_type_test.go — integration tests for Feature #30
// "Missing Content-Type returns 415 envelope"
//
// These tests exercise the RequireJSONContentType middleware wired globally in
// NewRouter (adapters/http/router.go). All requests go through the full
// middleware chain so X-Request-Id and X-Trace-Id headers are already set when
// the 415 response is built.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// step 1: POST /v1/echo with no Content-Type header and valid JSON body → 415
func TestContentType_NoContentType_Returns415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	body := `{"message":"hello"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// No Content-Type header set deliberately.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("got status %d, want %d (415)", resp.StatusCode, http.StatusUnsupportedMediaType)
	}
}

// step 2: error code in body is 'http.unsupported_media_type'
func TestContentType_NoContentType_ErrorCode(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no Content-Type.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'error' object in body, got %v", envelope)
	}
	code, _ := errObj["code"].(string)
	if code != "http.unsupported_media_type" {
		t.Errorf("got code %q, want %q", code, "http.unsupported_media_type")
	}
}

// step 3: Accept-Post: application/json header present in 415 response
func TestContentType_NoContentType_AcceptPostHeader(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	acceptPost := resp.Header.Get("Accept-Post")
	if acceptPost == "" {
		t.Error("expected Accept-Post response header, got empty string")
	}
	if !strings.Contains(strings.ToLower(acceptPost), "application/json") {
		t.Errorf("Accept-Post %q does not mention application/json", acceptPost)
	}
}

// step 3 (also): 415 response body is a valid JSON envelope with 'error' key
func TestContentType_NoContentType_ResponseIsJSONEnvelope(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type is %q, want application/json prefix", ct)
	}
	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := envelope["error"]; !ok {
		t.Error("expected 'error' key in JSON envelope, not found")
	}
}

// step 4: POST with Content-Type: text/plain → expect 415
func TestContentType_TextPlain_Returns415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain got status %d, want 415", resp.StatusCode)
	}
}

// step 4 variant: application/x-www-form-urlencoded → 415
func TestContentType_FormData_Returns415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader("message=hello"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form-urlencoded got status %d, want 415", resp.StatusCode)
	}
}

// step 5: POST with Content-Type: application/json; charset=utf-8 → NOT 415
// (the request may fail for other reasons — auth, body validation — but not 415)
func TestContentType_JsonWithCharset_NotRejectedWith415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnsupportedMediaType {
		t.Error("application/json; charset=utf-8 should be accepted, got 415")
	}
}

// step 6: POST with Content-Type: application/JSON (case-insensitive) → accepted
func TestContentType_JsonCaseInsensitive_NotRejectedWith415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/JSON")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnsupportedMediaType {
		t.Error("application/JSON (uppercase) should be accepted, got 415")
	}
}

// --- GET/DELETE are not subject to the content-type check ---

func TestContentType_GET_NotAffected(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// GET /healthz — no Content-Type requirement.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnsupportedMediaType {
		t.Error("GET /healthz should never return 415 regardless of Content-Type")
	}
}

// --- PUT/PATCH follow the same rule as POST ---

func TestContentType_PUT_NoContentType_Returns415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// Non-existent PUT route: the middleware fires before routing → 415 before 404.
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/v1/anything", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	// No Content-Type.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("PUT without Content-Type got %d, want 415", resp.StatusCode)
	}
}

func TestContentType_PATCH_NoContentType_Returns415(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/v1/anything", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("PATCH without Content-Type got %d, want 415", resp.StatusCode)
	}
}

// --- 415 response envelope fields are well-formed ---

func TestContentType_EnvelopeHasMessageField(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// No Content-Type.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("no 'error' object in envelope")
	}
	msg, _ := errObj["message"].(string)
	if msg == "" {
		t.Error("expected non-empty 'message' field in error envelope")
	}
}

// Verify that Accept-Post is only set on 415 responses, not on successful ones.
func TestContentType_AcceptPostAbsentOnSuccess(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// GET /healthz — succeeds without Content-Type check.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ap := resp.Header.Get("Accept-Post"); ap != "" {
		t.Errorf("Accept-Post should not be set on non-415 response, got %q", ap)
	}
}
