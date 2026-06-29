// openapi_inventory_266_test.go pins the OpenAPI documentation contract
// established by feature #266 (Wave A-5): every inventory ledger endpoint
// implemented in apps/backend/internal/platform/httpserver/inventory.go
// must be documented in apps/backend/openapi/openapi.yaml together with
// its component schemas, permissions, error envelope, and example
// payloads.
//
// Coverage matches feature #266 acceptance steps:
//
//   - Step 1: path + operation entries for every inventory.go handler
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint
//   - Step 3: minimal contract test validates the spec's request examples
//     for the inventory group against the schema (yaml parse + key
//     presence + invariants — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas (InventoryRowItem, InventoryEnvelope,
//     InventoryListResponse, InitInventoryRequest,
//     InventoryQuantityRequest) live under components.schemas with the
//     documented required fields.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readOpenAPISpec266 returns the raw bytes of openapi.yaml using the shared
// path resolver from openapi_drift_test.go.
func readOpenAPISpec266(t *testing.T) string {
	t.Helper()
	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml at %s: %v", specPath, err)
	}
	if len(data) == 0 {
		t.Fatalf("openapi.yaml is empty at %s", specPath)
	}
	return string(data)
}

// TestOpenAPI266_PathsPresent verifies step 1: every inventory path from
// inventory.go is documented under `paths:` in openapi.yaml.
func TestOpenAPI266_PathsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	expected := []string{
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory:",
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/reserve:",
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/release:",
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/confirm:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI266_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator for the inventory group.
func TestOpenAPI266_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	expected := []string{
		"operationId: listInventory",
		"operationId: initInventory",
		"operationId: reserveInventory",
		"operationId: releaseInventory",
		"operationId: confirmInventory",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI266_SchemasPresent verifies step 4: every inventory-group
// component schema is declared under components.schemas.
func TestOpenAPI266_SchemasPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	expected := []string{
		"    InventoryRowItem:",
		"    InventoryEnvelope:",
		"    InventoryListResponse:",
		"    InitInventoryRequest:",
		"    InventoryQuantityRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI266_PermissionsDocumented verifies step 2: every inventory
// permission used by mount_inventory.go is mentioned in the spec.
func TestOpenAPI266_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	for _, perm := range []string{
		"inventory.read",
		"inventory.reserve",
		"inventory.release",
		"inventory.confirm",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI266_BearerAuthAndTags verifies every inventory operation
// declares `bearerAuth: []` and the `v1` tag, matching the rest of the v1
// surface.
func TestOpenAPI266_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	for _, op := range []string{
		"operationId: listInventory",
		"operationId: initInventory",
		"operationId: reserveInventory",
		"operationId: releaseInventory",
		"operationId: confirmInventory",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 3500
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

// TestOpenAPI266_ErrorEnvelopeUsed verifies step 2: every inventory
// endpoint wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI266_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	for _, op := range []string{
		"operationId: listInventory",
		"operationId: initInventory",
		"operationId: reserveInventory",
		"operationId: releaseInventory",
		"operationId: confirmInventory",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
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

// TestOpenAPI266_ConflictCodesDocumented verifies that the three
// conflict-prone operations (reserve / release / confirm) document the
// 409 error codes emitted by inventory.go in their response descriptions.
func TestOpenAPI266_ConflictCodesDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec266(t)

	for _, code := range []string{
		"inventory.over_capacity",
		"inventory.insufficient_held",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document conflict code %q", code)
		}
	}
}

// TestOpenAPI266_SpecExamplesValidate parses openapi.yaml as YAML and
// walks the inventory-group request examples, asserting that:
//
//   - The initInventory examples either omit `capacity_total` / set it to
//     null (unlimited) or carry a non-negative integer.
//   - The reserveInventory / releaseInventory / confirmInventory examples
//     each carry a positive integer `quantity` (the handler returns
//     400 `inventory.invalid_quantity` for `quantity <= 0`).
//
// This is the "minimal contract test" called out by step 3 — it runs in
// CI without docker / postgres / oapi-codegen because it only needs the
// YAML file itself.
func TestOpenAPI266_SpecExamplesValidate(t *testing.T) {
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

	// asInt64 coerces yaml numeric scalar types to int64.
	asInt64 := func(v any) (int64, bool) {
		switch n := v.(type) {
		case int:
			return int64(n), true
		case int64:
			return n, true
		case float64:
			return int64(n), true
		}
		return 0, false
	}

	// ── POST .../sessions/{session_id}/inventory (initInventory) ──
	initOp := mustOperation(t, paths,
		"/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory", "post")
	initExamples := extractExamples(t, initOp)
	if len(initExamples) == 0 {
		t.Fatal("initInventory body must declare at least one example payload")
	}
	for name, ex := range initExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("init example %q: not a mapping", name)
			continue
		}
		// capacity_total is optional; when present must be either null or
		// a non-negative integer.
		if v, present := body["capacity_total"]; present && v != nil {
			cap, ok := asInt64(v)
			if !ok {
				t.Errorf("init example %q: capacity_total %v is not an integer", name, v)
				continue
			}
			if cap < 0 {
				t.Errorf("init example %q: capacity_total %d must be >= 0", name, cap)
			}
		}
	}

	// ── reserve / release / confirm bodies all share the same shape ──
	for _, segment := range []string{"reserve", "release", "confirm"} {
		opPath := "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/" + segment
		op := mustOperation(t, paths, opPath, "post")
		examples := extractExamples(t, op)
		if len(examples) == 0 {
			t.Fatalf("%s body must declare at least one example payload", segment)
		}
		for name, ex := range examples {
			body, ok := ex.(map[string]any)
			if !ok {
				t.Errorf("%s example %q: not a mapping", segment, name)
				continue
			}
			qRaw, present := body["quantity"]
			if !present {
				t.Errorf("%s example %q: missing required field \"quantity\"", segment, name)
				continue
			}
			q, ok := asInt64(qRaw)
			if !ok {
				t.Errorf("%s example %q: quantity %v is not an integer", segment, name, qRaw)
				continue
			}
			if q <= 0 {
				t.Errorf("%s example %q: quantity %d must be > 0 (handler rejects with inventory.invalid_quantity)",
					segment, name, q)
			}
		}
	}
}
