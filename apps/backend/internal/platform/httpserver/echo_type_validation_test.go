// echo_type_validation_test.go verifies feature #58:
// "POST /v1/echo with non-string message returns 400"
//
// Type validation: message must be a string. Sending a number, boolean,
// array, or object for the 'message' field is rejected with:
//   - HTTP 400
//   - code='validation.field_type'
//   - details.expected='string'
//   - details.actual='number' (or 'bool', 'array', 'object')
//
// Steps:
//  1. POST with body {"message":123}     → 400 code='validation.field_type'
//  2. POST with body {"message":true}    → 400
//  3. POST with body {"message":["a"]}  → 400
//  4. POST with body {"message":{"x":1}} → 400
//  5. Verify details.expected='string' and details.actual='number' (or similar)
//
// Tests use buildEchoTestToken (defined in echo_invalid_json_test.go).
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

// errorDetailsExpected extracts error.details.expected from a decoded error envelope.
func errorDetailsExpected(env map[string]any) (string, bool) {
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		return "", false
	}
	details, ok := errObj["details"].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := details["expected"].(string)
	return v, ok
}

// errorDetailsActual extracts error.details.actual from a decoded error envelope.
func errorDetailsActual(env map[string]any) (string, bool) {
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		return "", false
	}
	details, ok := errObj["details"].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := details["actual"].(string)
	return v, ok
}

// decodeEchoEnvelope decodes a JSON response body into a generic map.
func decodeEchoTypeEnvelope(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return env
}

// =============================================================================
// Step 1 — {"message":123} → HTTP 400 with code='validation.field_type'
// =============================================================================

// TestTypeValidation_Number_Returns400 verifies step 1: {"message":123} → 400.
func TestTypeValidation_Number_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":123}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestTypeValidation_Number_CodeIsValidationFieldType verifies step 1+5:
// {"message":123} produces code='validation.field_type'.
func TestTypeValidation_Number_CodeIsValidationFieldType(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":123}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatal("missing 'error' object in response")
	}
	code, _ := errObj["code"].(string)
	if code != "validation.field_type" {
		t.Errorf("expected code='validation.field_type', got %q", code)
	}
}

// TestTypeValidation_Number_DetailsExpectedIsString verifies step 5:
// details.expected='string' for number input.
func TestTypeValidation_Number_DetailsExpectedIsString(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":123}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	expected, ok := errorDetailsExpected(env)
	if !ok {
		t.Fatal("missing details.expected in response")
	}
	if expected != "string" {
		t.Errorf("expected details.expected='string', got %q", expected)
	}
}

// TestTypeValidation_Number_DetailsActualIsNumber verifies step 5:
// details.actual='number' for numeric input.
func TestTypeValidation_Number_DetailsActualIsNumber(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":123}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	actual, ok := errorDetailsActual(env)
	if !ok {
		t.Fatal("missing details.actual in response")
	}
	if actual != "number" {
		t.Errorf("expected details.actual='number', got %q", actual)
	}
}

// =============================================================================
// Step 2 — {"message":true} → HTTP 400
// =============================================================================

// TestTypeValidation_Boolean_Returns400 verifies step 2: {"message":true} → 400.
func TestTypeValidation_Boolean_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":true}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestTypeValidation_Boolean_CodeIsValidationFieldType verifies step 2:
// {"message":true} produces code='validation.field_type'.
func TestTypeValidation_Boolean_CodeIsValidationFieldType(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":true}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatal("missing 'error' object in response")
	}
	code, _ := errObj["code"].(string)
	if code != "validation.field_type" {
		t.Errorf("expected code='validation.field_type', got %q", code)
	}
}

// TestTypeValidation_BoolFalse_Returns400 verifies false is also rejected.
func TestTypeValidation_BoolFalse_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":false}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Step 3 — {"message":["a"]} → HTTP 400
// =============================================================================

// TestTypeValidation_Array_Returns400 verifies step 3: {"message":["a"]} → 400.
func TestTypeValidation_Array_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":["a"]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestTypeValidation_Array_CodeIsValidationFieldType verifies step 3:
// {"message":["a"]} produces code='validation.field_type'.
func TestTypeValidation_Array_CodeIsValidationFieldType(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":["a"]}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatal("missing 'error' object in response")
	}
	code, _ := errObj["code"].(string)
	if code != "validation.field_type" {
		t.Errorf("expected code='validation.field_type', got %q", code)
	}
}

// =============================================================================
// Step 4 — {"message":{"x":1}} → HTTP 400
// =============================================================================

// TestTypeValidation_Object_Returns400 verifies step 4: {"message":{"x":1}} → 400.
func TestTypeValidation_Object_Returns400(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":{"x":1}}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestTypeValidation_Object_CodeIsValidationFieldType verifies step 4:
// {"message":{"x":1}} produces code='validation.field_type'.
func TestTypeValidation_Object_CodeIsValidationFieldType(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":{"x":1}}`)
	defer resp.Body.Close()

	env := decodeEchoTypeEnvelope(t, resp)
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatal("missing 'error' object in response")
	}
	code, _ := errObj["code"].(string)
	if code != "validation.field_type" {
		t.Errorf("expected code='validation.field_type', got %q", code)
	}
}

// =============================================================================
// Step 5 — details verification across types
// =============================================================================

// TestTypeValidation_DetailsFieldIsMessage verifies that details.field='message'
// is present for all non-string type rejections.
func TestTypeValidation_DetailsFieldIsMessage(t *testing.T) {
	t.Parallel()
	bodies := []string{
		`{"message":123}`,
		`{"message":true}`,
		`{"message":["a"]}`,
		`{"message":{"x":1}}`,
	}
	for _, body := range bodies {
		body := body
		t.Run(strings.ReplaceAll(body, `"`, ""), func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, body)
			defer resp.Body.Close()

			env := decodeEchoTypeEnvelope(t, resp)
			field, ok := errorDetailsField(env)
			if !ok {
				t.Fatal("missing details.field in response")
			}
			if field != "message" {
				t.Errorf("expected details.field='message', got %q", field)
			}
		})
	}
}

// TestTypeValidation_DetailsExpectedIsStringForAll verifies that details.expected='string'
// for all non-string type inputs.
func TestTypeValidation_DetailsExpectedIsStringForAll(t *testing.T) {
	t.Parallel()
	bodies := []string{
		`{"message":123}`,
		`{"message":true}`,
		`{"message":["a"]}`,
		`{"message":{"x":1}}`,
	}
	for _, body := range bodies {
		body := body
		t.Run(strings.ReplaceAll(body, `"`, ""), func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, body)
			defer resp.Body.Close()

			env := decodeEchoTypeEnvelope(t, resp)
			expected, ok := errorDetailsExpected(env)
			if !ok {
				t.Fatal("missing details.expected in response")
			}
			if expected != "string" {
				t.Errorf("expected details.expected='string', got %q", expected)
			}
		})
	}
}

// =============================================================================
// Regression guard — valid string message still passes
// =============================================================================

// TestTypeValidation_ValidStringMessageSucceeds ensures normal string messages
// are not affected by the type validation guard.
func TestTypeValidation_ValidStringMessageSucceeds(t *testing.T) {
	t.Parallel()
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"hello world"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid string, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Full verification sweep (feature #58)
// =============================================================================

// TestTypeValidation_FullVerification runs all 5 feature steps as sub-tests.
func TestTypeValidation_FullVerification(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name           string
		body           string
		expectedCode   string
		expectedActual string
	}

	cases := []testCase{
		{
			name:           "Step1_NumberRejected",
			body:           `{"message":123}`,
			expectedCode:   "validation.field_type",
			expectedActual: "number",
		},
		{
			name:         "Step2_BooleanRejected",
			body:         `{"message":true}`,
			expectedCode: "validation.field_type",
		},
		{
			name:         "Step3_ArrayRejected",
			body:         `{"message":["a"]}`,
			expectedCode: "validation.field_type",
		},
		{
			name:         "Step4_ObjectRejected",
			body:         `{"message":{"x":1}}`,
			expectedCode: "validation.field_type",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseURL, token := buildEchoTestToken(t)

			resp := doEchoRequest(t, baseURL, token, tc.body)
			defer resp.Body.Close()

			// 1. Status must be 400
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", resp.StatusCode)
			}

			// 2. Decode and check code
			env := decodeEchoTypeEnvelope(t, resp)
			errObj, ok := env["error"].(map[string]any)
			if !ok {
				t.Fatal("missing 'error' object")
			}
			code, _ := errObj["code"].(string)
			if code != tc.expectedCode {
				t.Errorf("expected code=%q, got %q", tc.expectedCode, code)
			}

			// 3. details.expected must be 'string'
			expected, ok := errorDetailsExpected(env)
			if !ok {
				t.Error("missing details.expected")
			} else if expected != "string" {
				t.Errorf("expected details.expected='string', got %q", expected)
			}

			// 4. details.field must be 'message'
			field, ok := errorDetailsField(env)
			if !ok {
				t.Error("missing details.field")
			} else if field != "message" {
				t.Errorf("expected details.field='message', got %q", field)
			}

			// 5. details.actual (if expected is set)
			if tc.expectedActual != "" {
				actual, ok := errorDetailsActual(env)
				if !ok {
					t.Error("missing details.actual")
				} else if actual != tc.expectedActual {
					t.Errorf("expected details.actual=%q, got %q", tc.expectedActual, actual)
				}
			}
		})
	}
}
