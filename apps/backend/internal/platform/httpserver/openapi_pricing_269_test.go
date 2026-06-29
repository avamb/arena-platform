// openapi_pricing_269_test.go pins the OpenAPI documentation contract
// established by feature #269 (Wave A-8): the pricing endpoint group
// implemented by apps/backend/internal/platform/httpserver/pricing.go
// (and pricing_calculator.go) must be documented in
// apps/backend/openapi/openapi.yaml together with its component schemas,
// permissions, error envelope, and example payloads.
//
// Coverage matches feature #269 acceptance steps:
//
//   - Step 1: path + operation entry for the GET /v1/checkout/quote
//     handler (handleQuote in pricing_calculator.go) together with
//     PricingBreakdownItem, QuoteResponseItem, and QuoteResponseEnvelope
//     component schemas.
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every status code other than 200.
//   - Step 3: minimal contract test validates the spec's response
//     examples for the pricing group against the schema (yaml parse +
//     key presence + invariants — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas live under components.schemas with the documented
//     required fields, and the pricing pipeline invariant
//     (subtotal - discount) + platform_fee + provider_fee + tax == total
//     is asserted on every response example.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI269_PathsPresent verifies step 1: the pricing path from
// pricing_calculator.go is documented under `paths:` in openapi.yaml.
func TestOpenAPI269_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/checkout/quote:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI269_OperationIDsPresent verifies the canonical operationId
// used by oapi-codegen / the TS client generator for the pricing group.
func TestOpenAPI269_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: getCheckoutQuote",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI269_SchemasPresent verifies step 4: every pricing
// component schema is declared under components.schemas.
func TestOpenAPI269_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    PricingBreakdownItem:",
		"    QuoteResponseItem:",
		"    QuoteResponseEnvelope:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI269_PermissionsDocumented verifies step 2: the pricing
// permission used by mount_commerce.go is mentioned in the spec.
func TestOpenAPI269_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"pricing.quote",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI269_BearerAuthAndTags verifies every pricing operation
// declares `bearerAuth: []` and the `v1` tag.
func TestOpenAPI269_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: getCheckoutQuote",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 8000
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

// TestOpenAPI269_ErrorEnvelopeUsed verifies step 2: every pricing
// endpoint wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI269_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: getCheckoutQuote",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			continue
		}
		end := idx + 8000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, `$ref: "#/components/schemas/ErrorEnvelope"`) {
			t.Errorf("operation %q does not reference ErrorEnvelope on any error response", op)
		}
	}
}

// TestOpenAPI269_PricingErrorCodesDocumented verifies that the spec
// documents the canonical pricing error codes emitted by handleQuote
// (pricing_calculator.go) — both the parameter-validation codes and the
// pwyw-specific codes, plus the dependency surface (promo state-machine
// + database).
func TestOpenAPI269_PricingErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"pricing.missing_params",
		"pricing.invalid_tier_id",
		"pricing.invalid_session_id",
		"pricing.invalid_org_id",
		"pricing.invalid_quantity",
		"pricing.chosen_price_required",
		"pricing.invalid_chosen_price",
		"pricing.chosen_price_below_min",
		"pricing.chosen_price_above_max",
		"pricing.tier_not_found",
		"pricing.tier_lookup_failed",
		"pricing.promo_lookup_failed",
		"pricing.unknown_pricing_mode",
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document pricing code %q", code)
		}
	}
}

// TestOpenAPI269_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks every
// response example for GET /v1/checkout/quote, asserting the per-handler
// invariants enforced by handleQuote / ComputePricing
// (pricing_calculator.go):
//
//   - Every example carries the required `quote` envelope key wrapping
//     the QuoteResponseItem.
//   - Required fields are present and have the right shapes
//     (UUID-shaped tier_id/session_id, non-empty 3-char currency,
//     non-negative integer pricing fields, quantity >= 1).
//   - The pricing pipeline invariant holds:
//     (subtotal - discount) + platform_fee + provider_fee + tax == total
//   - subtotal == unit_price * quantity (the first step of ComputePricing).
//   - discount is clamped to [0, subtotal] (cap enforced in handler).
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI269_SpecExamplesValidate(t *testing.T) {
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

	quoteOp := mustOperation(t, paths, "/v1/checkout/quote", "get")

	// Extract response examples from
	// responses["200"].content["application/json"].examples.*.value.
	responses, ok := quoteOp["responses"].(map[string]any)
	if !ok {
		t.Fatal("getCheckoutQuote: responses missing or not a mapping")
	}
	resp200, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatal("getCheckoutQuote: 200 response missing or not a mapping")
	}
	content, ok := resp200["content"].(map[string]any)
	if !ok {
		t.Fatal("getCheckoutQuote: 200 response has no content map")
	}
	js, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatal("getCheckoutQuote: 200 response has no application/json content")
	}
	examples, ok := js["examples"].(map[string]any)
	if !ok || len(examples) == 0 {
		t.Fatal("getCheckoutQuote 200 response must declare at least one example payload")
	}

	for name, raw := range examples {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("example %q: not a mapping", name)
			continue
		}
		envelope, ok := entry["value"].(map[string]any)
		if !ok {
			t.Errorf("example %q: value is not a mapping", name)
			continue
		}
		// Envelope: must wrap response under "quote" key.
		quote, ok := envelope["quote"].(map[string]any)
		if !ok {
			t.Errorf("example %q: envelope missing required `quote` key", name)
			continue
		}

		// Required fields presence.
		required := []string{
			"unit_price", "quantity", "subtotal", "discount",
			"platform_fee", "provider_fee", "tax", "total",
			"currency", "tier_id", "session_id", "promo_code",
		}
		for _, field := range required {
			if _, present := quote[field]; !present {
				t.Errorf("example %q: quote missing required field %q", name, field)
			}
		}

		// tier_id / session_id: UUID-shaped strings.
		for _, field := range []string{"tier_id", "session_id"} {
			s, ok := quote[field].(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("example %q: %s %v is not a UUID-shaped string", name, field, quote[field])
			}
		}

		// currency: ISO 4217 (3 uppercase letters).
		currency, ok := quote["currency"].(string)
		if !ok || len(currency) != 3 || strings.ToUpper(currency) != currency {
			t.Errorf("example %q: currency %v is not a 3-letter uppercase ISO 4217 code", name, quote["currency"])
		}

		// promo_code: must be present and either string or nil (null).
		if pc, present := quote["promo_code"]; present && pc != nil {
			if s, ok := pc.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("example %q: promo_code %v must be null or a non-empty string", name, pc)
			}
		}

		// Numeric fields: non-negative integers.
		nums := map[string]int64{}
		for _, field := range []string{
			"unit_price", "quantity", "subtotal", "discount",
			"platform_fee", "provider_fee", "tax", "total",
		} {
			n, ok := asInt64(quote[field])
			if !ok {
				t.Errorf("example %q: %s %v is not an integer", name, field, quote[field])
				continue
			}
			if n < 0 {
				t.Errorf("example %q: %s %d must be non-negative", name, field, n)
			}
			nums[field] = n
		}
		if len(nums) != 8 {
			// Skip invariant checks when fields are malformed; the errors
			// above already flag the underlying issue.
			continue
		}

		// quantity must be >= 1 (handler rejects 0 with pricing.invalid_quantity).
		if nums["quantity"] < 1 {
			t.Errorf("example %q: quantity %d must be >= 1 (handler rejects with pricing.invalid_quantity)", name, nums["quantity"])
		}

		// subtotal == unit_price * quantity (first step of ComputePricing).
		expectedSubtotal := nums["unit_price"] * nums["quantity"]
		if nums["subtotal"] != expectedSubtotal {
			t.Errorf("example %q: subtotal %d != unit_price (%d) * quantity (%d) = %d",
				name, nums["subtotal"], nums["unit_price"], nums["quantity"], expectedSubtotal)
		}

		// discount clamped to [0, subtotal] by ComputePricing.
		if nums["discount"] > nums["subtotal"] {
			t.Errorf("example %q: discount %d must not exceed subtotal %d (ComputePricing caps it)",
				name, nums["discount"], nums["subtotal"])
		}

		// Pipeline invariant:
		// (subtotal - discount) + platform_fee + provider_fee + tax == total
		expectedTotal := (nums["subtotal"] - nums["discount"]) +
			nums["platform_fee"] + nums["provider_fee"] + nums["tax"]
		if nums["total"] != expectedTotal {
			t.Errorf("example %q: pipeline invariant violated — total %d != (subtotal-discount)+platform_fee+provider_fee+tax = %d",
				name, nums["total"], expectedTotal)
		}
	}
}
