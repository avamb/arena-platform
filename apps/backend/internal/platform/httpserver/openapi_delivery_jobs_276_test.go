// openapi_delivery_jobs_276_test.go pins the OpenAPI documentation
// contract established by feature #276 (Wave A-15): the delivery_jobs
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/admin_ticket_delivery.go
// (the feature description references the historical "delivery_jobs.go"
// group name; the actual file is admin_ticket_delivery.go which hosts
// the support-console delivery inspect + resend handlers) must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #276 acceptance steps:
//
//   - Step 1: path + operationId entries for every admin_ticket_delivery
//     handler (handleAdminGetTicketDelivery,
//     handleAdminResendTicketDelivery) together with three component
//     schemas (DeliveryJob, AdminTicketDeliveryResponse,
//     AdminTicketDeliveryResendResponse).
//   - Step 2: permission `ticket.update` is mentioned on every
//     operation, the standard ErrorEnvelope is wired on every error
//     status code, each authenticated operation declares
//     `bearerAuth: []`, both carry the `X-Admin-Reason` header
//     parameter, and `tags: [v1, admin]` is present.
//   - Step 3: minimal contract test validates the spec's example
//     payloads against the invariants enforced by
//     admin_ticket_delivery.go and the underlying delivery_jobs
//     schema (runs in CI without docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and nullable shapes
//     (`recipient_email`, `last_error`, `sent_at`).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI276_PathsPresent verifies step 1: both
// /v1/admin/tickets/{id}/delivery and /v1/admin/tickets/{id}/delivery/resend
// are documented under `paths:` in openapi.yaml.
func TestOpenAPI276_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/admin/tickets/{id}/delivery:",
		"  /v1/admin/tickets/{id}/delivery/resend:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI276_OperationIDsPresent verifies the canonical
// operationId values used by oapi-codegen / the TS client generator
// for each delivery_jobs handler.
func TestOpenAPI276_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: getAdminTicketDelivery",
		"operationId: resendAdminTicketDelivery",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI276_SchemasPresent verifies step 4: every delivery_jobs
// component schema is declared under components.schemas.
func TestOpenAPI276_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    DeliveryJob:",
		"    AdminTicketDeliveryResponse:",
		"    AdminTicketDeliveryResendResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI276_PermissionsDocumented verifies step 2: each handler's
// permission (`ticket.update` from mount_admin.go
// mountAdminTicketDeliveryRoutes) is mentioned inside its operation
// window.
func TestOpenAPI276_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	ops := []string{
		"operationId: getAdminTicketDelivery",
		"operationId: resendAdminTicketDelivery",
	}
	const perm = "ticket.update"
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

// TestOpenAPI276_BearerAuthAndAdminReason verifies that every
// delivery_jobs operation declares `bearerAuth: []`, the
// `X-Admin-Reason` header parameter, and the `[v1, admin]` tag set.
func TestOpenAPI276_BearerAuthAndAdminReason(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: getAdminTicketDelivery",
		"operationId: resendAdminTicketDelivery",
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
		if !strings.Contains(window, "X-Admin-Reason") {
			t.Errorf("operation %q missing `X-Admin-Reason` header parameter", op)
		}
		if !strings.Contains(window, "tags: [v1, admin]") {
			t.Errorf("operation %q missing `tags: [v1, admin]`", op)
		}
	}
}

// TestOpenAPI276_ErrorEnvelopeUsed verifies step 2: each delivery_jobs
// operation wires the standard ErrorEnvelope on its error responses.
func TestOpenAPI276_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: getAdminTicketDelivery",
		"operationId: resendAdminTicketDelivery",
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

// TestOpenAPI276_DeliveryErrorCodesDocumented verifies that the spec
// documents the canonical error codes emitted by
// admin_ticket_delivery.go across the two handlers:
//
//   - ticket_delivery.not_found
//   - ticket_delivery.ticket_not_found
//   - ticket_delivery.internal
//   - ticket_delivery.enqueue_failed
//   - dependency.database_unavailable
//   - bad_request
func TestOpenAPI276_DeliveryErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"ticket_delivery.not_found",
		"ticket_delivery.ticket_not_found",
		"ticket_delivery.internal",
		"ticket_delivery.enqueue_failed",
		"dependency.database_unavailable",
		"bad_request",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document delivery error code %q", code)
		}
	}
}

// TestOpenAPI276_DeliveryJobSchemaShape verifies the DeliveryJob
// component schema lists every required field including the
// nullable-projection trio (`recipient_email`, `last_error`,
// `sent_at`). The handler's deliveryJobToMap helper sets these
// to explicit JSON nulls when the underlying *string / *time.Time
// is nil; the spec documents the null-on-omission contract in
// prose because oapi-codegen v2.4.1 does not yet accept the OAS
// 3.1 `type: [string, "null"]` array idiom.
func TestOpenAPI276_DeliveryJobSchemaShape(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	const header = "    DeliveryJob:"
	idx := strings.Index(spec, header)
	if idx < 0 {
		t.Fatalf("schema %q missing", header)
	}
	rest := spec[idx+len(header):]
	endIdx := strings.Index(rest, "\n    AdminTicketDeliveryResponse:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after DeliveryJob")
	}
	block := rest[:endIdx]

	// Required field list (declared on the schema's required: array).
	for _, field := range []string{
		"id",
		"ticket_id",
		"status",
		"attempts",
		"recipient_email",
		"last_error",
		"sent_at",
		"queued_at",
		"created_at",
		"updated_at",
	} {
		if !strings.Contains(block, "- "+field) {
			t.Errorf("DeliveryJob.required missing field %q", field)
		}
	}

	// Nullable projections are documented as JSON `null` in the
	// prose for each of recipient_email, last_error, and sent_at —
	// confirm that each prose mention appears in the schema block.
	// (OAS 3.1 `type: [string, "null"]` is not yet supported by
	// oapi-codegen v2.4.1, so the spec encodes nullability via the
	// description string rather than the type system.)
	for _, field := range []string{"recipient_email", "last_error", "sent_at"} {
		header := "        " + field + ":"
		if !strings.Contains(block, header) {
			t.Errorf("DeliveryJob schema missing nullable field %q", field)
		}
	}
}

// TestOpenAPI276_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline example payloads for every delivery_jobs operation, asserting
// per-handler invariants from admin_ticket_delivery.go:
//
//   - getAdminTicketDelivery 200 examples: envelope shape
//     {delivery: {...}}; every delivery row has the ten required
//     fields with the expected shapes; `status` is one of the four
//     documented values; `attempts` is a non-negative int; nullable
//     fields are present (as null OR a valid value).
//   - resendAdminTicketDelivery 202 examples: envelope shape
//     {delivery: {...}, worker_job_id: "<uuid>"}; the delivery row
//     mirrors a freshly inserted pending row (`status == "pending"`,
//     `attempts == 0`, `sent_at == null`, `last_error == null`);
//     `worker_job_id` is UUID-shaped.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI276_SpecExamplesValidate(t *testing.T) {
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

	allowedStatuses := map[string]bool{
		"pending":     true,
		"in_progress": true,
		"sent":        true,
		"failed":      true,
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

	checkDeliveryRow := func(label string, row map[string]any, requirePending bool) {
		t.Helper()
		// id (UUID-shaped string)
		if v, present := row["id"]; !present {
			t.Errorf("%s: required field id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: id %v is not a UUID-shaped string", label, v)
		}
		// ticket_id (UUID-shaped string)
		if v, present := row["ticket_id"]; !present {
			t.Errorf("%s: required field ticket_id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: ticket_id %v is not a UUID-shaped string", label, v)
		}
		// status (one of the four documented values)
		if v, present := row["status"]; !present {
			t.Errorf("%s: required field status missing", label)
		} else if s, ok := v.(string); !ok || !allowedStatuses[s] {
			t.Errorf("%s: status %v must be one of pending|in_progress|sent|failed", label, v)
		} else if requirePending && s != "pending" {
			t.Errorf("%s: status %q must be \"pending\" on a freshly resent row", label, s)
		}
		// attempts (non-negative int)
		attemptsValue := -1
		if v, present := row["attempts"]; !present {
			t.Errorf("%s: required field attempts missing", label)
		} else {
			switch n := v.(type) {
			case int:
				attemptsValue = n
			case int64:
				attemptsValue = int(n)
			case float64:
				attemptsValue = int(n)
			default:
				t.Errorf("%s: attempts %v has unexpected type %T", label, v, v)
			}
			if attemptsValue < 0 {
				t.Errorf("%s: attempts %d must be >= 0", label, attemptsValue)
			}
		}
		if requirePending && attemptsValue != 0 {
			t.Errorf("%s: attempts %d must be 0 on a freshly resent row", label, attemptsValue)
		}

		// Nullable fields must be DECLARED (present as null OR a
		// concrete value). yaml.Unmarshal decodes `null` to a literal
		// nil interface value.
		for _, field := range []string{"recipient_email", "last_error", "sent_at"} {
			if _, present := row[field]; !present {
				t.Errorf("%s: required nullable field %q missing (must appear as null or a value)", label, field)
			}
		}

		if requirePending {
			if row["sent_at"] != nil {
				t.Errorf("%s: sent_at %v must be null on a freshly resent row", label, row["sent_at"])
			}
			if row["last_error"] != nil {
				t.Errorf("%s: last_error %v must be null on a freshly resent row", label, row["last_error"])
			}
		}

		// queued_at / created_at / updated_at (RFC 3339 strings)
		for _, field := range []string{"queued_at", "created_at", "updated_at"} {
			if v, present := row[field]; !present {
				t.Errorf("%s: required field %q missing", label, field)
			} else if s, ok := v.(string); !ok || !looksLikeRFC3339(s) {
				t.Errorf("%s: %s %v does not look like RFC 3339", label, field, v)
			}
		}
	}

	// ── GET /v1/admin/tickets/{id}/delivery ─────────────────────────
	getOp := mustOperation(t, paths, "/v1/admin/tickets/{id}/delivery", "get")
	getExamples := extractResponseExamples(getOp, "200")
	if len(getExamples) == 0 {
		t.Fatal("getAdminTicketDelivery must declare at least one 200 response example")
	}
	for name, rawValue := range getExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("get example %q: value is not a mapping", name)
			continue
		}
		delivery, ok := body["delivery"].(map[string]any)
		if !ok {
			t.Errorf("get example %q: required field delivery must be a mapping", name)
			continue
		}
		checkDeliveryRow("get example "+name, delivery, false)
	}

	// ── POST /v1/admin/tickets/{id}/delivery/resend ─────────────────
	postOp := mustOperation(t, paths, "/v1/admin/tickets/{id}/delivery/resend", "post")
	resendExamples := extractResponseExamples(postOp, "202")
	if len(resendExamples) == 0 {
		t.Fatal("resendAdminTicketDelivery must declare at least one 202 response example")
	}
	for name, rawValue := range resendExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("resend example %q: value is not a mapping", name)
			continue
		}
		delivery, ok := body["delivery"].(map[string]any)
		if !ok {
			t.Errorf("resend example %q: required field delivery must be a mapping", name)
			continue
		}
		checkDeliveryRow("resend example "+name, delivery, true)
		// worker_job_id MUST be a UUID-shaped string.
		if v, present := body["worker_job_id"]; !present {
			t.Errorf("resend example %q: required field worker_job_id missing", name)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("resend example %q: worker_job_id %v is not a UUID-shaped string", name, v)
		}
	}
}
