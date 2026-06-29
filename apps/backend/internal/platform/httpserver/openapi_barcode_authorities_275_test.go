// openapi_barcode_authorities_275_test.go pins the OpenAPI documentation
// contract established by feature #275 (Wave A-14): the
// barcode_authorities endpoint group implemented in
// apps/backend/internal/platform/httpserver/barcodes.go (the
// barcode-federation file; "barcode_authorities.go" in the feature
// description is the historical group name, not a separate file) must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #275 acceptance steps:
//
//   - Step 1: path + operationId entries for the two
//     barcode_authorities handlers (handleListBarcodeAuthorities,
//     handleCreateBarcodeAuthority) together with three component
//     schemas (BarcodeAuthorityItem, BarcodeAuthorityListResponse,
//     CreateBarcodeAuthorityRequest).
//   - Step 2: permissions `barcode.read` / `barcode.create` are
//     mentioned and the standard ErrorEnvelope is wired on every
//     error status code.
//   - Step 3: minimal contract test validates the spec's example
//     payloads (both request and response) against the schema
//     invariants enforced in barcodes.go and the underlying
//     migration 0029_barcode_authorities.sql (runs in CI without
//     docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and the canonical four-value type
//     enum from migration 0029_barcode_authorities.sql
//     (`barcode_authorities_type_check`).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI275_PathsPresent verifies step 1: both barcode_authorities
// handlers are documented under `paths:` in openapi.yaml.
func TestOpenAPI275_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/barcodes/authorities:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI275_OperationIDsPresent verifies the canonical operationId
// values used by oapi-codegen / the TS client generator for each
// barcode_authorities handler.
func TestOpenAPI275_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: listBarcodeAuthorities",
		"operationId: createBarcodeAuthority",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI275_SchemasPresent verifies step 4: every barcode-authority
// component schema is declared under components.schemas.
func TestOpenAPI275_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    BarcodeAuthorityItem:",
		"    BarcodeAuthorityListResponse:",
		"    CreateBarcodeAuthorityRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI275_PermissionsDocumented verifies step 2: each handler's
// permission (as declared by mount_scanning.go) is mentioned inside its
// operation window.
func TestOpenAPI275_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	cases := []struct {
		op   string
		perm string
	}{
		{"operationId: listBarcodeAuthorities", "barcode.read"},
		{"operationId: createBarcodeAuthority", "barcode.create"},
	}
	for _, c := range cases {
		idx := strings.Index(spec, c.op)
		if idx < 0 {
			t.Errorf("%s missing", c.op)
			continue
		}
		end := idx + 6000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, c.perm) {
			t.Errorf("%s does not mention %q permission", c.op, c.perm)
		}
	}
}

// TestOpenAPI275_BearerAuthAndTags verifies that both barcode-authority
// operations declare `bearerAuth: []` and `tags: [v1]`.
func TestOpenAPI275_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listBarcodeAuthorities",
		"operationId: createBarcodeAuthority",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Fatalf("operation %q missing from openapi.yaml", op)
		}
		end := idx + 6000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, "- bearerAuth: []") {
			t.Errorf("operation %q missing `bearerAuth: []` security entry", op)
		}
		if !strings.Contains(window, "tags: [v1]") {
			t.Errorf("operation %q missing `tags: [v1]`", op)
		}
	}
}

// TestOpenAPI275_ErrorEnvelopeUsed verifies step 2: each
// barcode-authority operation wires the standard ErrorEnvelope on its
// error responses.
func TestOpenAPI275_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listBarcodeAuthorities",
		"operationId: createBarcodeAuthority",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %s missing", op)
			continue
		}
		end := idx + 6000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, `$ref: "#/components/schemas/ErrorEnvelope"`) {
			t.Errorf("operation %q does not reference ErrorEnvelope on any error response", op)
		}
	}
}

// TestOpenAPI275_BarcodeAuthorityErrorCodesDocumented verifies that the
// spec documents the canonical error codes emitted by barcodes.go for
// the two barcode_authorities handlers.
func TestOpenAPI275_BarcodeAuthorityErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"barcode.invalid_body",
		"barcode.invalid_authority_type",
		"barcode.missing_label",
		"barcode.create_authority_failed",
		"barcode.list_authorities_failed",
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document barcode code %q", code)
		}
	}
}

// TestOpenAPI275_TypeEnumPinned verifies that the
// BarcodeAuthorityItem.type and CreateBarcodeAuthorityRequest.type enums
// list every value from the barcode_authorities_type_check constraint in
// migration 0029_barcode_authorities.sql: ('platform', 'legacy_bil24',
// 'external_platform', 'guest_list').
func TestOpenAPI275_TypeEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, schema := range []string{
		"    BarcodeAuthorityItem:",
		"    CreateBarcodeAuthorityRequest:",
	} {
		idx := strings.Index(spec, schema)
		if idx < 0 {
			t.Errorf("schema %q missing", schema)
			continue
		}
		end := idx + 6000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		for _, literal := range []string{
			"- platform",
			"- legacy_bil24",
			"- external_platform",
			"- guest_list",
		} {
			if !strings.Contains(window, literal) {
				t.Errorf("schema %s type enum missing literal %q", schema, literal)
			}
		}
	}
}

// TestOpenAPI275_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline example payloads for both barcode_authorities operations,
// asserting per-handler invariants from barcodes.go and migration
// 0029_barcode_authorities.sql:
//
//   - createBarcodeAuthority request examples: required `type` is one
//     of the four migration enum values, required `label` is a
//     non-empty string.
//   - listBarcodeAuthorities response examples: top-level `authorities`
//     is a list (possibly empty); each entry has the four required
//     fields (id, type, label, created_at); `id` is UUID-shaped; `type`
//     is one of the four migration enum values; `label` is non-empty;
//     `created_at` is RFC 3339.
//   - createBarcodeAuthority 201 response example: same row-shape
//     invariants as a single list entry.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI275_SpecExamplesValidate(t *testing.T) {
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

	validType := map[string]bool{
		"platform":          true,
		"legacy_bil24":      true,
		"external_platform": true,
		"guest_list":        true,
	}

	// extractRequestExamples walks
	// op.requestBody.content["application/json"].examples and returns
	// the {name → value} map.
	extractRequestExamples := func(op map[string]any) map[string]any {
		body, ok := op["requestBody"].(map[string]any)
		if !ok {
			return nil
		}
		content, ok := body["content"].(map[string]any)
		if !ok {
			return nil
		}
		js, ok := content["application/json"].(map[string]any)
		if !ok {
			return nil
		}
		examples, ok := js["examples"].(map[string]any)
		if !ok {
			return nil
		}
		out := make(map[string]any, len(examples))
		for name, rawEx := range examples {
			entry, ok := rawEx.(map[string]any)
			if !ok {
				continue
			}
			out[name] = entry["value"]
		}
		return out
	}

	// extractResponseExamples walks op.responses[status]
	// .content["application/json"].examples and returns the
	// {name → value} map.
	extractResponseExamples := func(op map[string]any, status string) map[string]any {
		responses, ok := op["responses"].(map[string]any)
		if !ok {
			return nil
		}
		resp, ok := responses[status].(map[string]any)
		if !ok {
			return nil
		}
		content, ok := resp["content"].(map[string]any)
		if !ok {
			return nil
		}
		js, ok := content["application/json"].(map[string]any)
		if !ok {
			return nil
		}
		examples, ok := js["examples"].(map[string]any)
		if !ok {
			return nil
		}
		out := make(map[string]any, len(examples))
		for name, rawEx := range examples {
			entry, ok := rawEx.(map[string]any)
			if !ok {
				continue
			}
			out[name] = entry["value"]
		}
		return out
	}

	// Shared row-shape validator for a single BarcodeAuthorityItem.
	checkAuthorityRow := func(label string, row map[string]any) {
		t.Helper()
		// id (UUID-shaped string)
		v, present := row["id"]
		if !present {
			t.Errorf("%s: required field id missing", label)
		} else {
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("%s: id %v is not a UUID-shaped string", label, v)
			}
		}
		// type ∈ migration enum
		authType, ok := row["type"].(string)
		if !ok {
			t.Errorf("%s: type %v must be a string", label, row["type"])
		} else if !validType[authType] {
			t.Errorf("%s: type %q not in barcode_authorities_type_check (platform|legacy_bil24|external_platform|guest_list)", label, authType)
		}
		// label (non-empty string)
		lblRaw, present := row["label"]
		if !present {
			t.Errorf("%s: required field label missing", label)
		} else {
			s, ok := lblRaw.(string)
			if !ok || s == "" {
				t.Errorf("%s: label %v must be a non-empty string", label, lblRaw)
			}
		}
		// created_at (RFC 3339)
		caRaw, present := row["created_at"]
		if !present {
			t.Errorf("%s: required field created_at missing", label)
		} else {
			s, ok := caRaw.(string)
			if !ok || !looksLikeRFC3339(s) {
				t.Errorf("%s: created_at %v does not look like RFC 3339", label, caRaw)
			}
		}
	}

	// ── GET /v1/barcodes/authorities ─────────────────────────────────
	getOp := mustOperation(t, paths, "/v1/barcodes/authorities", "get")
	getExamples := extractResponseExamples(getOp, "200")
	if len(getExamples) == 0 {
		t.Fatal("listBarcodeAuthorities must declare at least one 200 response example")
	}
	for name, rawValue := range getExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("list example %q: value is not a mapping", name)
			continue
		}
		auths, ok := body["authorities"].([]any)
		if !ok {
			t.Errorf("list example %q: required field authorities must be a list", name)
			continue
		}
		for i, item := range auths {
			row, ok := item.(map[string]any)
			if !ok {
				t.Errorf("list example %q[%d]: entry is not a mapping", name, i)
				continue
			}
			checkAuthorityRow("list example "+name+"["+itoaSmall(i)+"]", row)
		}
	}

	// ── POST /v1/barcodes/authorities ────────────────────────────────
	postOp := mustOperation(t, paths, "/v1/barcodes/authorities", "post")

	// Request examples: type ∈ enum, label non-empty.
	reqExamples := extractRequestExamples(postOp)
	if len(reqExamples) == 0 {
		t.Fatal("createBarcodeAuthority must declare at least one requestBody example")
	}
	for name, rawValue := range reqExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("create request example %q: value is not a mapping", name)
			continue
		}
		authType, ok := body["type"].(string)
		if !ok {
			t.Errorf("create request example %q: type %v must be a string", name, body["type"])
		} else if !validType[authType] {
			t.Errorf("create request example %q: type %q not in barcode_authorities_type_check", name, authType)
		}
		lbl, ok := body["label"].(string)
		if !ok || lbl == "" {
			t.Errorf("create request example %q: label %v must be a non-empty string", name, body["label"])
		}
	}

	// 201 response examples: full row shape.
	createdExamples := extractResponseExamples(postOp, "201")
	if len(createdExamples) == 0 {
		t.Fatal("createBarcodeAuthority must declare at least one 201 response example")
	}
	for name, rawValue := range createdExamples {
		row, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("create 201 example %q: value is not a mapping", name)
			continue
		}
		checkAuthorityRow("create 201 example "+name, row)
	}
}

// itoaSmall returns the decimal representation of a small non-negative
// int without pulling in strconv (kept local so the package test surface
// is self-contained and does not collide with helpers in other test
// files).
func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
