// openapi_refunds_274_test.go pins the OpenAPI documentation contract
// established by feature #274 (Wave A-13): the refunds endpoint group
// implemented in apps/backend/internal/platform/httpserver/refunds.go
// must be documented in apps/backend/openapi/openapi.yaml together
// with its component schemas, permissions, error envelope, and
// example payloads.
//
// Coverage matches feature #274 acceptance steps:
//
//   - Step 1: path + operationId entries for every refunds.go handler
//     (handleCreateRefund, handleGetRefund, handleApproveRefund,
//     handleRejectRefund, handleRefundWebhook) together with the
//     RefundItem, RefundEnvelope, CreateRefundRequest,
//     ApproveRefundRequest, RefundWebhookRequest, and
//     RefundWebhookAck component schemas.
//   - Step 2: permissions (`refund.create`, `.read`, `.approve`) are
//     mentioned, the standard ErrorEnvelope is wired on every status
//     code other than 2xx, and the webhook endpoint is documented as
//     unauthenticated.
//   - Step 3: minimal contract test validates the spec's request
//     example payloads against the schema (yaml parse + key presence
//     + invariants from refunds.go — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and enum literals (the canonical
//     seven-value refund state enum from validRefundTransitions).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI274_PathsPresent verifies step 1: every refunds.go
// handler is documented under `paths:` in openapi.yaml.
func TestOpenAPI274_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/refunds:",
		"  /v1/refunds/{id}:",
		"  /v1/refunds/{id}/approve:",
		"  /v1/refunds/{id}/reject:",
		"  /v1/refunds/webhook:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI274_OperationIDsPresent verifies the canonical
// operationId used by oapi-codegen / the TS client generator for each
// refund handler.
func TestOpenAPI274_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: createRefund",
		"operationId: getRefund",
		"operationId: approveRefund",
		"operationId: rejectRefund",
		"operationId: refundWebhook",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI274_SchemasPresent verifies step 4: every refund
// component schema is declared under components.schemas.
func TestOpenAPI274_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    RefundItem:",
		"    RefundEnvelope:",
		"    CreateRefundRequest:",
		"    ApproveRefundRequest:",
		"    RefundWebhookRequest:",
		"    RefundWebhookAck:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI274_PermissionsDocumented verifies step 2: every
// permission declared by mount_commerce.go for the refunds group is
// mentioned in the spec.
func TestOpenAPI274_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"refund.create",
		"refund.read",
		"refund.approve",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI274_BearerAuthAndTags verifies the four authenticated
// refund operations declare `bearerAuth: []` and `tags: [v1]`, and
// the webhook operation declares `tags: [v1]` but does NOT carry a
// bearerAuth security entry (it is unauthenticated by design).
func TestOpenAPI274_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	authedOps := []string{
		"operationId: createRefund",
		"operationId: getRefund",
		"operationId: approveRefund",
		"operationId: rejectRefund",
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
	whIdx := strings.Index(spec, "operationId: refundWebhook")
	if whIdx < 0 {
		t.Fatal("operation refundWebhook missing")
	}
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

// TestOpenAPI274_ErrorEnvelopeUsed verifies step 2: every refund
// endpoint wires the standard ErrorEnvelope on its error responses.
func TestOpenAPI274_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createRefund",
		"operationId: getRefund",
		"operationId: approveRefund",
		"operationId: rejectRefund",
		"operationId: refundWebhook",
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

// TestOpenAPI274_RefundErrorCodesDocumented verifies that the spec
// documents the canonical refund / refund_webhook error codes emitted
// by refunds.go, covering all five handlers.
func TestOpenAPI274_RefundErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		// create
		"refund.invalid_body",
		"refund.empty_body",
		"refund.invalid_json",
		"refund.invalid_payment_intent_id",
		"refund.invalid_amount",
		"refund.missing_currency",
		"refund.payment_intent_not_found",
		"refund.pi_lookup_failed",
		"refund.create_failed",
		// get
		"refund.invalid_id",
		"refund.not_found",
		"refund.get_failed",
		// approve / reject
		"refund.invalid_state",
		"refund.fetch_failed",
		"refund.transition_failed",
		// webhook
		"refund_webhook.invalid_body",
		"refund_webhook.empty_body",
		"refund_webhook.invalid_json",
		"refund_webhook.missing_provider_refund_id",
		"refund_webhook.missing_event_type",
		"refund_webhook.missing_refund_id",
		"refund_webhook.invalid_refund_id",
		"refund_webhook.refund_not_found",
		"refund_webhook.lookup_failed",
		"refund_webhook.event_record_failed",
		"refund_webhook.state_update_failed",
		// dependency
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document refund code %q", code)
		}
	}
}

// TestOpenAPI274_StateMachineEnumPinned verifies that the
// RefundItem.state enum lists every state from the
// validRefundTransitions map in refunds.go.
func TestOpenAPI274_StateMachineEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	idx := strings.Index(spec, "    RefundItem:")
	if idx < 0 {
		t.Fatal("RefundItem schema missing")
	}
	end := idx + 8000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, state := range []string{
		"- requested",
		"- approved",
		"- provider_pending",
		"- manual_review",
		"- succeeded",
		"- failed",
		"- rejected",
	} {
		if !strings.Contains(window, state) {
			t.Errorf("RefundItem.state enum missing literal %q", state)
		}
	}

	// Cross-check: every key in validRefundTransitions appears in
	// the enum window. This pins the YAML enum directly to the
	// source-of-truth map, so adding a new state to the state
	// machine without updating the spec fails this test.
	for state := range validRefundTransitions {
		needle := "- " + state
		if !strings.Contains(window, needle) {
			t.Errorf("RefundItem.state enum missing state machine literal %q", needle)
		}
	}
}

// TestOpenAPI274_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline request-body examples for the four POST handlers that
// declare a request body, asserting per-handler invariants enforced
// in refunds.go:
//
//   - create: payment_intent_id is UUID-shaped; amount is a positive
//     integer (handler rejects amount<=0 with refund.invalid_amount);
//     currency is a 3-char string (empty rejected with
//     refund.missing_currency).
//   - approve / reject: optional body, when notes is present it must
//     be a non-empty string.
//   - webhook: provider_refund_id, event_type and refund_id are all
//     non-empty strings (handler rejects with refund_webhook.missing_*
//     otherwise); refund_id is UUID-shaped; if target_state is
//     supplied it must be a valid state machine target.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI274_SpecExamplesValidate(t *testing.T) {
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

	// ── POST /v1/refunds ──────────────────────────────────────────
	createOp := mustOperation(t, paths, "/v1/refunds", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Error("createRefund requestBody must declare at least one example")
	}
	for name, rawValue := range createExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("create example %q: value is not a mapping", name)
			continue
		}
		pid, ok := value["payment_intent_id"].(string)
		if !ok || !looksLikeUUID(pid) {
			t.Errorf("create example %q: payment_intent_id %v is not a UUID-shaped string (handler rejects with refund.invalid_payment_intent_id)", name, value["payment_intent_id"])
		}
		amount, ok := asInt64(value["amount"])
		if !ok {
			t.Errorf("create example %q: amount %v is not an integer", name, value["amount"])
		} else if amount <= 0 {
			t.Errorf("create example %q: amount %d must be positive (handler rejects with refund.invalid_amount)", name, amount)
		}
		currency, ok := value["currency"].(string)
		if !ok || len(currency) != 3 {
			t.Errorf("create example %q: currency %v must be a 3-char string (handler rejects empty with refund.missing_currency)", name, value["currency"])
		}
		for _, field := range []string{"reason", "requested_by"} {
			if v, present := value[field]; present && v != nil {
				if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
					t.Errorf("create example %q: %s %v must be null or a non-empty string", name, field, v)
				}
			}
		}
	}

	// ── POST /v1/refunds/{id}/approve ─────────────────────────────
	approveOp := mustOperation(t, paths, "/v1/refunds/{id}/approve", "post")
	approveExamples := extractExamples(t, approveOp)
	if len(approveExamples) == 0 {
		t.Error("approveRefund requestBody must declare at least one example")
	}
	for name, rawValue := range approveExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("approve example %q: value is not a mapping", name)
			continue
		}
		if v, present := value["notes"]; present && v != nil {
			if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("approve example %q: notes %v must be null or a non-empty string", name, v)
			}
		}
	}

	// ── POST /v1/refunds/{id}/reject ──────────────────────────────
	rejectOp := mustOperation(t, paths, "/v1/refunds/{id}/reject", "post")
	rejectExamples := extractExamples(t, rejectOp)
	if len(rejectExamples) == 0 {
		t.Error("rejectRefund requestBody must declare at least one example")
	}
	for name, rawValue := range rejectExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("reject example %q: value is not a mapping", name)
			continue
		}
		if v, present := value["notes"]; present && v != nil {
			if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("reject example %q: notes %v must be null or a non-empty string", name, v)
			}
		}
	}

	// ── POST /v1/refunds/webhook ──────────────────────────────────
	webhookOp := mustOperation(t, paths, "/v1/refunds/webhook", "post")
	webhookExamples := extractExamples(t, webhookOp)
	if len(webhookExamples) == 0 {
		t.Error("refundWebhook requestBody must declare at least one example")
	}
	for name, rawValue := range webhookExamples {
		value, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("webhook example %q: value is not a mapping", name)
			continue
		}
		prid, ok := value["provider_refund_id"].(string)
		if !ok || strings.TrimSpace(prid) == "" {
			t.Errorf("webhook example %q: provider_refund_id %v must be a non-empty string (handler rejects with refund_webhook.missing_provider_refund_id)", name, value["provider_refund_id"])
		}
		et, ok := value["event_type"].(string)
		if !ok || strings.TrimSpace(et) == "" {
			t.Errorf("webhook example %q: event_type %v must be a non-empty string (handler rejects with refund_webhook.missing_event_type)", name, value["event_type"])
		}
		rid, ok := value["refund_id"].(string)
		if !ok || strings.TrimSpace(rid) == "" {
			t.Errorf("webhook example %q: refund_id %v must be a non-empty string (handler rejects with refund_webhook.missing_refund_id)", name, value["refund_id"])
		} else if !looksLikeUUID(rid) {
			t.Errorf("webhook example %q: refund_id %q must be UUID-shaped (handler rejects with refund_webhook.invalid_refund_id)", name, rid)
		}
		if ts, present := value["target_state"]; present && ts != nil {
			s, ok := ts.(string)
			if !ok {
				t.Errorf("webhook example %q: target_state %v must be a string", name, ts)
				continue
			}
			reachable := false
			for _, targets := range validRefundTransitions {
				if targets[s] {
					reachable = true
					break
				}
			}
			if !reachable {
				t.Errorf("webhook example %q: target_state %q is not a valid state machine target", name, s)
			}
		}
		if v, present := value["failure_reason"]; present && v != nil {
			if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("webhook example %q: failure_reason %v must be null or a non-empty string", name, v)
			}
		}
	}
}
