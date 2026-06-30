// openapi_scan_events_278_test.go pins the OpenAPI documentation
// contract established by feature #278 (Wave A-17): the scan_events
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/scanner_callback.go
// (the feature description references the historical "scan_events.go"
// group name; the actual file is scanner_callback.go which hosts the
// POST /v1/scanner/scan-events ingest handler) must be documented in
// apps/backend/openapi/openapi.yaml together with its component
// schemas, security scheme, error envelope, and example payloads.
//
// Coverage matches feature #278 acceptance steps:
//
//   - Step 1: path + operationId entry for the scanner_callback
//     handler (handleScannerScanEvents) together with the four
//     component schemas (ScannerScanInput, ScannerScanBatchRequest,
//     ScannerScanResult, ScannerScanBatchResponse).
//   - Step 2: permission story is documented (this endpoint is NOT
//     gated by JWT / RBAC — authentication is performed solely via
//     the agentFeedTokenAuth bearer scheme, distinct from bearerAuth);
//     the standard ErrorEnvelope is wired on every error status code;
//     the operation declares `agentFeedTokenAuth: []`; the new
//     `agentFeedTokenAuth` security scheme is registered under
//     components.securitySchemes.
//   - Step 3: minimal contract test validates the spec's example
//     payloads against the invariants enforced by scanner_callback.go
//     (runs in CI without docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and nullable shape (`ticket_id`).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI278_PathPresent verifies step 1: /v1/scanner/scan-events
// is documented under `paths:` in openapi.yaml.
func TestOpenAPI278_PathPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	if !strings.Contains(spec, "  /v1/scanner/scan-events:") {
		t.Errorf("openapi.yaml missing path mapping %q", "/v1/scanner/scan-events")
	}
}

// TestOpenAPI278_OperationIDPresent verifies the canonical
// operationId used by oapi-codegen / the TS client generator for
// the scan_events ingest handler.
func TestOpenAPI278_OperationIDPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	if !strings.Contains(spec, "operationId: postScannerScanEvents") {
		t.Errorf("openapi.yaml missing operationId %q", "postScannerScanEvents")
	}
}

// TestOpenAPI278_SchemasPresent verifies step 4: every scan_events
// component schema is declared under components.schemas.
func TestOpenAPI278_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    ScannerScanInput:",
		"    ScannerScanBatchRequest:",
		"    ScannerScanResult:",
		"    ScannerScanBatchResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI278_AgentFeedTokenSchemeRegistered verifies that the
// `agentFeedTokenAuth` security scheme used by the scan_events
// endpoint is declared under components.securitySchemes. The
// scheme is distinct from `bearerAuth` (JWT) so OpenAPI consumers
// can tell the credentials apart.
func TestOpenAPI278_AgentFeedTokenSchemeRegistered(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	if !strings.Contains(spec, "    agentFeedTokenAuth:") {
		t.Error("openapi.yaml missing `agentFeedTokenAuth` security scheme under components.securitySchemes")
	}
	// The scheme must declare a bearerFormat distinct from the JWT
	// bearerAuth scheme so generated clients can render them
	// independently.
	if !strings.Contains(spec, "bearerFormat: AgentFeedToken") {
		t.Error("openapi.yaml missing `bearerFormat: AgentFeedToken` on agentFeedTokenAuth")
	}
}

// TestOpenAPI278_OperationSecurityAndTags verifies that the
// postScannerScanEvents operation declares `agentFeedTokenAuth: []`
// (NOT bearerAuth — distinct credential) and the `[v1, scanner]`
// tag set.
func TestOpenAPI278_OperationSecurityAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: postScannerScanEvents"
	idx := strings.Index(spec, op)
	if idx < 0 {
		t.Fatalf("operation %q missing from openapi.yaml", op)
	}
	end := idx + 12000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	if !strings.Contains(window, "- agentFeedTokenAuth: []") {
		t.Errorf("operation %q missing `agentFeedTokenAuth: []` security entry", op)
	}
	if !strings.Contains(window, "tags: [v1, scanner]") {
		t.Errorf("operation %q missing `tags: [v1, scanner]`", op)
	}
}

// TestOpenAPI278_ErrorEnvelopeUsed verifies step 2: the
// postScannerScanEvents operation wires the standard ErrorEnvelope
// on every error response (400 / 401 / 413 / 500 / 503).
func TestOpenAPI278_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: postScannerScanEvents"
	idx := strings.Index(spec, op)
	if idx < 0 {
		t.Fatalf("operation %q missing", op)
	}
	end := idx + 12000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, status := range []string{`"400"`, `"401"`, `"413"`, `"500"`, `"503"`} {
		if !strings.Contains(window, status) {
			t.Errorf("operation %q missing error response %s", op, status)
		}
	}
	if !strings.Contains(window, `$ref: "#/components/schemas/ErrorEnvelope"`) {
		t.Errorf("operation %q does not reference ErrorEnvelope on any error response", op)
	}
}

// TestOpenAPI278_ScannerErrorCodesDocumented verifies that the spec
// documents the canonical error codes emitted by scanner_callback.go:
//
//   - scanner.missing_token
//   - scanner.invalid_token
//   - scanner.auth_lookup_failed
//   - scanner.invalid_body
//   - scanner.empty_batch
//   - scanner.batch_too_large
//   - dependency.database_unavailable
func TestOpenAPI278_ScannerErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"scanner.missing_token",
		"scanner.invalid_token",
		"scanner.auth_lookup_failed",
		"scanner.invalid_body",
		"scanner.empty_batch",
		"scanner.batch_too_large",
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document scanner error code %q", code)
		}
	}
}

// TestOpenAPI278_ScannerScanResultSchemaShape verifies the
// ScannerScanResult component schema lists every required field
// including the nullable-projection (`ticket_id`). The handler
// sets ticket_id to explicit JSON null when the credential could
// not be resolved; the spec documents the null-on-omission
// contract in prose because oapi-codegen v2.4.1 does not yet
// accept the OAS 3.1 `type: [string, "null"]` array idiom.
func TestOpenAPI278_ScannerScanResultSchemaShape(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	const header = "    ScannerScanResult:"
	idx := strings.Index(spec, header)
	if idx < 0 {
		t.Fatalf("schema %q missing", header)
	}
	rest := spec[idx+len(header):]
	endIdx := strings.Index(rest, "\n    ScannerScanBatchResponse:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after ScannerScanResult")
	}
	block := rest[:endIdx]

	// Required field list (declared on the schema's required: array).
	for _, field := range []string{
		"credential_code",
		"scanned_at",
		"result",
		"ticket_id",
		"duplicate",
		"first_admission",
	} {
		if !strings.Contains(block, "- "+field) {
			t.Errorf("ScannerScanResult.required missing field %q", field)
		}
	}

	// Nullable projection: ticket_id is documented prose-only as
	// nullable because OAS 3.1 `type: [string, "null"]` is not
	// supported by oapi-codegen v2.4.1 yet.
	if !strings.Contains(block, "        ticket_id:") {
		t.Error("ScannerScanResult schema missing nullable field ticket_id")
	}
}

// TestOpenAPI278_ScannerScanInputSchemaShape verifies the
// ScannerScanInput component schema lists every required field
// and pins the result enum to {admitted, denied}.
func TestOpenAPI278_ScannerScanInputSchemaShape(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	const header = "    ScannerScanInput:"
	idx := strings.Index(spec, header)
	if idx < 0 {
		t.Fatalf("schema %q missing", header)
	}
	rest := spec[idx+len(header):]
	endIdx := strings.Index(rest, "\n    ScannerScanBatchRequest:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after ScannerScanInput")
	}
	block := rest[:endIdx]

	for _, field := range []string{"credential_code", "scanned_at", "result"} {
		if !strings.Contains(block, "- "+field) {
			t.Errorf("ScannerScanInput.required missing field %q", field)
		}
	}
	if !strings.Contains(block, "enum: [admitted, denied]") {
		t.Error("ScannerScanInput.result missing enum: [admitted, denied]")
	}
}

// TestOpenAPI278_ScannerScanBatchRequestCap verifies that the
// ScannerScanBatchRequest schema pins the 500-entry maxItems cap
// enforced by scanner_callback.go's maxScannerBatchSize constant
// so a future raise propagates platform-wide and surfaces in the
// generated client types.
func TestOpenAPI278_ScannerScanBatchRequestCap(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	const header = "    ScannerScanBatchRequest:"
	idx := strings.Index(spec, header)
	if idx < 0 {
		t.Fatalf("schema %q missing", header)
	}
	rest := spec[idx+len(header):]
	endIdx := strings.Index(rest, "\n    ScannerScanResult:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after ScannerScanBatchRequest")
	}
	block := rest[:endIdx]

	if !strings.Contains(block, "minItems: 1") {
		t.Error("ScannerScanBatchRequest.scans missing minItems: 1 (matches scanner.empty_batch 400)")
	}
	if !strings.Contains(block, "maxItems: 500") {
		t.Error("ScannerScanBatchRequest.scans missing maxItems: 500 (matches maxScannerBatchSize in scanner_callback.go)")
	}

	// Sanity check: maxItems must mirror the Go constant — if the
	// constant changes, this assertion forces the spec edit.
	if maxScannerBatchSize != 500 {
		t.Errorf("maxScannerBatchSize = %d; openapi.yaml pins maxItems: 500 — re-sync the spec", maxScannerBatchSize)
	}
}

// TestOpenAPI278_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline example payloads for the scan_events operation, asserting
// per-handler invariants from scanner_callback.go:
//
//   - request examples: at least one example present; each entry
//     has credential_code (non-empty string), scanned_at (RFC 3339),
//     result in {admitted, denied}; gate / device_id are optional.
//   - 200 response examples: envelope shape {results: [...]} with
//     each entry exposing credential_code, scanned_at, result in
//     {admitted, denied}, duplicate (bool), first_admission (bool),
//     ticket_id (UUID-shaped string OR explicit null);
//     scan_event_id when present must be UUID-shaped;
//     when error is set, scan_event_id MUST be omitted and
//     ticket_id MUST be null (per-scan validation error path);
//     on the idempotent-replay path (duplicate=true),
//     first_admission MUST be false.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI278_SpecExamplesValidate(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("paths: section missing or not a mapping")
	}

	looksLikeUUID := func(s string) bool {
		if len(s) != 36 {
			return false
		}
		for _, i := range []int{8, 13, 18, 23} {
			if s[i] != '-' {
				return false
			}
		}
		return true
	}
	allowedResults := map[string]bool{"admitted": true, "denied": true}

	op := mustOperation(t, paths, "/v1/scanner/scan-events", "post")

	// ── Request body examples ───────────────────────────────────────
	requestBody, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents requestBody must be a mapping")
	}
	rbContent, ok := requestBody["content"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents requestBody.content must be a mapping")
	}
	rbJSON, ok := rbContent["application/json"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents requestBody.content.application/json must be a mapping")
	}
	rbExamples, ok := rbJSON["examples"].(map[string]any)
	if !ok || len(rbExamples) == 0 {
		t.Fatal("postScannerScanEvents must declare at least one request example")
	}
	for name, rawEx := range rbExamples {
		entry, ok := rawEx.(map[string]any)
		if !ok {
			t.Errorf("request example %q: entry is not a mapping", name)
			continue
		}
		value, ok := entry["value"].(map[string]any)
		if !ok {
			t.Errorf("request example %q: value is not a mapping", name)
			continue
		}
		scans, ok := value["scans"].([]any)
		if !ok || len(scans) == 0 {
			t.Errorf("request example %q: scans must be a non-empty array", name)
			continue
		}
		if len(scans) > maxScannerBatchSize {
			t.Errorf("request example %q: scans length %d exceeds maxScannerBatchSize %d",
				name, len(scans), maxScannerBatchSize)
		}
		for i, rawScan := range scans {
			scan, ok := rawScan.(map[string]any)
			if !ok {
				t.Errorf("request example %q scan %d: entry is not a mapping", name, i)
				continue
			}
			cc, _ := scan["credential_code"].(string)
			if cc == "" {
				t.Errorf("request example %q scan %d: credential_code must be a non-empty string", name, i)
			}
			sa, _ := scan["scanned_at"].(string)
			if !looksLikeRFC3339(sa) {
				t.Errorf("request example %q scan %d: scanned_at %v does not look like RFC 3339", name, i, scan["scanned_at"])
			}
			rs, _ := scan["result"].(string)
			if !allowedResults[rs] {
				t.Errorf("request example %q scan %d: result %v must be admitted|denied", name, i, scan["result"])
			}
		}
	}

	// ── 200 response examples ───────────────────────────────────────
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents responses missing")
	}
	resp200, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents must declare a 200 response")
	}
	respContent, ok := resp200["content"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents 200 response missing content")
	}
	respJSON, ok := respContent["application/json"].(map[string]any)
	if !ok {
		t.Fatal("postScannerScanEvents 200 application/json missing")
	}
	respExamples, ok := respJSON["examples"].(map[string]any)
	if !ok || len(respExamples) == 0 {
		t.Fatal("postScannerScanEvents must declare at least one 200 response example")
	}

	for name, rawEx := range respExamples {
		entry, ok := rawEx.(map[string]any)
		if !ok {
			t.Errorf("response example %q: entry is not a mapping", name)
			continue
		}
		value, ok := entry["value"].(map[string]any)
		if !ok {
			t.Errorf("response example %q: value is not a mapping", name)
			continue
		}
		results, ok := value["results"].([]any)
		if !ok || len(results) == 0 {
			t.Errorf("response example %q: results must be a non-empty array", name)
			continue
		}
		for i, rawResult := range results {
			row, ok := rawResult.(map[string]any)
			if !ok {
				t.Errorf("response example %q result %d: entry is not a mapping", name, i)
				continue
			}
			label := name + " result " + indexString(i)

			cc, _ := row["credential_code"].(string)
			if cc == "" {
				t.Errorf("%s: credential_code must be a non-empty string", label)
			}
			if _, present := row["scanned_at"].(string); !present {
				t.Errorf("%s: scanned_at must be a string (echoed verbatim)", label)
			}
			rs, _ := row["result"].(string)
			if !allowedResults[rs] {
				t.Errorf("%s: result %v must be admitted|denied", label, row["result"])
			}
			if _, present := row["duplicate"].(bool); !present {
				t.Errorf("%s: duplicate must be a boolean", label)
			}
			if _, present := row["first_admission"].(bool); !present {
				t.Errorf("%s: first_admission must be a boolean", label)
			}
			// ticket_id MUST be DECLARED (present as null OR a
			// UUID-shaped string). yaml.Unmarshal decodes `null`
			// to a literal nil interface value.
			tidRaw, present := row["ticket_id"]
			if !present {
				t.Errorf("%s: ticket_id must be declared (null or UUID-shaped string)", label)
			} else if tidRaw != nil {
				if s, ok := tidRaw.(string); !ok || !looksLikeUUID(s) {
					t.Errorf("%s: ticket_id %v must be null or UUID-shaped", label, tidRaw)
				}
			}
			// scan_event_id is optional — but when present, must
			// be UUID-shaped.
			if sidRaw, present := row["scan_event_id"]; present && sidRaw != nil {
				if s, ok := sidRaw.(string); !ok || !looksLikeUUID(s) {
					t.Errorf("%s: scan_event_id %v must be UUID-shaped when present", label, sidRaw)
				}
			}

			// Per-scan validation error path: when error is set,
			// scan_event_id MUST be omitted and ticket_id MUST be
			// null (no row inserted, no resolution attempted).
			if errStr, present := row["error"].(string); present && errStr != "" {
				if _, sidPresent := row["scan_event_id"]; sidPresent {
					t.Errorf("%s: scan_event_id must be omitted on the error path", label)
				}
				if tidRaw != nil {
					t.Errorf("%s: ticket_id must be null on the error path", label)
				}
			}
			// Idempotent-replay path (duplicate=true) MUST have
			// first_admission=false — replays suppress all side
			// effects including the first-admission flag.
			if dup, _ := row["duplicate"].(bool); dup {
				if fa, _ := row["first_admission"].(bool); fa {
					t.Errorf("%s: first_admission must be false on the duplicate=true replay path", label)
				}
			}
		}
	}
}

// indexString returns "0", "1", "2", ... for small ints to avoid
// pulling strconv into the test file just for the label suffix.
func indexString(i int) string {
	if i < 0 {
		return "?"
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	// Two-digit fall-through for the unlikely case of a >10-entry
	// example payload; keeps the helper allocation-free for the
	// common case.
	return indexString(i/10) + string(rune('0'+i%10))
}
