// echo_invalid_json_test.go verifies feature #29:
// "Invalid JSON body returns 400 envelope"
//
// Malformed JSON request bodies sent to POST /v1/echo must produce 400
// responses with precise error codes that aid client diagnostics without
// leaking the raw body content (XSS / log-injection defence).
//
// Steps covered:
//  1. POST with '{not json' → HTTP 400, code='http.invalid_json'
//  2. Response message references parse error byte position
//  3. Raw body content is NOT echoed back (no XSS / log injection vector)
//  4. POST with empty body → HTTP 400, code='http.empty_body'
//  5. POST with valid JSON array instead of object → HTTP 400, code='http.invalid_shape'
//
// All tests use buildEchoServer (defined in echo_audit_test.go) and mintJWT to
// produce authenticated requests — authentication must succeed before the body
// parser runs, so a valid JWT is required for every case.
package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

// doEchoRequest sends POST /v1/echo with the supplied raw body string and
// bearer token. Returns the response (caller must close Body).
func doEchoRequest(t *testing.T, baseURL, token, rawBody string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		baseURL+"/v1/echo",
		strings.NewReader(rawBody),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-invalid-json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	return resp
}

// decodeErrorEnvelope reads and parses the response body into a generic map.
// It asserts that the Content-Type is application/json and the body is valid JSON.
func decodeErrorEnvelope(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll response body: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	var env map[string]any
	if err := json.Unmarshal(bodyBytes, &env); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, bodyBytes)
	}
	return env
}

// errorCode extracts the error.code field from a decoded error envelope.
func errorCode(t *testing.T, env map[string]any) string {
	t.Helper()
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got: %#v", env)
	}
	code, ok := errObj["code"].(string)
	if !ok {
		t.Fatalf("error.code is not a string; got: %#v", errObj["code"])
	}
	return code
}

// errorMessage extracts the error.message field from a decoded error envelope.
func errorMessage(t *testing.T, env map[string]any) string {
	t.Helper()
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got: %#v", env)
	}
	msg, ok := errObj["message"].(string)
	if !ok {
		t.Fatalf("error.message is not a string; got: %#v", errObj["message"])
	}
	return msg
}

// buildEchoTestToken builds a server and mints a JWT for test actor "user-001".
func buildEchoTestToken(t *testing.T) (baseURL string, token string) {
	t.Helper()
	ts, stub, _ := buildEchoServer(t)
	token = mintJWT(t, stub, "user-001")
	return ts.URL, token
}

// mintJWT is defined in echo_audit_test.go; helper re-exported here for clarity.
// (No redeclaration needed — same package.)

// =============================================================================
// Step 1 — Malformed JSON → HTTP 400
// =============================================================================

// TestInvalidJSON_MalformedBody_Returns400 verifies step 1: POST /v1/echo with
// a syntactically invalid JSON body ("{not json") must return HTTP 400.
func TestInvalidJSON_MalformedBody_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "{not json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestInvalidJSON_MalformedBody_CodeIsHttpInvalidJSON verifies step 1 (code):
// the error envelope code must be exactly 'http.invalid_json'.
func TestInvalidJSON_MalformedBody_CodeIsHttpInvalidJSON(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "{not json")
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "http.invalid_json" {
		t.Errorf("expected error code 'http.invalid_json', got %q", code)
	}
}

// TestInvalidJSON_MissingCloseBrace_Returns400 verifies step 1 with a body
// missing the closing brace ('{not json' from the feature description).
func TestInvalidJSON_MissingCloseBrace_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{not json`)
	env := decodeErrorEnvelope(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	code := errorCode(t, env)
	if code != "http.invalid_json" {
		t.Errorf("expected error code 'http.invalid_json', got %q", code)
	}
}

// =============================================================================
// Step 2 — Message references parse error position
// =============================================================================

// TestInvalidJSON_MessageReferencesPosition verifies step 2: the error message
// must include a reference to a byte offset or position in the parse error.
// This helps clients debug without exposing the full body content.
func TestInvalidJSON_MessageReferencesPosition(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "{not json")
	env := decodeErrorEnvelope(t, resp)

	msg := errorMessage(t, env)
	// The message must reference "offset" or a numeric position to help the
	// client identify where the parse failure occurred.
	if !strings.Contains(msg, "offset") && !strings.Contains(msg, "position") {
		t.Errorf("error message must reference parse error position (offset/position), got: %q", msg)
	}
}

// TestInvalidJSON_MessageContainsOffset_IsNumeric verifies that the position
// reference in the message is meaningful (contains a digit, not just "offset ").
func TestInvalidJSON_MessageContainsOffset_IsNumeric(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "{not json")
	env := decodeErrorEnvelope(t, resp)

	msg := errorMessage(t, env)
	// Check that the message contains at least one digit (the actual offset value).
	hasDigit := false
	for _, r := range msg {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		t.Errorf("error message must include a numeric byte offset, got: %q", msg)
	}
}

// =============================================================================
// Step 3 — Body NOT echoed back (XSS / log injection defence)
// =============================================================================

// TestInvalidJSON_BodyNotEchoedBack verifies step 3: the response must not
// contain the raw body content — a malicious body with script tags must not
// appear in the response.
func TestInvalidJSON_BodyNotEchoedBack(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	// Include a recognisable string that would be dangerous if echoed.
	const dangerousPayload = `{<script>alert('xss')</script>`
	resp := doEchoRequest(t, baseURL, token, dangerousPayload)
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	rawBody := string(bodyBytes)

	// The script tag must never appear in the response.
	if strings.Contains(rawBody, "<script>") {
		t.Errorf("raw body was echoed back in the response — XSS vector: %q", rawBody)
	}
	// The dangerous payload token must not appear verbatim.
	if strings.Contains(rawBody, "alert(") {
		t.Errorf("raw body content was echoed back in the response: %q", rawBody)
	}
}

// TestInvalidJSON_BodyNotEchoedBack_UniqueToken verifies step 3 with a unique
// sentinel token that would only appear if the server echoed the body.
func TestInvalidJSON_BodyNotEchoedBack_UniqueToken(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	const sentinel = "SENTINEL_SHOULD_NOT_APPEAR_IN_RESPONSE_XYZ987"
	malformedBody := `{` + sentinel + `}`
	resp := doEchoRequest(t, baseURL, token, malformedBody)
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if strings.Contains(string(bodyBytes), sentinel) {
		t.Errorf("body sentinel was echoed back in response — body reflection vulnerability")
	}
}

// =============================================================================
// Step 4 — Empty body → 400 http.empty_body
// =============================================================================

// TestInvalidJSON_EmptyBody_Returns400 verifies step 4: POST /v1/echo with an
// empty body must return HTTP 400.
func TestInvalidJSON_EmptyBody_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", resp.StatusCode)
	}
}

// TestInvalidJSON_EmptyBody_CodeIsHttpEmptyBody verifies step 4 (code): the
// error code must be 'http.empty_body' (or start with 'http.empty').
func TestInvalidJSON_EmptyBody_CodeIsHttpEmptyBody(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, "")
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if !strings.HasPrefix(code, "http.empty") {
		t.Errorf("expected error code starting with 'http.empty' for empty body, got %q", code)
	}
}

// TestInvalidJSON_EmptyBody_DistinctFromMalformed verifies step 4 (distinct):
// empty body and malformed JSON must produce different error codes — they are
// different client mistakes with different remediation guidance.
func TestInvalidJSON_EmptyBody_DistinctFromMalformed(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	respEmpty := doEchoRequest(t, baseURL, token, "")
	envEmpty := decodeErrorEnvelope(t, respEmpty)
	codeEmpty := errorCode(t, envEmpty)

	resp2URL, token2 := buildEchoTestToken(t)
	respMalformed := doEchoRequest(t, resp2URL, token2, "{not json")
	envMalformed := decodeErrorEnvelope(t, respMalformed)
	codeMalformed := errorCode(t, envMalformed)

	if codeEmpty == codeMalformed {
		t.Errorf("empty body and malformed JSON should have distinct error codes, both got %q", codeEmpty)
	}
}

// =============================================================================
// Step 5 — Valid JSON but wrong type (array) → 400 http.invalid_shape
// =============================================================================

// TestInvalidJSON_ArrayBody_Returns400 verifies step 5: POST /v1/echo with a
// JSON array body must return HTTP 400.
func TestInvalidJSON_ArrayBody_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `["message","hello"]`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for array body, got %d", resp.StatusCode)
	}
}

// TestInvalidJSON_ArrayBody_CodeIsHttpInvalidShape verifies step 5 (code): the
// error code for a structurally valid JSON but wrong type must be
// 'http.invalid_shape'.
func TestInvalidJSON_ArrayBody_CodeIsHttpInvalidShape(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `["message","hello"]`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "http.invalid_shape" {
		t.Errorf("expected error code 'http.invalid_shape' for array body, got %q", code)
	}
}

// TestInvalidJSON_ArrayBody_DistinctFromSyntaxError verifies step 5 (distinct):
// a valid JSON array and a syntax error must produce different codes — the
// array is syntactically valid JSON, just the wrong shape.
func TestInvalidJSON_ArrayBody_DistinctFromSyntaxError(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp2URL, token2 := buildEchoTestToken(t)
	respSyntax := doEchoRequest(t, resp2URL, token2, "{not json")
	envSyntax := decodeErrorEnvelope(t, respSyntax)
	codeSyntax := errorCode(t, envSyntax)

	respArray := doEchoRequest(t, baseURL, token, `[]`)
	envArray := decodeErrorEnvelope(t, respArray)
	codeArray := errorCode(t, envArray)

	if codeArray == codeSyntax {
		t.Errorf("array body and syntax error should have distinct codes, both got %q", codeArray)
	}
	if codeArray != "http.invalid_shape" {
		t.Errorf("array body code should be 'http.invalid_shape', got %q", codeArray)
	}
	if codeSyntax != "http.invalid_json" {
		t.Errorf("syntax error code should be 'http.invalid_json', got %q", codeSyntax)
	}
}

// TestInvalidJSON_ArrayBody_MessageMentionsExpectedObject verifies step 5
// (message): the error message should mention what was expected (object) to
// guide the client towards a fix.
func TestInvalidJSON_ArrayBody_MessageMentionsExpectedObject(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `[1,2,3]`)
	env := decodeErrorEnvelope(t, resp)

	msg := errorMessage(t, env)
	if !strings.Contains(strings.ToLower(msg), "object") {
		t.Errorf("http.invalid_shape message should mention 'object', got %q", msg)
	}
}

// =============================================================================
// Additional cross-cutting: error envelope shape
// =============================================================================

// TestInvalidJSON_ErrorEnvelopeHasRequiredFields verifies that all 400
// responses from invalid-body paths return the standard error envelope with
// code, message, request_id, and trace_id fields.
func TestInvalidJSON_ErrorEnvelopeHasRequiredFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"malformed", "{not json"},
		{"empty", ""},
		{"array", `["hello"]`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, tc.body)
			env := decodeErrorEnvelope(t, resp)

			errObj, ok := env["error"].(map[string]any)
			if !ok {
				t.Fatalf("envelope missing 'error' object for body %q", tc.body)
			}
			for _, field := range []string{"code", "message", "request_id", "trace_id"} {
				if _, present := errObj[field]; !present {
					t.Errorf("error envelope missing field %q for body %q", field, tc.body)
				}
			}
		})
	}
}

// TestInvalidJSON_CodeUsesDottedNamespace verifies that all three new error
// codes follow the dotted-namespace convention (http.something) as required
// by the OpenAPI schema pattern constraint.
func TestInvalidJSON_CodeUsesDottedNamespace(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name         string
		body         string
		expectedCode string
	}
	cases := []testCase{
		{"malformed_json", "{not json", "http.invalid_json"},
		{"empty_body", "", "http.empty_body"},
		{"array_body", `["hello"]`, "http.invalid_shape"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, tc.body)
			env := decodeErrorEnvelope(t, resp)
			code := errorCode(t, env)

			if code != tc.expectedCode {
				t.Errorf("expected code %q, got %q", tc.expectedCode, code)
			}
			// Must follow dotted-namespace: at least one dot.
			if !strings.Contains(code, ".") {
				t.Errorf("error code %q must use dotted-namespace convention", code)
			}
		})
	}
}

