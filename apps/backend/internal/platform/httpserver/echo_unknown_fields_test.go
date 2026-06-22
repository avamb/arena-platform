// echo_unknown_fields_test.go verifies feature #59:
// "POST /v1/echo with extra unknown fields rejected consistently"
//
// Strict schema policy: the server rejects any field in the EchoRequest body
// that is not explicitly defined in the schema (i.e. anything other than
// "message"). This protects against typos and prevents clients from silently
// passing private data in unknown fields.
//
// Steps:
//  1. POST {"message":"x","extra":"y"} → 400 code='validation.unknown_field',
//     details.field='extra'
//  2. Error response includes the unknown field NAME but NOT its value (no leak).
//  3. POST {"messsage":"x"} (typo of "message") → 400 code='validation.unknown_field'
//     for the typo field (and "message" is missing, but the unknown field is
//     reported first).
//  4. Policy is documented in openapi/openapi.yaml (additionalProperties: false)
//     and in README.md.
//
// Tests use buildEchoTestToken and doEchoRequest (defined in
// echo_invalid_json_test.go) and errorDetailsField (defined in
// echo_empty_message_test.go).
package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

// =============================================================================
// Step 1 + 2: {"message":"x","extra":"y"} → 400 validation.unknown_field
// =============================================================================

// TestUnknownField_ExtraFieldReturns400 verifies that an extra unknown field
// alongside a valid "message" produces HTTP 400.
func TestUnknownField_ExtraFieldReturns400(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"y"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected HTTP 400, got %d", resp.StatusCode)
	}
}

// TestUnknownField_ExtraFieldCodeIsUnknownField verifies code='validation.unknown_field'.
func TestUnknownField_ExtraFieldCodeIsUnknownField(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"y"}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.unknown_field" {
		t.Errorf("expected code 'validation.unknown_field', got %q", code)
	}
}

// TestUnknownField_ExtraFieldDetailsFieldIsExtra verifies details.field='extra'.
func TestUnknownField_ExtraFieldDetailsFieldIsExtra(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"y"}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field missing from response")
	}
	if field != "extra" {
		t.Errorf("expected details.field='extra', got %q", field)
	}
}

// TestUnknownField_ValueNotLeakedInError verifies that the VALUE of the unknown
// field ("y") does not appear anywhere in the error response body.
func TestUnknownField_ValueNotLeakedInError(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	// Use a distinctive value that would be obviously visible if leaked.
	body := `{"message":"x","extra":"LEAKED_SECRET_VALUE_42"}`
	resp := doEchoRequest(t, baseURL, token, body)
	defer resp.Body.Close()

	// Read raw response body to check for leakage.
	import_buf := make([]byte, 4096)
	n, _ := resp.Body.Read(import_buf)
	raw := string(import_buf[:n])

	if strings.Contains(raw, "LEAKED_SECRET_VALUE_42") {
		t.Errorf("unknown field VALUE leaked in error response: %s", raw)
	}
}

// TestUnknownField_FieldNamePresentInError verifies that the field NAME ("extra")
// IS present in the error response (so the client knows which field to remove).
func TestUnknownField_FieldNamePresentInError(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"y"}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field missing")
	}
	if field != "extra" {
		t.Errorf("field name not present in error; expected 'extra', got %q", field)
	}
}

// TestUnknownField_MultipleUnknownFields verifies that at least one unknown field
// is reported when multiple unknown fields are present.
func TestUnknownField_MultipleUnknownFields(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"x","foo":"1","bar":"2"}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.unknown_field" {
		t.Errorf("expected code 'validation.unknown_field', got %q", code)
	}
	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field missing")
	}
	if field != "foo" && field != "bar" {
		t.Errorf("expected details.field to be 'foo' or 'bar', got %q", field)
	}
}

// =============================================================================
// Step 3: {"messsage":"x"} (typo) → validation.unknown_field
// =============================================================================

// TestUnknownField_TypoReturns400 verifies that a typo in the field name
// ("messsage" instead of "message") produces HTTP 400.
func TestUnknownField_TypoReturns400(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"messsage":"x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for typo field, got %d", resp.StatusCode)
	}
}

// TestUnknownField_TypoCodeIsUnknownField verifies code='validation.unknown_field'
// for a typo field name (unknown field reported before missing required field).
func TestUnknownField_TypoCodeIsUnknownField(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"messsage":"x"}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code != "validation.unknown_field" {
		t.Errorf("expected code 'validation.unknown_field' for typo, got %q", code)
	}
}

// TestUnknownField_TypoDetailsFieldIsMesssage verifies details.field='messsage'
// (the typo) in the error response.
func TestUnknownField_TypoDetailsFieldIsMesssage(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"messsage":"x"}`)
	env := decodeErrorEnvelope(t, resp)

	field, ok := errorDetailsField(env)
	if !ok {
		t.Fatal("error.details.field missing")
	}
	if field != "messsage" {
		t.Errorf("expected details.field='messsage', got %q", field)
	}
}

// TestUnknownField_TypoDoesNotReturnFieldRequired verifies that the response is
// NOT code='validation.field_required' — the unknown field check fires first.
func TestUnknownField_TypoDoesNotReturnFieldRequired(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"messsage":"x"}`)
	env := decodeErrorEnvelope(t, resp)

	code := errorCode(t, env)
	if code == "validation.field_required" {
		t.Error("got validation.field_required; expected validation.unknown_field for typo field")
	}
}

// =============================================================================
// Step 4: Policy documented in openapi.yaml and README
// =============================================================================

// TestUnknownField_OpenAPIDocumentsAdditionalPropertiesFalse verifies that the
// EchoRequest schema in openapi.yaml contains "additionalProperties: false".
func TestUnknownField_OpenAPIDocumentsAdditionalPropertiesFalse(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "additionalProperties: false") {
		t.Error("openapi.yaml does not contain 'additionalProperties: false' for EchoRequest")
	}
}

// TestUnknownField_OpenAPIEchoRequestHasAdditionalPropertiesFalse verifies that
// "additionalProperties: false" appears in the EchoRequest section specifically.
func TestUnknownField_OpenAPIEchoRequestHasAdditionalPropertiesFalse(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")

	// Find the EchoRequest block and verify additionalProperties: false is there.
	idx := strings.Index(content, "EchoRequest:")
	if idx < 0 {
		t.Fatal("EchoRequest: not found in openapi.yaml")
	}
	// Look at the next ~500 characters after EchoRequest: to find the setting
	// (within the schema block, before the next top-level key).
	section := content[idx:]
	endIdx := strings.Index(section[1:], "\n  ") // next same-level key
	if endIdx > 0 {
		section = section[:endIdx+1]
	}
	if !strings.Contains(section, "additionalProperties: false") {
		t.Errorf("EchoRequest section in openapi.yaml missing 'additionalProperties: false'.\nSection:\n%s", section)
	}
}

// TestUnknownField_READMEDocumentsStrictSchemaPolicy verifies that README.md
// documents the unknown field rejection policy for POST /v1/echo.
func TestUnknownField_READMEDocumentsStrictSchemaPolicy(t *testing.T) {
	content := findFileByName(t, "README.md")
	if !strings.Contains(content, "validation.unknown_field") {
		t.Error("README.md does not mention 'validation.unknown_field' policy for POST /v1/echo")
	}
}

// TestUnknownField_READMEMentionsStrictSchema verifies README uses "strict schema"
// language documenting the policy intent.
func TestUnknownField_READMEMentionsStrictSchema(t *testing.T) {
	content := findFileByName(t, "README.md")
	if !strings.Contains(content, "strict schema") && !strings.Contains(content, "strict-schema") {
		t.Error("README.md does not describe a 'strict schema' policy for POST /v1/echo")
	}
}

// =============================================================================
// Regression: valid request still succeeds
// =============================================================================

// TestUnknownField_ValidRequestStillSucceeds verifies that a well-formed request
// {"message":"hello"} is not rejected by the unknown field check.
func TestUnknownField_ValidRequestStillSucceeds(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	resp := doEchoRequest(t, baseURL, token, `{"message":"hello"}`)
	defer resp.Body.Close()

	// 200 or at least not 400 from the unknown-field check.
	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("valid request was rejected with 400 — unknown field check is too broad")
	}
}

// =============================================================================
// Full sweep (all steps in one table-driven test)
// =============================================================================

// TestUnknownField_FullVerification runs all feature steps as sub-tests so the
// feature backlog can match each step to a test case.
func TestUnknownField_FullVerification(t *testing.T) {
	baseURL, token := buildEchoTestToken(t)

	t.Run("Step1_ExtraFieldReturns400WithUnknownFieldCode", func(t *testing.T) {
		resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"y"}`)
		env := decodeErrorEnvelope(t, resp)

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
		code := errorCode(t, env)
		if code != "validation.unknown_field" {
			t.Errorf("expected code 'validation.unknown_field', got %q", code)
		}
		field, _ := errorDetailsField(env)
		if field != "extra" {
			t.Errorf("expected details.field='extra', got %q", field)
		}
	})

	t.Run("Step2_FieldNamePresentValueNotLeaked", func(t *testing.T) {
		resp := doEchoRequest(t, baseURL, token, `{"message":"x","extra":"SHOULD_NOT_APPEAR"}`)
		env := decodeErrorEnvelope(t, resp)

		field, ok := errorDetailsField(env)
		if !ok || field != "extra" {
			t.Errorf("details.field should be 'extra', got %q (ok=%v)", field, ok)
		}
		// Verify the raw JSON doesn't include the value.
		errObj := env["error"].(map[string]any)
		rawDetails, _ := errObj["details"].(map[string]any)
		for _, v := range rawDetails {
			if s, ok := v.(string); ok && s == "SHOULD_NOT_APPEAR" {
				t.Error("unknown field VALUE leaked in error details")
			}
		}
	})

	t.Run("Step3_TypoFieldReturnsUnknownField", func(t *testing.T) {
		resp := doEchoRequest(t, baseURL, token, `{"messsage":"x"}`)
		env := decodeErrorEnvelope(t, resp)

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
		code := errorCode(t, env)
		if code != "validation.unknown_field" {
			t.Errorf("expected code 'validation.unknown_field' for typo, got %q", code)
		}
		field, _ := errorDetailsField(env)
		if field != "messsage" {
			t.Errorf("expected details.field='messsage', got %q", field)
		}
	})

	t.Run("Step4a_OpenAPIHasAdditionalPropertiesFalse", func(t *testing.T) {
		content := findFileByName(t, "openapi.yaml")
		if !strings.Contains(content, "additionalProperties: false") {
			t.Error("openapi.yaml missing additionalProperties: false")
		}
	})

	t.Run("Step4b_READMEDocumentsPolicy", func(t *testing.T) {
		content := findFileByName(t, "README.md")
		if !strings.Contains(content, "validation.unknown_field") {
			t.Error("README.md missing documentation of validation.unknown_field policy")
		}
	})
}
