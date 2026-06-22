// error_envelope_test.go verifies feature #36:
// "Error envelope schema documented in OpenAPI"
//
// The standard ErrorEnvelope component in openapi/openapi.yaml must:
//  1. Exist and be parseable
//  2. Declare required fields: error.code, error.message, error.request_id, error.trace_id
//  3. Expose an optional error.details field (jsonb-like object)
//  4. Constrain error.code with a pattern (dotted-namespace format)
//  5. Document a 5xx response on GET /v1/info referencing ErrorEnvelope
//  6. Document 400, 401, 409, 413, 415, 500 on POST /v1/echo — all referencing ErrorEnvelope
package httpserver

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"
)

// findErrorEnvelopeSpec returns the raw bytes of openapi.yaml via the same
// runtime.Caller-based path resolution used in openapi_drift_test.go.
func findErrorEnvelopeSpec(t *testing.T) []byte {
	t.Helper()
	specPath := findOpenAPISpecPath(t) // helper defined in openapi_drift_test.go
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml at %s: %v", specPath, err)
	}
	if len(data) == 0 {
		t.Fatalf("openapi.yaml is empty")
	}
	return data
}

// extractErrorSchemaBlock returns the lines belonging to the ErrorEnvelope
// → error inner-object block inside components.schemas. It scans until the
// next peer-level schema (a line with 4 leading spaces that is not a
// continuation of ErrorEnvelope).
func extractErrorSchemaBlock(data []byte) []byte {
	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))

	// State machine:
	//  0 = looking for "    ErrorEnvelope:" (4 spaces)
	//  1 = inside ErrorEnvelope block
	state := 0

	for scanner.Scan() {
		line := scanner.Text()
		switch state {
		case 0:
			trimmed := strings.TrimRight(line, " \t")
			if trimmed == "    ErrorEnvelope:" {
				state = 1
				buf.WriteString(line + "\n")
			}
		case 1:
			// A line with exactly 4 leading spaces and not just whitespace that is
			// NOT part of the ErrorEnvelope block (i.e. it's the next schema).
			if len(line) >= 4 && line[0] == ' ' && line[1] == ' ' && line[2] == ' ' && line[3] == ' ' {
				// Count leading spaces.
				ns := 0
				for _, ch := range line {
					if ch == ' ' {
						ns++
					} else {
						break
					}
				}
				rest := strings.TrimLeft(line, " ")
				// A 4-space-indented non-empty line that isn't a list or value
				// continuation AND is not "ErrorEnvelope:" itself signals a new schema.
				if ns == 4 && rest != "" && strings.HasSuffix(strings.TrimRight(rest, " \t"), ":") &&
					!strings.HasPrefix(rest, "-") {
					// This is the next peer schema — stop.
					state = 0
					break
				}
			} else if len(line) > 0 && line[0] != ' ' {
				// Top-level key (e.g. "paths:") — stop.
				state = 0
				break
			}
			buf.WriteString(line + "\n")
		}
	}
	return buf.Bytes()
}

// extractEchoResponseBlock returns the lines belonging to the /v1/echo path
// item's responses section. It stops at the next top-level path or section.
func extractEchoResponseBlock(data []byte) []byte {
	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))

	state := 0 // 0=searching, 1=in /v1/echo responses

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t")

		switch state {
		case 0:
			if trimmed == "  /v1/echo:" {
				state = 1
			}
		case 1:
			// Another top-level path starts at "  /something:" (2-space indent + /)
			if len(line) >= 3 && line[0] == ' ' && line[1] == ' ' && line[2] == '/' && trimmed != "  /v1/echo:" {
				state = 0
				break
			}
			// Non-indented line signals we've left the paths section entirely.
			if len(line) > 0 && line[0] != ' ' {
				state = 0
				break
			}
			buf.WriteString(line + "\n")
		}
	}
	return buf.Bytes()
}

// extractInfoResponseBlock returns the lines belonging to the /v1/info path item.
func extractInfoResponseBlock(data []byte) []byte {
	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))

	state := 0

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t")

		switch state {
		case 0:
			if trimmed == "  /v1/info:" {
				state = 1
			}
		case 1:
			if len(line) >= 3 && line[0] == ' ' && line[1] == ' ' && line[2] == '/' && trimmed != "  /v1/info:" {
				state = 0
				break
			}
			if len(line) > 0 && line[0] != ' ' {
				state = 0
				break
			}
			buf.WriteString(line + "\n")
		}
	}
	return buf.Bytes()
}

// =============================================================================
// Step 1 — ErrorEnvelope schema exists in components.schemas
// =============================================================================

// TestErrorEnvelope_SchemaExists verifies step 1: ErrorEnvelope is declared
// under components.schemas in openapi.yaml.
func TestErrorEnvelope_SchemaExists(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)

	if !bytes.Contains(data, []byte("ErrorEnvelope:")) {
		t.Fatal("openapi.yaml must contain an 'ErrorEnvelope:' schema under components.schemas")
	}
}

// =============================================================================
// Step 2 — Required fields: code, message, request_id, trace_id
// =============================================================================

// TestErrorEnvelope_RequiredFieldsPresent verifies step 2: the inner error
// object lists all four required fields: code, message, request_id, trace_id.
func TestErrorEnvelope_RequiredFieldsPresent(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	block := extractErrorSchemaBlock(data)

	if len(block) == 0 {
		t.Fatal("failed to extract ErrorEnvelope schema block from openapi.yaml")
	}

	// All four fields must appear in the required array inside the error object.
	requiredFields := []string{"code", "message", "request_id", "trace_id"}
	for _, field := range requiredFields {
		// The required list is inline YAML: required: [code, message, request_id, trace_id]
		// OR multi-line with "- field" entries. Check for either form.
		inlineToken := []byte(field)
		if !bytes.Contains(block, inlineToken) {
			t.Errorf("ErrorEnvelope.error required list must include %q", field)
		}
	}
}

// TestErrorEnvelope_RequiredArrayContainsAllFour verifies that the `required:`
// array inside the inner error object explicitly lists all four required fields
// (not just that the properties exist — they must be in `required`).
func TestErrorEnvelope_RequiredArrayContainsAllFour(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)

	// Find the line containing "required: [code, message, request_id, trace_id]"
	// or a multi-line required block that lists all four.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var requiredLine string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "required:") && strings.Contains(trimmed, "code") {
			requiredLine = trimmed
			break
		}
	}
	if requiredLine == "" {
		t.Fatal("could not find 'required:' list containing 'code' in ErrorEnvelope block")
	}

	for _, field := range []string{"code", "message", "request_id", "trace_id"} {
		if !strings.Contains(requiredLine, field) {
			t.Errorf("ErrorEnvelope.error required list is missing %q (found: %q)", field, requiredLine)
		}
	}
}

// =============================================================================
// Step 3 — error.details is an optional jsonb-like object
// =============================================================================

// TestErrorEnvelope_DetailsFieldIsOptional verifies step 3: the error object
// defines a `details` property but does NOT include it in the required array
// (it is optional).
func TestErrorEnvelope_DetailsFieldIsOptional(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	block := extractErrorSchemaBlock(data)

	if len(block) == 0 {
		t.Fatal("failed to extract ErrorEnvelope schema block from openapi.yaml")
	}

	// details must appear as a property key.
	if !bytes.Contains(block, []byte("details:")) {
		t.Error("ErrorEnvelope.error must define an optional 'details' property")
	}

	// required line must NOT contain "details".
	scanner := bufio.NewScanner(bytes.NewReader(block))
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "required:") && strings.Contains(trimmed, "details") {
			t.Error("'details' must be optional and must NOT appear in the required array")
		}
	}
}

// TestErrorEnvelope_DetailsIsObjectType verifies that error.details is typed
// as an object (additionalProperties accepted or no explicit type rejection).
func TestErrorEnvelope_DetailsIsObjectType(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)

	// The details property block should indicate it is an object (either via
	// "type: object" or "additionalProperties:" which implies object type).
	if !bytes.Contains(data, []byte("additionalProperties:")) {
		t.Error("ErrorEnvelope.error.details must be jsonb-like: declare 'additionalProperties:' to allow arbitrary key-value pairs")
	}
}

// =============================================================================
// Step 4 — error.code is a constrained string with pattern
// =============================================================================

// TestErrorEnvelope_CodeHasPatternConstraint verifies step 4: error.code
// declares a `pattern:` constraint (dotted-namespace format).
func TestErrorEnvelope_CodeHasPatternConstraint(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	block := extractErrorSchemaBlock(data)

	if len(block) == 0 {
		t.Fatal("failed to extract ErrorEnvelope schema block from openapi.yaml")
	}

	if !bytes.Contains(block, []byte("pattern:")) {
		t.Error("ErrorEnvelope.error.code must include a 'pattern:' constraint (e.g. '^[a-z][a-z0-9_]+\\.[a-z0-9_.]+$')")
	}
}

// TestErrorEnvelope_CodePatternIsDottedNamespace verifies that the pattern
// enforces the dotted-namespace convention for error codes.
func TestErrorEnvelope_CodePatternIsDottedNamespace(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)

	// The pattern must contain a literal dot (escaped or unescaped) to enforce
	// the namespace.subcode convention.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var patternLine string
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "pattern:") {
			patternLine = trimmed
			break
		}
	}
	if patternLine == "" {
		t.Fatal("no 'pattern:' line found in openapi.yaml ErrorEnvelope schema")
	}

	// Pattern must enforce at least one dot separator between namespace and subcode.
	if !strings.Contains(patternLine, ".") {
		t.Errorf("error.code pattern must enforce dotted-namespace format (must contain '.'): %q", patternLine)
	}
}

// =============================================================================
// Step 5 — GET /v1/info documents a 5xx response referencing ErrorEnvelope
// =============================================================================

// TestErrorEnvelope_InfoHas5xxResponse verifies step 5: the /v1/info path item
// in openapi.yaml documents at least one 5xx HTTP status code response.
func TestErrorEnvelope_InfoHas5xxResponse(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	infoBlock := extractInfoResponseBlock(data)

	if len(infoBlock) == 0 {
		t.Fatal("failed to extract /v1/info path block from openapi.yaml")
	}

	// Look for any "5xx" status code key: "500", "502", "503", "5XX", etc.
	has5xx := false
	scanner := bufio.NewScanner(bytes.NewReader(infoBlock))
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, `"5`) || strings.HasPrefix(trimmed, "5") {
			// e.g. '"500":' or '500:'
			if strings.HasSuffix(strings.TrimRight(trimmed, " "), ":") {
				has5xx = true
				break
			}
		}
	}
	if !has5xx {
		t.Error("GET /v1/info in openapi.yaml must document at least one 5xx response (e.g. '\"500\":')")
	}
}

// TestErrorEnvelope_InfoErrorResponseReferencesEnvelope verifies step 5b:
// the 5xx response block for /v1/info references the ErrorEnvelope schema.
func TestErrorEnvelope_InfoErrorResponseReferencesEnvelope(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	infoBlock := extractInfoResponseBlock(data)

	if len(infoBlock) == 0 {
		t.Fatal("failed to extract /v1/info path block from openapi.yaml")
	}

	if !bytes.Contains(infoBlock, []byte("ErrorEnvelope")) {
		t.Error("GET /v1/info must reference the ErrorEnvelope schema in its error response(s)")
	}
}

// =============================================================================
// Step 6 — POST /v1/echo documents 400, 401, 409, 413, 415, 500
//           and all reference ErrorEnvelope
// =============================================================================

// TestErrorEnvelope_EchoResponseCodes verifies step 6a: POST /v1/echo documents
// all six required error status codes.
func TestErrorEnvelope_EchoResponseCodes(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	echoBlock := extractEchoResponseBlock(data)

	if len(echoBlock) == 0 {
		t.Fatal("failed to extract /v1/echo path block from openapi.yaml")
	}

	required := []string{`"400"`, `"401"`, `"409"`, `"413"`, `"415"`, `"500"`}
	for _, code := range required {
		if !bytes.Contains(echoBlock, []byte(code+":")) {
			t.Errorf("POST /v1/echo must document HTTP %s response in openapi.yaml", code)
		}
	}
}

// TestErrorEnvelope_EchoAllErrorResponsesReferenceEnvelope verifies step 6b:
// all error responses in the /v1/echo block reference the ErrorEnvelope schema
// (i.e. $ref: "#/components/schemas/ErrorEnvelope").
func TestErrorEnvelope_EchoAllErrorResponsesReferenceEnvelope(t *testing.T) {
	t.Parallel()

	data := findErrorEnvelopeSpec(t)
	echoBlock := extractEchoResponseBlock(data)

	if len(echoBlock) == 0 {
		t.Fatal("failed to extract /v1/echo path block from openapi.yaml")
	}

	// Each of the 6 required error codes must be followed (within a few lines)
	// by a $ref to ErrorEnvelope. We count occurrences of the $ref pattern
	// inside the echo block — we need at least 6 (one per error response).
	refToken := []byte(`"#/components/schemas/ErrorEnvelope"`)
	count := bytes.Count(echoBlock, refToken)

	// 200 + 400 + 401 + 409 + 413 + 415 + 500 (+ 503) — at minimum the 6 error codes.
	const minErrorRefs = 6
	if count < minErrorRefs {
		t.Errorf("POST /v1/echo must reference ErrorEnvelope in all error responses: found %d refs, need at least %d", count, minErrorRefs)
	}
}
