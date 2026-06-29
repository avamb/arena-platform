// openapi_promo_codes_268_test.go pins the OpenAPI documentation contract
// established by feature #268 (Wave A-7): every promo-code endpoint
// implemented in
// apps/backend/internal/platform/httpserver/promo_codes.go must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #268 acceptance steps:
//
//   - Step 1: path + operation entries for every promo_codes.go handler
//     (CRUD under /v1/organizations/{org_id}/promo-codes plus
//     POST /v1/checkout/promo-validate).
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint.
//   - Step 3: minimal contract test validates the spec's request
//     examples for the promo-codes group against the schema (yaml parse +
//     key presence + invariants — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas (PromoCodeItem, PromoCodeEnvelope,
//     PromoCodeListResponse, PromoCodeDeleteResponse,
//     CreatePromoCodeRequest, UpdatePromoCodeRequest,
//     ValidatePromoCodeRequest, ValidatePromoCodeResponse) live under
//     components.schemas with the documented required fields.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI268_PathsPresent verifies step 1: every promo-code path
// from promo_codes.go is documented under `paths:` in openapi.yaml.
func TestOpenAPI268_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/organizations/{org_id}/promo-codes:",
		"  /v1/organizations/{org_id}/promo-codes/{id}:",
		"  /v1/checkout/promo-validate:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI268_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator for the promo-codes
// group.
func TestOpenAPI268_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: createPromoCode",
		"operationId: listPromoCodes",
		"operationId: getPromoCode",
		"operationId: updatePromoCode",
		"operationId: deletePromoCode",
		"operationId: validatePromoCode",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI268_SchemasPresent verifies step 4: every promo-codes
// component schema is declared under components.schemas.
func TestOpenAPI268_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    PromoCodeItem:",
		"    PromoCodeEnvelope:",
		"    PromoCodeListResponse:",
		"    PromoCodeDeleteResponse:",
		"    CreatePromoCodeRequest:",
		"    UpdatePromoCodeRequest:",
		"    ValidatePromoCodeRequest:",
		"    ValidatePromoCodeResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI268_PermissionsDocumented verifies step 2: every promo-code
// permission used by mount_commerce.go is mentioned in the spec.
func TestOpenAPI268_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"promo.create",
		"promo.read",
		"promo.update",
		"promo.delete",
		"promo.validate",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI268_BearerAuthAndTags verifies every promo-code operation
// declares `bearerAuth: []` and the `v1` tag.
func TestOpenAPI268_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createPromoCode",
		"operationId: listPromoCodes",
		"operationId: getPromoCode",
		"operationId: updatePromoCode",
		"operationId: deletePromoCode",
		"operationId: validatePromoCode",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 4000
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

// TestOpenAPI268_ErrorEnvelopeUsed verifies step 2: every promo-code
// endpoint wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI268_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createPromoCode",
		"operationId: listPromoCodes",
		"operationId: getPromoCode",
		"operationId: updatePromoCode",
		"operationId: deletePromoCode",
		"operationId: validatePromoCode",
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

// TestOpenAPI268_DiscountTypeEnumPinned verifies that the discount_type
// enum (percent | fixed_amount) is pinned in the three places that
// surface it: PromoCodeItem, CreatePromoCodeRequest, UpdatePromoCodeRequest.
func TestOpenAPI268_DiscountTypeEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	count := strings.Count(spec, "enum: [percent, fixed_amount]")
	if count < 3 {
		t.Errorf("expected `enum: [percent, fixed_amount]` in PromoCodeItem + Create + Update schemas (>=3 occurrences); got %d", count)
	}

	// Confirm each owning schema's window actually contains the enum so
	// later refactors can't accidentally drop it from one location.
	for _, owner := range []string{"PromoCodeItem", "CreatePromoCodeRequest", "UpdatePromoCodeRequest"} {
		window := schemaWindow(spec, owner)
		if !strings.Contains(window, "enum: [percent, fixed_amount]") {
			t.Errorf("schema %q is missing the discount_type enum pin", owner)
		}
	}
}

// TestOpenAPI268_StateMachineCodesDocumented verifies that the spec
// documents the canonical error codes emitted by the validate-promo
// handler (the state-machine-adjacent ones that callers must surface
// to buyers in the checkout UI).
func TestOpenAPI268_StateMachineCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"promo.duplicate",
		"promo.not_found",
		"promo.not_active",
		"promo.not_yet_valid",
		"promo.expired",
		"promo.invalid_order_amount",
		"promo.exhausted",
		"promo.per_customer_limit",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document promo code %q", code)
		}
	}
}

// TestOpenAPI268_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks every
// promo-codes-group request example, asserting the per-handler
// invariants enforced by promo_codes.go:
//
//   - createPromoCode examples have non-empty trimmed `code`, a
//     discount_type in {percent, fixed_amount}, a positive
//     discount_value (and <=100 when type is percent), RFC3339 date
//     window fields when present, and a non-negative min_order_amount.
//   - updatePromoCode examples either omit discount_type or use a value
//     from the enum, and any date-window string is RFC3339-shaped.
//   - validatePromoCode examples have a UUID-shaped org_id, a non-empty
//     trimmed code, a non-negative order_amount, and (when present) a
//     UUID-shaped user_id.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI268_SpecExamplesValidate(t *testing.T) {
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

	validDiscountType := func(s string) bool {
		return s == "percent" || s == "fixed_amount"
	}

	// --- createPromoCode ----------------------------------------------------
	createOp := mustOperation(t, paths, "/v1/organizations/{org_id}/promo-codes", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Fatal("createPromoCode body must declare at least one example payload")
	}
	for name, ex := range createExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("create example %q: not a mapping", name)
			continue
		}
		// code: required, non-empty after trim.
		codeRaw, present := body["code"]
		if !present {
			t.Errorf("create example %q: missing required field %q", name, "code")
		} else if s, ok := codeRaw.(string); !ok || strings.TrimSpace(s) == "" {
			t.Errorf("create example %q: code %v must be a non-empty trimmed string", name, codeRaw)
		}
		// discount_type: required, in enum.
		dtRaw, present := body["discount_type"]
		if !present {
			t.Errorf("create example %q: missing required field %q", name, "discount_type")
		} else if s, ok := dtRaw.(string); !ok || !validDiscountType(s) {
			t.Errorf("create example %q: discount_type %v must be 'percent' or 'fixed_amount'", name, dtRaw)
		}
		// discount_value: required, > 0; <=100 when percent.
		dvRaw, present := body["discount_value"]
		if !present {
			t.Errorf("create example %q: missing required field %q", name, "discount_value")
			continue
		}
		dv, ok := asInt64(dvRaw)
		if !ok {
			t.Errorf("create example %q: discount_value %v is not an integer", name, dvRaw)
			continue
		}
		if dv <= 0 {
			t.Errorf("create example %q: discount_value %d must be > 0 (handler rejects with promo.invalid_discount_value)", name, dv)
		}
		if dtStr, ok := dtRaw.(string); ok && dtStr == "percent" && (dv < 1 || dv > 100) {
			t.Errorf("create example %q: percent discount_value %d must be in [1, 100]", name, dv)
		}
		// Optional date windows: RFC3339 when present.
		for _, field := range []string{"valid_from", "valid_until"} {
			if v, present := body[field]; present && v != nil {
				s, ok := v.(string)
				if !ok || !looksLikeRFC3339(s) {
					t.Errorf("create example %q: %s %v is not RFC3339-shaped", name, field, v)
				}
			}
		}
		// Optional min_order_amount: non-negative when present.
		if v, present := body["min_order_amount"]; present {
			n, ok := asInt64(v)
			if !ok || n < 0 {
				t.Errorf("create example %q: min_order_amount %v must be a non-negative integer", name, v)
			}
		}
	}

	// --- updatePromoCode ----------------------------------------------------
	updateOp := mustOperation(t, paths, "/v1/organizations/{org_id}/promo-codes/{id}", "patch")
	updateExamples := extractExamples(t, updateOp)
	if len(updateExamples) == 0 {
		t.Fatal("updatePromoCode body must declare at least one example payload")
	}
	for name, ex := range updateExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("update example %q: not a mapping", name)
			continue
		}
		// discount_type: when present, must be in enum.
		if dt, present := body["discount_type"]; present {
			if s, ok := dt.(string); !ok || !validDiscountType(s) {
				t.Errorf("update example %q: discount_type %v must be 'percent' or 'fixed_amount'", name, dt)
			}
		}
		// date windows: RFC3339 when present.
		for _, field := range []string{"valid_from", "valid_until"} {
			if v, present := body[field]; present && v != nil {
				s, ok := v.(string)
				if !ok || !looksLikeRFC3339(s) {
					t.Errorf("update example %q: %s %v is not RFC3339-shaped", name, field, v)
				}
			}
		}
	}

	// --- validatePromoCode --------------------------------------------------
	validateOp := mustOperation(t, paths, "/v1/checkout/promo-validate", "post")
	validateExamples := extractExamples(t, validateOp)
	if len(validateExamples) == 0 {
		t.Fatal("validatePromoCode body must declare at least one example payload")
	}
	for name, ex := range validateExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("validate example %q: not a mapping", name)
			continue
		}
		// org_id: required, UUID-shaped.
		orgRaw, present := body["org_id"]
		if !present {
			t.Errorf("validate example %q: missing required field %q", name, "org_id")
		} else if s, ok := orgRaw.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("validate example %q: org_id %v is not a UUID-shaped string", name, orgRaw)
		}
		// code: required, non-empty trimmed.
		codeRaw, present := body["code"]
		if !present {
			t.Errorf("validate example %q: missing required field %q", name, "code")
		} else if s, ok := codeRaw.(string); !ok || strings.TrimSpace(s) == "" {
			t.Errorf("validate example %q: code %v must be a non-empty trimmed string", name, codeRaw)
		}
		// order_amount: required, non-negative integer.
		amtRaw, present := body["order_amount"]
		if !present {
			t.Errorf("validate example %q: missing required field %q", name, "order_amount")
		} else {
			n, ok := asInt64(amtRaw)
			if !ok || n < 0 {
				t.Errorf("validate example %q: order_amount %v must be a non-negative integer", name, amtRaw)
			}
		}
		// Optional user_id: UUID-shaped when present.
		if v, present := body["user_id"]; present {
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("validate example %q: optional user_id %v is not a UUID-shaped string", name, v)
			}
		}
	}
}
