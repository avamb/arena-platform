// openapi_payment_intents_271_test.go pins the OpenAPI documentation
// contract established by feature #271 (Wave A-10): the payment_intents
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/payment_intents.go must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #271 acceptance steps:
//
//   - Step 1: path + operationId entries for every payment_intents.go
//     handler (handleCreatePaymentIntent, handleGetPaymentIntent,
//     handleTransitionPaymentIntent, handlePaymentIntentWebhook)
//     together with the PaymentIntentItem, PaymentIntentEnvelope,
//     CreatePaymentIntentRequest, TransitionPaymentIntentRequest,
//     PaymentIntentWebhookRequest, and PaymentIntentWebhookAck
//     component schemas.
//   - Step 2: permissions (`payment_intent.create`, `.read`, `.update`)
//     are mentioned, the standard ErrorEnvelope is wired on every
//     status code other than 2xx, and the webhook endpoint is
//     documented as unauthenticated.
//   - Step 3: minimal contract test validates the spec's request
//     example payloads against the schema (yaml parse + key presence
//   - invariants from payment_intents.go — runs without docker /
//     postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and enum literals (the canonical
//     seven-value payment-intent state enum from
//     validPaymentIntentTransitions).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI271_PathsPresent verifies step 1: every payment_intents.go
// handler is documented under `paths:` in openapi.yaml.
func TestOpenAPI271_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/payment-intents:",
		"  /v1/payment-intents/{id}:",
		"  /v1/payment-intents/{id}/transition:",
		"  /v1/payment-intents/webhook:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI271_OperationIDsPresent verifies the canonical operationId
// used by oapi-codegen / the TS client generator for each payment
// intent handler.
func TestOpenAPI271_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: createPaymentIntent",
		"operationId: getPaymentIntent",
		"operationId: transitionPaymentIntent",
		"operationId: paymentIntentWebhook",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI271_SchemasPresent verifies step 4: every payment intent
// component schema is declared under components.schemas.
func TestOpenAPI271_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    PaymentIntentItem:",
		"    PaymentIntentEnvelope:",
		"    CreatePaymentIntentRequest:",
		"    TransitionPaymentIntentRequest:",
		"    PaymentIntentWebhookRequest:",
		"    PaymentIntentWebhookAck:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI271_PermissionsDocumented verifies step 2: every permission
// declared by mount_commerce.go for the payment_intents group is
// mentioned in the spec.
func TestOpenAPI271_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"payment_intent.create",
		"payment_intent.read",
		"payment_intent.update",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI271_BearerAuthAndTags verifies the three authenticated
// payment intent operations declare `bearerAuth: []` and `tags: [v1]`,
// and the webhook operation declares `tags: [v1]` but does NOT carry a
// bearerAuth security entry (it is unauthenticated by design).
func TestOpenAPI271_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	authedOps := []string{
		"operationId: createPaymentIntent",
		"operationId: getPaymentIntent",
		"operationId: transitionPaymentIntent",
	}
	for _, op := range authedOps {
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

	// Webhook: tags present but no bearerAuth entry within its
	// operation window (before responses:).
	whIdx := strings.Index(spec, "operationId: paymentIntentWebhook")
	if whIdx < 0 {
		t.Fatal("operation paymentIntentWebhook missing")
	}
	// Look at the body of the operation up to responses:.
	tail := spec[whIdx:]
	rspIdx := strings.Index(tail, "responses:")
	if rspIdx < 0 {
		t.Fatal("webhook operation missing responses: section")
	}
	whHeader := tail[:rspIdx]
	if !strings.Contains(whHeader, "tags: [v1]") {
		t.Error("webhook operation missing `tags: [v1]`")
	}
	if strings.Contains(whHeader, "- bearerAuth: []") {
		t.Error("webhook operation must NOT declare bearerAuth (handler is unauthenticated)")
	}
}

// TestOpenAPI271_ErrorEnvelopeUsed verifies step 2: every payment
// intent endpoint wires the standard ErrorEnvelope on its error
// responses.
func TestOpenAPI271_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createPaymentIntent",
		"operationId: getPaymentIntent",
		"operationId: transitionPaymentIntent",
		"operationId: paymentIntentWebhook",
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

// TestOpenAPI271_PaymentIntentErrorCodesDocumented verifies that the
// spec documents the canonical payment_intent / webhook error codes
// emitted by payment_intents.go, covering all four handlers.
func TestOpenAPI271_PaymentIntentErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		// create
		"payment_intent.invalid_body",
		"payment_intent.empty_body",
		"payment_intent.invalid_json",
		"payment_intent.invalid_org_id",
		"payment_intent.missing_provider",
		"payment_intent.invalid_amount",
		"payment_intent.missing_currency",
		"payment_intent.invalid_initial_state",
		"payment_intent.invalid_checkout_session_id",
		"payment_intent.create_failed",
		// get
		"payment_intent.invalid_id",
		"payment_intent.not_found",
		"payment_intent.get_failed",
		// transition
		"payment_intent.missing_state",
		"payment_intent.missing_sca_redirect_url",
		"payment_intent.terminal_state",
		"payment_intent.invalid_transition",
		"payment_intent.fetch_failed",
		"payment_intent.transition_failed",
		// webhook
		"webhook.invalid_body",
		"webhook.empty_body",
		"webhook.invalid_json",
		"webhook.missing_provider_payment_id",
		"webhook.missing_event_type",
		"webhook.intent_not_found",
		"webhook.lookup_failed",
		"webhook.event_record_failed",
		"webhook.state_update_failed",
		// dependency
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document payment intent code %q", code)
		}
	}
}

// TestOpenAPI271_StateMachineEnumPinned verifies that the
// PaymentIntentItem.state enum lists every state from the
// validPaymentIntentTransitions map in payment_intents.go.
func TestOpenAPI271_StateMachineEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	idx := strings.Index(spec, "    PaymentIntentItem:")
	if idx < 0 {
		t.Fatal("PaymentIntentItem schema missing")
	}
	end := idx + 8000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, state := range []string{
		"- created",
		"- requires_action",
		"- processing",
		"- authorized",
		"- manual_review",
		"- succeeded",
		"- failed",
	} {
		if !strings.Contains(window, state) {
			t.Errorf("PaymentIntentItem.state enum missing literal %q", state)
		}
	}

	// Cross-check: every key in validPaymentIntentTransitions appears in
	// the enum window. This pins the YAML enum directly to the source-of-
	// truth map, so adding a new state to the state machine without
	// updating the spec fails this test.
	for state := range validPaymentIntentTransitions {
		needle := "- " + state
		if !strings.Contains(window, needle) {
			t.Errorf("PaymentIntentItem.state enum missing state machine literal %q", needle)
		}
	}
}

// TestOpenAPI271_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline request-body examples for the three POST handlers that
// declare a request body, asserting per-handler invariants enforced in
// payment_intents.go:
//
//   - create: org_id is UUID-shaped; provider non-empty; amount >= 0;
//     currency is a 3-char string; initial_state (when present) is a
//     non-terminal state from validPaymentIntentTransitions;
//     checkout_session_id (when present) is UUID-shaped; transitioning
//     a fresh `requires_action` intent into existence requires
//     sca_redirect_url.
//   - transition: state is a valid state machine target; setting state
//     to `requires_action` requires sca_redirect_url (handler rejects
//     with payment_intent.missing_sca_redirect_url otherwise); failure_*
//     fields when present are non-empty strings.
//   - webhook: provider_payment_id and event_type are both non-empty
//     strings (handler rejects with webhook.missing_* otherwise).
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI271_SpecExamplesValidate(t *testing.T) {
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

	// ── POST /v1/payment-intents ─────────────────────────────────────
	createOp := mustOperation(t, paths, "/v1/payment-intents", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Error("createPaymentIntent requestBody must declare at least one example")
	}
	for name, rawValue := range createExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("create example %q: value is not a mapping", name)
			continue
		}
		orgID, ok := value["org_id"].(string)
		if !ok || !looksLikeUUID(orgID) {
			t.Errorf("create example %q: org_id %v is not a UUID-shaped string", name, value["org_id"])
		}
		provider, ok := value["provider"].(string)
		if !ok || strings.TrimSpace(provider) == "" {
			t.Errorf("create example %q: provider %v must be a non-empty string (handler rejects with payment_intent.missing_provider)", name, value["provider"])
		}
		amount, ok := asInt64(value["amount"])
		if !ok {
			t.Errorf("create example %q: amount %v is not an integer", name, value["amount"])
		} else if amount < 0 {
			t.Errorf("create example %q: amount %d must be non-negative (handler rejects with payment_intent.invalid_amount)", name, amount)
		}
		currency, ok := value["currency"].(string)
		if !ok || len(currency) != 3 {
			t.Errorf("create example %q: currency %v must be a 3-char string (handler rejects empty with payment_intent.missing_currency)", name, value["currency"])
		}
		if csid, present := value["checkout_session_id"]; present && csid != nil {
			if s, ok := csid.(string); !ok || !looksLikeUUID(s) {
				t.Errorf("create example %q: checkout_session_id %v must be null or UUID-shaped (handler rejects with payment_intent.invalid_checkout_session_id)", name, csid)
			}
		}
		if is, present := value["initial_state"]; present && is != nil {
			s, ok := is.(string)
			if !ok {
				t.Errorf("create example %q: initial_state %v must be a string", name, is)
				continue
			}
			targets, exists := validPaymentIntentTransitions[s]
			if !exists {
				t.Errorf("create example %q: initial_state %q is not a known state (handler rejects with payment_intent.invalid_initial_state)", name, s)
				continue
			}
			if len(targets) == 0 {
				// Terminal state — handler rejects.
				t.Errorf("create example %q: initial_state %q is terminal; handler rejects with payment_intent.invalid_initial_state", name, s)
			}
			if s == "requires_action" {
				if url, _ := value["sca_redirect_url"].(string); strings.TrimSpace(url) == "" {
					t.Errorf("create example %q: initial_state requires_action must carry a non-empty sca_redirect_url", name)
				}
			}
		}
	}

	// ── POST /v1/payment-intents/{id}/transition ─────────────────────
	transitionOp := mustOperation(t, paths, "/v1/payment-intents/{id}/transition", "post")
	transitionExamples := extractExamples(t, transitionOp)
	if len(transitionExamples) == 0 {
		t.Error("transitionPaymentIntent requestBody must declare at least one example")
	}
	for name, rawValue := range transitionExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("transition example %q: value is not a mapping", name)
			continue
		}
		state, ok := value["state"].(string)
		if !ok || strings.TrimSpace(state) == "" {
			t.Errorf("transition example %q: state %v must be a non-empty string (handler rejects with payment_intent.missing_state)", name, value["state"])
			continue
		}
		// Must be reachable somewhere in the state machine.
		reachable := false
		for _, targets := range validPaymentIntentTransitions {
			if targets[state] {
				reachable = true
				break
			}
		}
		if !reachable {
			t.Errorf("transition example %q: state %q is not a valid transition target in validPaymentIntentTransitions", name, state)
		}
		if state == "requires_action" {
			if url, _ := value["sca_redirect_url"].(string); strings.TrimSpace(url) == "" {
				t.Errorf("transition example %q: state=requires_action requires non-empty sca_redirect_url (handler rejects with payment_intent.missing_sca_redirect_url)", name)
			}
		}
		for _, field := range []string{"failure_code", "failure_message", "client_secret", "provider_payment_id", "sca_redirect_url"} {
			if v, present := value[field]; present && v != nil {
				if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
					t.Errorf("transition example %q: %s %v must be null or a non-empty string", name, field, v)
				}
			}
		}
	}

	// ── POST /v1/payment-intents/webhook ─────────────────────────────
	webhookOp := mustOperation(t, paths, "/v1/payment-intents/webhook", "post")
	webhookExamples := extractExamples(t, webhookOp)
	if len(webhookExamples) == 0 {
		t.Error("paymentIntentWebhook requestBody must declare at least one example")
	}
	for name, rawValue := range webhookExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("webhook example %q: value is not a mapping", name)
			continue
		}
		ppi, ok := value["provider_payment_id"].(string)
		if !ok || strings.TrimSpace(ppi) == "" {
			t.Errorf("webhook example %q: provider_payment_id %v must be a non-empty string (handler rejects with webhook.missing_provider_payment_id)", name, value["provider_payment_id"])
		}
		et, ok := value["event_type"].(string)
		if !ok || strings.TrimSpace(et) == "" {
			t.Errorf("webhook example %q: event_type %v must be a non-empty string (handler rejects with webhook.missing_event_type)", name, value["event_type"])
		}
		// If target_state is provided, it must be a known state machine target.
		if ts, present := value["target_state"]; present && ts != nil {
			s, ok := ts.(string)
			if !ok {
				t.Errorf("webhook example %q: target_state %v must be a string", name, ts)
				continue
			}
			reachable := false
			for _, targets := range validPaymentIntentTransitions {
				if targets[s] {
					reachable = true
					break
				}
			}
			if !reachable {
				t.Errorf("webhook example %q: target_state %q is not a valid state machine target", name, s)
			}
		}
	}
}
