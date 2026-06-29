// openapi_checkout_sessions_270_test.go pins the OpenAPI documentation
// contract established by feature #270 (Wave A-9): the checkout_sessions
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/checkout.go must be documented
// in apps/backend/openapi/openapi.yaml together with its component schemas,
// permissions, error envelope, and example payloads.
//
// Coverage matches feature #270 acceptance steps:
//
//   - Step 1: path + operationId entries for every checkout.go handler
//     (handleStartCheckout, handleGetCheckoutSession, handleConfirmCheckout,
//     handleCompleteCheckout, handleAbandonCheckout) together with the
//     CheckoutSessionItem, CheckoutSessionEnvelope, StartCheckoutRequest,
//     ConfirmCheckoutRequest, and CompleteCheckoutRequest component schemas.
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every status code other than 2xx.
//   - Step 3: minimal contract test validates the spec's request example
//     payloads against the schema (yaml parse + key presence + invariants —
//     runs without docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the documented
//     required fields and enum literals (the canonical seven-value
//     checkout-session state enum).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI270_PathsPresent verifies step 1: every checkout.go handler is
// documented under `paths:` in openapi.yaml.
func TestOpenAPI270_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/checkout/start:",
		"  /v1/checkout/{id}:",
		"  /v1/checkout/{id}/confirm:",
		"  /v1/checkout/{id}/complete:",
		"  /v1/checkout/{id}/abandon:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI270_OperationIDsPresent verifies the canonical operationId
// used by oapi-codegen / the TS client generator for each checkout handler.
func TestOpenAPI270_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: startCheckoutSession",
		"operationId: getCheckoutSession",
		"operationId: confirmCheckoutSession",
		"operationId: completeCheckoutSession",
		"operationId: abandonCheckoutSession",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI270_SchemasPresent verifies step 4: every checkout component
// schema is declared under components.schemas.
func TestOpenAPI270_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    CheckoutSessionItem:",
		"    CheckoutSessionEnvelope:",
		"    StartCheckoutRequest:",
		"    ConfirmCheckoutRequest:",
		"    CompleteCheckoutRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI270_PermissionsDocumented verifies step 2: every permission
// declared by mount_commerce.go for the checkout group is mentioned in
// the spec.
func TestOpenAPI270_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"checkout.start",
		"checkout.read",
		"checkout.confirm",
		"checkout.complete",
		"checkout.abandon",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI270_BearerAuthAndTags verifies every checkout operation
// declares `bearerAuth: []` and the `v1` tag.
func TestOpenAPI270_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: startCheckoutSession",
		"operationId: getCheckoutSession",
		"operationId: confirmCheckoutSession",
		"operationId: completeCheckoutSession",
		"operationId: abandonCheckoutSession",
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

// TestOpenAPI270_ErrorEnvelopeUsed verifies step 2: every checkout
// endpoint wires the standard ErrorEnvelope on its error responses.
func TestOpenAPI270_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: startCheckoutSession",
		"operationId: getCheckoutSession",
		"operationId: confirmCheckoutSession",
		"operationId: completeCheckoutSession",
		"operationId: abandonCheckoutSession",
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

// TestOpenAPI270_CheckoutErrorCodesDocumented verifies that the spec
// documents the canonical checkout error codes emitted by checkout.go,
// covering all five handlers plus the promo-state propagation surface
// in handleConfirmCheckout and the database/tier dependency codes.
func TestOpenAPI270_CheckoutErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		// start
		"checkout.invalid_body",
		"checkout.empty_body",
		"checkout.invalid_json",
		"checkout.invalid_org_id",
		"checkout.invalid_channel_id",
		"checkout.invalid_reservation_id",
		"checkout.invalid_user_id",
		"checkout.start_failed",
		// get
		"checkout.invalid_id",
		"checkout.not_found",
		"checkout.get_failed",
		// confirm
		"checkout.invalid_tier_id",
		"checkout.invalid_session_id",
		"checkout.invalid_quantity",
		"checkout.chosen_price_required",
		"checkout.invalid_chosen_price",
		"checkout.chosen_price_below_min",
		"checkout.chosen_price_above_max",
		"checkout.tier_not_found",
		"checkout.tier_lookup_failed",
		"checkout.promo_lookup_failed",
		"checkout.unknown_pricing_mode",
		"checkout.invalid_transition",
		"checkout.confirm_failed",
		// complete
		"checkout.missing_payment_intent",
		"checkout.missing_payment_provider",
		"checkout.payment_required",
		"checkout.complete_failed",
		// abandon
		"checkout.already_terminal",
		"checkout.abandon_failed",
		// dependency / promo propagation
		"dependency.database_unavailable",
		"dependency.tier_unavailable",
		"promo.not_found",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document checkout code %q", code)
		}
	}
}

// TestOpenAPI270_StateMachineEnumPinned verifies that the
// CheckoutSessionItem.state enum lists every state in the
// validCheckoutTransitions map from checkout.go.
func TestOpenAPI270_StateMachineEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	idx := strings.Index(spec, "    CheckoutSessionItem:")
	if idx < 0 {
		t.Fatal("CheckoutSessionItem schema missing")
	}
	end := idx + 8000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, state := range []string{
		"- created",
		"- pricing_confirmed",
		"- payment_started",
		"- completed",
		"- abandoned",
		"- expired",
		"- manual_review",
	} {
		if !strings.Contains(window, state) {
			t.Errorf("CheckoutSessionItem.state enum missing literal %q", state)
		}
	}
}

// TestOpenAPI270_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline request-body examples for the four POST handlers, asserting
// the per-handler validation invariants enforced in checkout.go:
//
//   - start: org_id / channel_id / reservation_id UUID-shaped; user_id
//     either null/absent or UUID-shaped.
//   - confirm: org_id / tier_id / session_id UUID-shaped; quantity >= 1
//     (handler rejects 0 with checkout.invalid_quantity); chosen_price
//     when present is a non-negative integer.
//   - complete: when payment_provider is set, payment_intent_id must
//     also be a non-empty string (handler rejects with
//     checkout.missing_payment_intent otherwise).
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI270_SpecExamplesValidate(t *testing.T) {
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

	// ── /v1/checkout/start ────────────────────────────────────────────
	startOp := mustOperation(t, paths, "/v1/checkout/start", "post")
	startExamples := extractExamples(t, startOp)
	if len(startExamples) == 0 {
		t.Error("startCheckoutSession requestBody must declare at least one example")
	}
	for name, raw := range startExamples {
		value, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("start example %q: value is not a mapping", name)
			continue
		}
		for _, field := range []string{"org_id", "channel_id", "reservation_id"} {
			s, ok := value[field].(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("start example %q: %s %v is not a UUID-shaped string", name, field, value[field])
			}
		}
		if uid, present := value["user_id"]; present && uid != nil {
			if s, ok := uid.(string); !ok || !looksLikeUUID(s) {
				t.Errorf("start example %q: user_id %v must be null or UUID-shaped", name, uid)
			}
		}
	}

	// ── /v1/checkout/{id}/confirm ─────────────────────────────────────
	confirmOp := mustOperation(t, paths, "/v1/checkout/{id}/confirm", "post")
	confirmExamples := extractExamples(t, confirmOp)
	if len(confirmExamples) == 0 {
		t.Error("confirmCheckoutSession requestBody must declare at least one example")
	}
	for name, raw := range confirmExamples {
		value, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("confirm example %q: value is not a mapping", name)
			continue
		}
		for _, field := range []string{"org_id", "tier_id", "session_id"} {
			s, ok := value[field].(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("confirm example %q: %s %v is not a UUID-shaped string", name, field, value[field])
			}
		}
		qty, ok := asInt64(value["quantity"])
		if !ok {
			t.Errorf("confirm example %q: quantity %v is not an integer", name, value["quantity"])
			continue
		}
		if qty < 1 {
			t.Errorf("confirm example %q: quantity %d must be >= 1 (handler rejects with checkout.invalid_quantity)", name, qty)
		}
		if cp, present := value["chosen_price"]; present && cp != nil {
			n, ok := asInt64(cp)
			if !ok {
				t.Errorf("confirm example %q: chosen_price %v must be integer or null", name, cp)
			} else if n < 0 {
				t.Errorf("confirm example %q: chosen_price %d must be non-negative", name, n)
			}
		}
		if pc, present := value["promo_code"]; present && pc != nil {
			if s, ok := pc.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("confirm example %q: promo_code %v must be null or a non-empty string", name, pc)
			}
		}
	}

	// ── /v1/checkout/{id}/complete ────────────────────────────────────
	completeOp := mustOperation(t, paths, "/v1/checkout/{id}/complete", "post")
	completeExamples := extractExamples(t, completeOp)
	if len(completeExamples) == 0 {
		t.Error("completeCheckoutSession requestBody must declare at least one example")
	}
	for name, raw := range completeExamples {
		value, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("complete example %q: value is not a mapping", name)
			continue
		}
		// Handler rejects payment_provider without payment_intent_id
		// (checkout.missing_payment_intent).
		provider, hasProvider := value["payment_provider"].(string)
		intent, hasIntent := value["payment_intent_id"].(string)
		if hasProvider && strings.TrimSpace(provider) != "" {
			if !hasIntent || strings.TrimSpace(intent) == "" {
				t.Errorf("complete example %q: payment_provider %q present without non-empty payment_intent_id (handler rejects with checkout.missing_payment_intent)", name, provider)
			}
		}
	}
}
