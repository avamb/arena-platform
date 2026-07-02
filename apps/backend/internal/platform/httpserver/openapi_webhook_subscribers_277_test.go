// openapi_webhook_subscribers_277_test.go pins the OpenAPI documentation
// contract established by feature #277 (Wave A-16): the webhook
// subscriber endpoint group implemented in
// apps/backend/internal/platform/httpserver/wp_webhooks.go (the feature
// description references the historical "webhook_subscribers.go" group
// name; the actual file is wp_webhooks.go which hosts the WordPress
// webhook subscriber registry) must be documented in
// apps/backend/openapi/openapi.yaml together with its component
// schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #277 acceptance steps:
//
//   - Step 1: path + operationId entries for every wp_webhooks.go
//     subscriber handler (handleListWebhookSubscribers,
//     handleRegisterWebhookSubscriber, handleGetWebhookSubscriber,
//     handleDeactivateWebhookSubscriber) together with five component
//     schemas (WebhookSubscriberSummary,
//     RegisterWebhookSubscriberRequest,
//     RegisterWebhookSubscriberResponse,
//     WebhookSubscriberListResponse,
//     DeactivateWebhookSubscriberResponse).
//   - Step 2: permission `webhook.subscriber.manage` is mentioned on
//     every operation, the standard ErrorEnvelope is wired on every
//     error status code, and each authenticated operation declares
//     `bearerAuth: []` + `tags: [v1]`.
//   - Step 3: minimal contract test validates the spec's example
//     payloads against the invariants enforced by wp_webhooks.go and
//     the underlying webhook_subscribers schema (runs in CI without
//     docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields, the secret-omission invariant on
//     the GET projection, and the 64-char-hex shape on the
//     registration response's signing_secret.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI277_PathsPresent verifies step 1: both
// /v1/webhooks/subscribers and /v1/webhooks/subscribers/{id} are
// documented under `paths:` in openapi.yaml.
func TestOpenAPI277_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/webhooks/subscribers:",
		"  /v1/webhooks/subscribers/{id}:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI277_OperationIDsPresent verifies the canonical
// operationId values used by oapi-codegen / the TS client generator
// for each webhook subscriber handler.
func TestOpenAPI277_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: listWebhookSubscribers",
		"operationId: registerWebhookSubscriber",
		"operationId: getWebhookSubscriber",
		"operationId: deactivateWebhookSubscriber",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI277_SchemasPresent verifies step 4: every webhook
// subscriber component schema is declared under components.schemas.
func TestOpenAPI277_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    WebhookSubscriberSummary:",
		"    RegisterWebhookSubscriberRequest:",
		"    RegisterWebhookSubscriberResponse:",
		"    WebhookSubscriberListResponse:",
		"    DeactivateWebhookSubscriberResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI277_PermissionsDocumented verifies step 2: each handler's
// permission (`webhook.subscriber.manage` from mount_admin.go
// mountWebhookSubscriberRoutes) is mentioned inside its operation
// window.
func TestOpenAPI277_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	ops := []string{
		"operationId: listWebhookSubscribers",
		"operationId: registerWebhookSubscriber",
		"operationId: getWebhookSubscriber",
		"operationId: deactivateWebhookSubscriber",
	}
	const perm = "webhook.subscriber.manage"
	for _, op := range ops {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("%s missing", op)
			continue
		}
		end := idx + 8000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, perm) {
			t.Errorf("%s does not mention %q permission", op, perm)
		}
	}
}

// TestOpenAPI277_BearerAuthAndTags verifies that every webhook
// subscriber operation declares `bearerAuth: []` and `tags: [v1]`.
// All four endpoints are authenticated (the unauthenticated WP
// inbound webhook is documented separately under wp_webhooks.go but
// is NOT part of feature #277's subscriber registry scope).
func TestOpenAPI277_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listWebhookSubscribers",
		"operationId: registerWebhookSubscriber",
		"operationId: getWebhookSubscriber",
		"operationId: deactivateWebhookSubscriber",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Fatalf("operation %q missing from openapi.yaml", op)
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

// TestOpenAPI277_ErrorEnvelopeUsed verifies step 2: each webhook
// subscriber operation wires the standard ErrorEnvelope on its error
// responses.
func TestOpenAPI277_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listWebhookSubscribers",
		"operationId: registerWebhookSubscriber",
		"operationId: getWebhookSubscriber",
		"operationId: deactivateWebhookSubscriber",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %s missing", op)
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

// TestOpenAPI277_WebhookSubscriberErrorCodesDocumented verifies that
// the spec documents the canonical error codes emitted by
// wp_webhooks.go across the four subscriber handlers:
//
//   - bad_request          (invalid JSON, invalid subscriber id)
//   - validation_error     (callback_url is required)
//   - conflict             (callback_url already registered)
//   - not_found            (subscriber not found)
//   - internal_error       (secret gen / register / list / get / deactivate failed)
//   - service_unavailable  (queries or pool not available)
func TestOpenAPI277_WebhookSubscriberErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"bad_request",
		"validation_error",
		"conflict",
		"not_found",
		"internal_error",
		"service_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document webhook subscriber code %q", code)
		}
	}
}

// TestOpenAPI277_SummaryOmitsSigningSecret verifies the central
// security invariant of wp_webhooks.go: the safe summary projection
// used by GET endpoints (WebhookSubscriberSummary) must NOT contain
// a `signing_secret` field. The secret is only emitted by
// RegisterWebhookSubscriberResponse.
func TestOpenAPI277_SummaryOmitsSigningSecret(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	// Slice the WebhookSubscriberSummary schema block: from the
	// schema header to the next top-level schema header (4-space
	// indentation under components.schemas → next "    XYZ:" line).
	const header = "    WebhookSubscriberSummary:"
	idx := strings.Index(spec, header)
	if idx < 0 {
		t.Fatalf("schema %q missing", header)
	}
	rest := spec[idx+len(header):]
	// Find the next sibling schema header. Sibling schemas start with
	// "\n    " followed by an uppercase letter + ":" near the end of
	// the block. We approximate by scanning for the literal
	// "\n    RegisterWebhookSubscriberRequest:" which we know follows
	// in this file's ordering.
	endIdx := strings.Index(rest, "\n    RegisterWebhookSubscriberRequest:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after WebhookSubscriberSummary")
	}
	block := rest[:endIdx]
	// Look only for the literal property declaration line
	// ("        signing_secret:" — 8-space indent under
	// "      properties:"), not for the field name inside prose
	// description blocks (which legitimately reference the secret).
	if strings.Contains(block, "\n        signing_secret:") {
		t.Errorf("WebhookSubscriberSummary must NOT declare a signing_secret property (it is only returned at registration); block:\n%s", block)
	}

	// Sanity check: the same field MUST appear inside the
	// RegisterWebhookSubscriberResponse schema.
	const regHeader = "    RegisterWebhookSubscriberResponse:"
	rIdx := strings.Index(spec, regHeader)
	if rIdx < 0 {
		t.Fatalf("schema %q missing", regHeader)
	}
	rRest := spec[rIdx+len(regHeader):]
	rEnd := strings.Index(rRest, "\n    WebhookSubscriberListResponse:")
	if rEnd < 0 {
		t.Fatalf("could not locate sibling schema after RegisterWebhookSubscriberResponse")
	}
	rBlock := rRest[:rEnd]
	if !strings.Contains(rBlock, "\n        signing_secret:") {
		t.Errorf("RegisterWebhookSubscriberResponse MUST declare a signing_secret property; block:\n%s", rBlock)
	}
}

// TestOpenAPI277_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline example payloads for every webhook subscriber operation,
// asserting per-handler invariants from wp_webhooks.go:
//
//   - listWebhookSubscribers 200 examples: top-level `subscribers`
//     is a list (possibly empty); `total` equals the list length;
//     every entry has the six required summary fields with the
//     expected shapes; no entry leaks a `signing_secret`.
//   - registerWebhookSubscriber request examples: required
//     `callback_url` is non-empty; when present, `event_types` is a
//     list of strings; when present, `site_url` is a non-empty
//     string.
//   - registerWebhookSubscriber 201 examples: all six required
//     response fields present; `signing_secret` is a 64-character
//     lowercase hex string (matches the 32-byte hex.EncodeToString
//     output of crypto/rand.Read in handleRegisterWebhookSubscriber);
//     `active` is `true`; `event_types` is a list.
//   - getWebhookSubscriber 200 examples: same row-shape invariants
//     as a single list entry; no signing_secret leak.
//   - deactivateWebhookSubscriber 200 example: `subscriber_id` is
//     UUID-shaped, `active` is `false`, `message` is non-empty.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI277_SpecExamplesValidate(t *testing.T) {
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

	// 64 lowercase hex chars (32 random bytes encoded by
	// hex.EncodeToString in handleRegisterWebhookSubscriber).
	looksLikeHexSecret := func(s string) bool {
		if len(s) != 64 {
			return false
		}
		for _, c := range s {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
		return true
	}

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

	// checkSummaryRow validates a WebhookSubscriberSummary instance:
	// six required fields with the expected shapes + no
	// signing_secret leak.
	checkSummaryRow := func(label string, row map[string]any) {
		t.Helper()
		// subscriber_id (UUID-shaped string)
		v, present := row["subscriber_id"]
		if !present {
			t.Errorf("%s: required field subscriber_id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: subscriber_id %v is not a UUID-shaped string", label, v)
		}
		// site_url (non-empty string)
		if v, present := row["site_url"]; !present {
			t.Errorf("%s: required field site_url missing", label)
		} else if s, ok := v.(string); !ok || s == "" {
			t.Errorf("%s: site_url %v must be a non-empty string", label, v)
		}
		// callback_url (non-empty string)
		if v, present := row["callback_url"]; !present {
			t.Errorf("%s: required field callback_url missing", label)
		} else if s, ok := v.(string); !ok || s == "" {
			t.Errorf("%s: callback_url %v must be a non-empty string", label, v)
		}
		// event_types (array of strings, possibly empty)
		if v, present := row["event_types"]; !present {
			t.Errorf("%s: required field event_types missing", label)
		} else if et, ok := v.([]any); !ok {
			t.Errorf("%s: event_types %v must be a list", label, v)
		} else {
			for i, item := range et {
				if _, ok := item.(string); !ok {
					t.Errorf("%s: event_types[%d] %v must be a string", label, i, item)
				}
			}
		}
		// active (bool)
		if v, present := row["active"]; !present {
			t.Errorf("%s: required field active missing", label)
		} else if _, ok := v.(bool); !ok {
			t.Errorf("%s: active %v must be a bool", label, v)
		}
		// created_at (RFC 3339)
		if v, present := row["created_at"]; !present {
			t.Errorf("%s: required field created_at missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeRFC3339(s) {
			t.Errorf("%s: created_at %v does not look like RFC 3339", label, v)
		}
		// signing_secret MUST NOT be present on a summary projection.
		if _, present := row["signing_secret"]; present {
			t.Errorf("%s: signing_secret must NOT appear on a subscriber summary projection", label)
		}
	}

	// ── GET /v1/webhooks/subscribers ────────────────────────────────
	listOp := mustOperation(t, paths, "/v1/webhooks/subscribers", "get")
	listExamples := extractResponseExamples(listOp, "200")
	if len(listExamples) == 0 {
		t.Fatal("listWebhookSubscribers must declare at least one 200 response example")
	}
	for name, rawValue := range listExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("list example %q: value is not a mapping", name)
			continue
		}
		subs, ok := body["subscribers"].([]any)
		if !ok {
			t.Errorf("list example %q: required field subscribers must be a list", name)
			continue
		}
		// total must equal len(subscribers)
		if rawTotal, present := body["total"]; !present {
			t.Errorf("list example %q: required field total missing", name)
		} else {
			// YAML decodes integers as int (or int64 on some
			// platforms); normalise.
			var n int
			switch v := rawTotal.(type) {
			case int:
				n = v
			case int64:
				n = int(v)
			case float64:
				n = int(v)
			default:
				t.Errorf("list example %q: total %v has unexpected type %T", name, rawTotal, rawTotal)
				continue
			}
			if n != len(subs) {
				t.Errorf("list example %q: total=%d does not match len(subscribers)=%d", name, n, len(subs))
			}
		}
		for i, item := range subs {
			row, ok := item.(map[string]any)
			if !ok {
				t.Errorf("list example %q[%d]: entry is not a mapping", name, i)
				continue
			}
			checkSummaryRow("list example "+name+"["+itoaSmall277(i)+"]", row)
		}
	}

	// ── POST /v1/webhooks/subscribers ───────────────────────────────
	regOp := mustOperation(t, paths, "/v1/webhooks/subscribers", "post")

	// Request examples
	reqExamples := extractRequestExamples(regOp)
	if len(reqExamples) == 0 {
		t.Fatal("registerWebhookSubscriber must declare at least one requestBody example")
	}
	for name, rawValue := range reqExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("register request example %q: value is not a mapping", name)
			continue
		}
		// callback_url required + non-empty.
		cb, ok := body["callback_url"].(string)
		if !ok || cb == "" {
			t.Errorf("register request example %q: callback_url %v must be a non-empty string", name, body["callback_url"])
		}
		// site_url optional, but if present must be non-empty string.
		if v, present := body["site_url"]; present {
			if s, ok := v.(string); !ok || s == "" {
				t.Errorf("register request example %q: site_url %v must be a non-empty string when present", name, v)
			}
		}
		// event_types optional, but if present must be []string.
		if v, present := body["event_types"]; present {
			et, ok := v.([]any)
			if !ok {
				t.Errorf("register request example %q: event_types %v must be a list when present", name, v)
			} else {
				for i, item := range et {
					if _, ok := item.(string); !ok {
						t.Errorf("register request example %q: event_types[%d] %v must be a string", name, i, item)
					}
				}
			}
		}
	}

	// 201 response examples — full registration row including secret.
	createdExamples := extractResponseExamples(regOp, "201")
	if len(createdExamples) == 0 {
		t.Fatal("registerWebhookSubscriber must declare at least one 201 response example")
	}
	for name, rawValue := range createdExamples {
		row, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("register 201 example %q: value is not a mapping", name)
			continue
		}
		// subscriber_id UUID-shaped
		if v, present := row["subscriber_id"]; !present {
			t.Errorf("register 201 example %q: required field subscriber_id missing", name)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("register 201 example %q: subscriber_id %v not UUID-shaped", name, v)
		}
		// site_url non-empty string
		if v, present := row["site_url"]; !present {
			t.Errorf("register 201 example %q: required field site_url missing", name)
		} else if s, ok := v.(string); !ok || s == "" {
			t.Errorf("register 201 example %q: site_url %v must be non-empty string", name, v)
		}
		// callback_url non-empty string
		if v, present := row["callback_url"]; !present {
			t.Errorf("register 201 example %q: required field callback_url missing", name)
		} else if s, ok := v.(string); !ok || s == "" {
			t.Errorf("register 201 example %q: callback_url %v must be non-empty string", name, v)
		}
		// event_types array of strings
		if v, present := row["event_types"]; !present {
			t.Errorf("register 201 example %q: required field event_types missing", name)
		} else if et, ok := v.([]any); !ok {
			t.Errorf("register 201 example %q: event_types %v must be a list", name, v)
		} else {
			for i, item := range et {
				if _, ok := item.(string); !ok {
					t.Errorf("register 201 example %q: event_types[%d] %v must be a string", name, i, item)
				}
			}
		}
		// active MUST be true on a successful registration.
		if v, present := row["active"]; !present {
			t.Errorf("register 201 example %q: required field active missing", name)
		} else if b, ok := v.(bool); !ok || !b {
			t.Errorf("register 201 example %q: active %v must be the bool true", name, v)
		}
		// signing_secret MUST be a 64-char lowercase hex string.
		if v, present := row["signing_secret"]; !present {
			t.Errorf("register 201 example %q: required field signing_secret missing", name)
		} else if s, ok := v.(string); !ok || !looksLikeHexSecret(s) {
			t.Errorf("register 201 example %q: signing_secret %v must be a 64-char lowercase hex string", name, v)
		}
	}

	// ── GET /v1/webhooks/subscribers/{id} ───────────────────────────
	getOp := mustOperation(t, paths, "/v1/webhooks/subscribers/{id}", "get")
	getExamples := extractResponseExamples(getOp, "200")
	if len(getExamples) == 0 {
		t.Fatal("getWebhookSubscriber must declare at least one 200 response example")
	}
	for name, rawValue := range getExamples {
		row, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("get 200 example %q: value is not a mapping", name)
			continue
		}
		checkSummaryRow("get 200 example "+name, row)
	}

	// ── DELETE /v1/webhooks/subscribers/{id} ────────────────────────
	delOp := mustOperation(t, paths, "/v1/webhooks/subscribers/{id}", "delete")
	delExamples := extractResponseExamples(delOp, "200")
	if len(delExamples) == 0 {
		t.Fatal("deactivateWebhookSubscriber must declare at least one 200 response example")
	}
	for name, rawValue := range delExamples {
		row, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("deactivate 200 example %q: value is not a mapping", name)
			continue
		}
		// subscriber_id UUID-shaped
		if v, present := row["subscriber_id"]; !present {
			t.Errorf("deactivate 200 example %q: required field subscriber_id missing", name)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("deactivate 200 example %q: subscriber_id %v not UUID-shaped", name, v)
		}
		// active MUST be false after deactivation.
		if v, present := row["active"]; !present {
			t.Errorf("deactivate 200 example %q: required field active missing", name)
		} else if b, ok := v.(bool); !ok || b {
			t.Errorf("deactivate 200 example %q: active %v must be the bool false", name, v)
		}
		// message non-empty.
		if v, present := row["message"]; !present {
			t.Errorf("deactivate 200 example %q: required field message missing", name)
		} else if s, ok := v.(string); !ok || s == "" {
			t.Errorf("deactivate 200 example %q: message %v must be non-empty string", name, v)
		}
	}
}

// itoaSmall277 returns the decimal representation of a small
// non-negative int without pulling in strconv (kept local so the
// package test surface is self-contained and does not collide with
// itoaSmall helpers in other test files).
func itoaSmall277(n int) string {
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
