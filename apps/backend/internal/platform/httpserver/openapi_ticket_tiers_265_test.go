// openapi_ticket_tiers_265_test.go pins the OpenAPI documentation contract
// established by feature #265 (Wave A-4): every ticket-tier endpoint
// implemented in
// apps/backend/internal/platform/httpserver/ticket_tiers.go must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #265 acceptance steps:
//
//   - Step 1: path + operation entries for every ticket_tiers.go handler
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint
//   - Step 3: minimal contract test validates the spec's request examples
//     for the ticket-tiers group against the schema (yaml parse + key
//     presence so the test runs without docker / postgres / oapi-codegen)
//   - Step 4: schemas (TicketTierItem, TicketTierEnvelope,
//     TicketTierListResponse, TicketTierDeleteResponse,
//     CreateTicketTierRequest, UpdateTicketTierRequest) live under
//     components.schemas with the documented enums and required fields
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readOpenAPISpec265 returns the raw bytes of openapi.yaml using the shared
// path resolver from openapi_drift_test.go.
func readOpenAPISpec265(t *testing.T) string {
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

// TestOpenAPI265_PathsPresent verifies step 1: both ticket-tier paths from
// ticket_tiers.go are documented under `paths:` in openapi.yaml.
func TestOpenAPI265_PathsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	expected := []string{
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers:",
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI265_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator for the ticket-tiers group.
func TestOpenAPI265_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	expected := []string{
		"operationId: createTicketTier",
		"operationId: listTicketTiers",
		"operationId: getTicketTier",
		"operationId: updateTicketTier",
		"operationId: deleteTicketTier",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI265_SchemasPresent verifies step 4: every ticket-tier-group
// component schema is declared under components.schemas.
func TestOpenAPI265_SchemasPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	expected := []string{
		"    TicketTierItem:",
		"    TicketTierEnvelope:",
		"    TicketTierListResponse:",
		"    TicketTierDeleteResponse:",
		"    CreateTicketTierRequest:",
		"    UpdateTicketTierRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI265_PermissionsDocumented verifies step 2: every ticket-tier
// permission used by mount_catalog.go is mentioned in the spec so operators
// reading the docs can map endpoints to RBAC entries.
func TestOpenAPI265_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	for _, perm := range []string{
		"tier.read",
		"tier.create",
		"tier.update",
		"tier.delete",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI265_BearerAuthAndTags verifies every ticket-tier operation
// declares `bearerAuth: []` and the `v1` tag, matching the rest of the v1
// surface.
func TestOpenAPI265_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	for _, op := range []string{
		"operationId: createTicketTier",
		"operationId: listTicketTiers",
		"operationId: getTicketTier",
		"operationId: updateTicketTier",
		"operationId: deleteTicketTier",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 2500
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

// TestOpenAPI265_ErrorEnvelopeUsed verifies step 2: every ticket-tier
// endpoint wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI265_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	for _, op := range []string{
		"operationId: createTicketTier",
		"operationId: listTicketTiers",
		"operationId: getTicketTier",
		"operationId: updateTicketTier",
		"operationId: deleteTicketTier",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			continue
		}
		end := idx + 5000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, `$ref: "#/components/schemas/ErrorEnvelope"`) {
			t.Errorf("operation %q does not reference ErrorEnvelope on any error response", op)
		}
	}
}

// TestOpenAPI265_PricingModeEnumPinned verifies that every ticket-tier
// schema that carries a pricing_mode pins the three values the handler
// accepts (validPricingModes in ticket_tiers.go). The three schemas that
// declare pricing_mode are TicketTierItem, CreateTicketTierRequest, and
// UpdateTicketTierRequest — so the canonical enum literal must appear
// exactly three times in the spec.
func TestOpenAPI265_PricingModeEnumPinned(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec265(t)

	const enumLiteral = "enum: [free, fixed, pwyw]"
	count := strings.Count(spec, enumLiteral)
	if count < 3 {
		t.Errorf("expected %q to appear at least 3 times (TicketTierItem, "+
			"CreateTicketTierRequest, UpdateTicketTierRequest); got %d",
			enumLiteral, count)
	}

	// Belt-and-braces: each of the three schema sections must exist.
	for _, schema := range []string{
		"    TicketTierItem:",
		"    CreateTicketTierRequest:",
		"    UpdateTicketTierRequest:",
	} {
		if !strings.Contains(spec, schema) {
			t.Errorf("openapi.yaml missing %q", schema)
		}
	}
}

// TestOpenAPI265_SpecExamplesValidate parses openapi.yaml as YAML and walks
// the ticket-tiers-group request examples, asserting that:
//
//   - The createTicketTier examples carry the required fields (`name`,
//     `pricing_mode`), use a documented pricing mode, supply a positive
//     `price_amount` when `pricing_mode = fixed`, and obey
//     `pwyw_min <= pwyw_max` when both bounds are present.
//   - The updateTicketTier examples (when they carry `pricing_mode`) use
//     a value from the documented enum, and any timestamps remain
//     RFC 3339-shaped.
//
// This is the "minimal contract test" called out by step 3 — it runs in
// CI without docker / postgres / oapi-codegen because it only needs the
// YAML file itself.
func TestOpenAPI265_SpecExamplesValidate(t *testing.T) {
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

	validModes := map[string]bool{
		"free":  true,
		"fixed": true,
		"pwyw":  true,
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

	// ── POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers ──
	createOp := mustOperation(t, paths,
		"/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Fatal("createTicketTier body must declare at least one example payload")
	}
	for name, ex := range createExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("create example %q: not a mapping", name)
			continue
		}
		for _, req := range []string{"name", "pricing_mode"} {
			if _, ok := body[req]; !ok {
				t.Errorf("create example %q: missing required field %q", name, req)
			}
		}
		mode, _ := body["pricing_mode"].(string)
		if !validModes[mode] {
			t.Errorf("create example %q: pricing_mode %q not in enum", name, mode)
		}
		// pricing-mode invariants
		switch mode {
		case "fixed":
			price, ok := asInt64(body["price_amount"])
			if !ok || price <= 0 {
				t.Errorf("create example %q: pricing_mode=fixed requires price_amount > 0, got %v",
					name, body["price_amount"])
			}
		case "free":
			if v, present := body["price_amount"]; present {
				if price, ok := asInt64(v); ok && price != 0 {
					t.Errorf("create example %q: pricing_mode=free requires price_amount = 0, got %d",
						name, price)
				}
			}
		case "pwyw":
			minV, hasMin := asInt64(body["pwyw_min"])
			maxV, hasMax := asInt64(body["pwyw_max"])
			if hasMin && hasMax && minV > maxV {
				t.Errorf("create example %q: pwyw_min (%d) must be <= pwyw_max (%d)",
					name, minV, maxV)
			}
		}
		if startStr, ok := body["sale_window_start"].(string); ok && !looksLikeRFC3339(startStr) {
			t.Errorf("create example %q: sale_window_start %q is not RFC 3339", name, startStr)
		}
		if endStr, ok := body["sale_window_end"].(string); ok && !looksLikeRFC3339(endStr) {
			t.Errorf("create example %q: sale_window_end %q is not RFC 3339", name, endStr)
		}
		if cap, ok := asInt64(body["capacity"]); ok && cap <= 0 {
			t.Errorf("create example %q: capacity %d must be > 0", name, cap)
		}
	}

	// ── PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id} ──
	updateOp := mustOperation(t, paths,
		"/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", "patch")
	updateExamples := extractExamples(t, updateOp)
	if len(updateExamples) == 0 {
		t.Fatal("updateTicketTier body must declare at least one example payload")
	}
	for name, ex := range updateExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("update example %q: not a mapping", name)
			continue
		}
		if mode, ok := body["pricing_mode"].(string); ok && !validModes[mode] {
			t.Errorf("update example %q: pricing_mode %q not in enum", name, mode)
		}
		if startStr, ok := body["sale_window_start"].(string); ok && !looksLikeRFC3339(startStr) {
			t.Errorf("update example %q: sale_window_start %q is not RFC 3339", name, startStr)
		}
		if endStr, ok := body["sale_window_end"].(string); ok && !looksLikeRFC3339(endStr) {
			t.Errorf("update example %q: sale_window_end %q is not RFC 3339", name, endStr)
		}
		minV, hasMin := asInt64(body["pwyw_min"])
		maxV, hasMax := asInt64(body["pwyw_max"])
		if hasMin && hasMax && minV > maxV {
			t.Errorf("update example %q: pwyw_min (%d) must be <= pwyw_max (%d)",
				name, minV, maxV)
		}
		if cap, ok := asInt64(body["capacity"]); ok && cap <= 0 {
			t.Errorf("update example %q: capacity %d must be > 0", name, cap)
		}
	}
}
