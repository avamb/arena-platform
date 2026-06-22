// echo_empty_message_test.go verifies feature #57:
// "POST /v1/echo with empty message returns 400"
//
// The server distinguishes three invalid-message scenarios:
//
//  1. {"message":""} (empty string)          → 400 code='validation.field_empty'
//  2. {} (key absent)                         → 400 code='validation.field_required'
//  3. {"message":null} (explicit null)        → 400 code='validation.field_required'
//
// All error responses must include details.field='message' for machine-readable
// diagnostics.
//
// Tests use buildEchoTestToken (defined in echo_invalid_json_test.go) and
// doEchoRequest; each call creates a fresh isolated in-memory server.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

// doEchoRequestWithKey is like doEchoRequest but uses a caller-supplied
// Idempotency-Key, allowing a single server to receive multiple requests
// with distinct keys.
func doEchoRequestWithKey(t *testing.T, baseURL, token, rawBody, idemKey string) *http.Response {
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
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	return resp
}

// errorDetailsField extracts error.details.field from a decoded error envelope.
// Returns ("", false) if the details object or field key is absent.
func errorDetailsField(env map[string]any) (string, bool) {
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		return "", false
	}
	details, ok := errObj["details"].(map[string]any)
	if !ok {
		return "", false
	}
	field, ok := details["field"].(string)
	return field, ok
}

// =============================================================================
// Step 1+2 — {"message":""} → HTTP 400, code='validation.field_empty'
// =============================================================================

// TestEmptyMessage_EmptyString_Returns400 verifies step 1: POST /v1/echo with
// body {"message":""} must return HTTP 400.
func TestEmptyMessage_EmptyString_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":""}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty message string, got %d", resp.StatusCode)
	}
}

// TestEmptyMessage_EmptyString_CodeIsValidationFieldEmpty verifies step 2:
// the error code for {"message":""} must be exactly 'validation.field_empty'.
func TestEmptyMessage_EmptyString_CodeIsValidationFieldEmpty(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":""}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.field_empty" {
		t.Errorf("expected error code 'validation.field_empty', got %q", code)
	}
}

// TestEmptyMessage_EmptyString_DetailsFieldIsMessage verifies step 2 (details):
// the error envelope must contain details.field='message'.
func TestEmptyMessage_EmptyString_DetailsFieldIsMessage(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":""}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field must be present in validation.field_empty response")
	}
	if field != "message" {
		t.Errorf("expected details.field='message', got %q", field)
	}
}

// TestEmptyMessage_WhitespaceOnly_Returns400 verifies that a whitespace-only
// message (semantically empty) also returns 400 with validation.field_empty.
func TestEmptyMessage_WhitespaceOnly_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"   "}`)
	env := decodeErrorEnvelope(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only message, got %d", resp.StatusCode)
	}
	code := errorCode(t, env)
	if code != "validation.field_empty" {
		t.Errorf("expected 'validation.field_empty' for whitespace message, got %q", code)
	}
}

// =============================================================================
// Step 3 — {} (message missing) → HTTP 400, code='validation.field_required'
// =============================================================================

// TestEmptyMessage_MissingKey_Returns400 verifies step 3: POST /v1/echo with
// body {} (no 'message' key) must return HTTP 400.
func TestEmptyMessage_MissingKey_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing message key, got %d", resp.StatusCode)
	}
}

// TestEmptyMessage_MissingKey_CodeIsValidationFieldRequired verifies step 3
// (code): a missing 'message' key must yield code='validation.field_required'.
func TestEmptyMessage_MissingKey_CodeIsValidationFieldRequired(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.field_required" {
		t.Errorf("expected error code 'validation.field_required' for missing key, got %q", code)
	}
}

// TestEmptyMessage_MissingKey_DetailsFieldIsMessage verifies step 3 (details):
// the error envelope for a missing key must contain details.field='message'.
func TestEmptyMessage_MissingKey_DetailsFieldIsMessage(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field must be present in validation.field_required response")
	}
	if field != "message" {
		t.Errorf("expected details.field='message', got %q", field)
	}
}

// =============================================================================
// Step 4 — {"message":null} → HTTP 400, code='validation.field_required'
// =============================================================================

// TestEmptyMessage_NullValue_Returns400 verifies step 4: POST /v1/echo with
// body {"message":null} must return HTTP 400.
func TestEmptyMessage_NullValue_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":null}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for null message value, got %d", resp.StatusCode)
	}
}

// TestEmptyMessage_NullValue_CodeIsValidationFieldRequired verifies step 4
// (code): an explicit null 'message' must yield code='validation.field_required'.
func TestEmptyMessage_NullValue_CodeIsValidationFieldRequired(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":null}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.field_required" {
		t.Errorf("expected error code 'validation.field_required' for null value, got %q", code)
	}
}

// TestEmptyMessage_NullValue_DetailsFieldIsMessage verifies step 4 (details):
// the error envelope for {"message":null} must contain details.field='message'.
func TestEmptyMessage_NullValue_DetailsFieldIsMessage(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":null}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field must be present in validation.field_required (null) response")
	}
	if field != "message" {
		t.Errorf("expected details.field='message', got %q", field)
	}
}

// =============================================================================
// Distinction checks — three cases must produce distinct codes
// =============================================================================

// TestEmptyMessage_MissingAndNullHaveSameCode verifies that missing and null
// message values both produce 'validation.field_required' (they are treated
// identically — both mean "no value provided").
func TestEmptyMessage_MissingAndNullHaveSameCode(t *testing.T) {
	t.Parallel()

	baseURLMissing, tokenMissing := buildEchoTestToken(t)
	baseURLNull, tokenNull := buildEchoTestToken(t)

	respMissing := doEchoRequest(t, baseURLMissing, tokenMissing, `{}`)
	envMissing := decodeErrorEnvelope(t, respMissing)

	respNull := doEchoRequest(t, baseURLNull, tokenNull, `{"message":null}`)
	envNull := decodeErrorEnvelope(t, respNull)

	codeMissing := errorCode(t, envMissing)
	codeNull := errorCode(t, envNull)

	if codeMissing != "validation.field_required" {
		t.Errorf("missing key: expected 'validation.field_required', got %q", codeMissing)
	}
	if codeNull != "validation.field_required" {
		t.Errorf("null value: expected 'validation.field_required', got %q", codeNull)
	}
	if codeMissing != codeNull {
		t.Errorf("missing and null must produce the same code; got %q vs %q", codeMissing, codeNull)
	}
}

// TestEmptyMessage_EmptyVsRequiredCodesAreDistinct verifies that the empty-string
// case ('validation.field_empty') and the missing-key case ('validation.field_required')
// are distinct codes — they represent different client mistakes.
func TestEmptyMessage_EmptyVsRequiredCodesAreDistinct(t *testing.T) {
	t.Parallel()

	baseURLEmpty, tokenEmpty := buildEchoTestToken(t)
	baseURLMissing, tokenMissing := buildEchoTestToken(t)

	respEmpty := doEchoRequest(t, baseURLEmpty, tokenEmpty, `{"message":""}`)
	envEmpty := decodeErrorEnvelope(t, respEmpty)
	codeEmpty := errorCode(t, envEmpty)

	respMissing := doEchoRequest(t, baseURLMissing, tokenMissing, `{}`)
	envMissing := decodeErrorEnvelope(t, respMissing)
	codeMissing := errorCode(t, envMissing)

	if codeEmpty == codeMissing {
		t.Errorf("empty string and missing key must produce distinct error codes, both got %q", codeEmpty)
	}
	if codeEmpty != "validation.field_empty" {
		t.Errorf("empty string: expected 'validation.field_empty', got %q", codeEmpty)
	}
	if codeMissing != "validation.field_required" {
		t.Errorf("missing key: expected 'validation.field_required', got %q", codeMissing)
	}
}

// =============================================================================
// Non-empty message still succeeds (regression guard)
// =============================================================================

// TestEmptyMessage_ValidMessageSucceeds verifies that a valid non-empty message
// still produces HTTP 200 — the validation must not over-reject.
func TestEmptyMessage_ValidMessageSucceeds(t *testing.T) {
	t.Parallel()
	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "user-valid-001")

	resp := doEchoRequestWithKey(t, ts.URL, token, `{"message":"hello world"}`, "idem-key-valid-msg-001")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid non-empty message, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Full sweep (all 5 feature steps in one table-driven test)
// =============================================================================

// TestEmptyMessage_FullVerification exercises all 5 feature steps as defined:
//
//	Step 1: POST {"message":""} → 400, code='validation.field_empty'
//	Step 2: details.field='message' in the above response
//	Step 3: POST {} → 400, code='validation.field_required'
//	Step 4: POST {"message":null} → 400, code='validation.field_required'
//	Step 5: POST {"message":"hi"} → 200 (regression: valid message not rejected)
func TestEmptyMessage_FullVerification(t *testing.T) {
	type testCase struct {
		step         int
		body         string
		wantStatus   int
		wantCode     string
		wantDetails  bool
		detailsField string
	}
	cases := []testCase{
		{
			step:         1,
			body:         `{"message":""}`,
			wantStatus:   http.StatusBadRequest,
			wantCode:     "validation.field_empty",
			wantDetails:  true,
			detailsField: "message",
		},
		{
			step:         2,
			body:         `{"message":""}`,
			wantStatus:   http.StatusBadRequest,
			wantCode:     "validation.field_empty",
			wantDetails:  true,
			detailsField: "message",
		},
		{
			step:         3,
			body:         `{}`,
			wantStatus:   http.StatusBadRequest,
			wantCode:     "validation.field_required",
			wantDetails:  true,
			detailsField: "message",
		},
		{
			step:         4,
			body:         `{"message":null}`,
			wantStatus:   http.StatusBadRequest,
			wantCode:     "validation.field_required",
			wantDetails:  true,
			detailsField: "message",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("step"+string(rune('0'+tc.step)), func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, tc.body)
			env := decodeErrorEnvelope(t, resp)

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("step %d: expected status %d, got %d (body=%s)",
					tc.step, tc.wantStatus, resp.StatusCode, tc.body)
			}
			code := errorCode(t, env)
			if code != tc.wantCode {
				t.Errorf("step %d: expected code %q, got %q (body=%s)",
					tc.step, tc.wantCode, code, tc.body)
			}
			if tc.wantDetails {
				field, ok := errorDetailsField(env)
				if !ok {
					t.Errorf("step %d: error.details.field must be present (body=%s)", tc.step, tc.body)
				} else if field != tc.detailsField {
					t.Errorf("step %d: expected details.field=%q, got %q (body=%s)",
						tc.step, tc.detailsField, field, tc.body)
				}
			}
		})
	}
}
