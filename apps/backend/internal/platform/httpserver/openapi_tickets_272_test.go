// openapi_tickets_272_test.go pins the OpenAPI documentation contract
// established by feature #272 (Wave A-11): the tickets endpoint group
// implemented in apps/backend/internal/platform/httpserver/tickets.go
// must be documented in apps/backend/openapi/openapi.yaml together
// with its component schemas, permission, error envelope, and example
// payloads.
//
// Coverage matches feature #272 acceptance steps:
//
//   - Step 1: path + operationId entries for every tickets.go handler
//     (handleListTickets) together with the TicketItem and
//     TicketListResponse component schemas.
//   - Step 2: permission `ticket.read` is mentioned and the standard
//     ErrorEnvelope is wired on every status code other than 2xx.
//   - Step 3: minimal contract test validates the spec's response
//     example payloads against the schema (yaml parse + key presence +
//     invariants from tickets.go — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and the canonical three-value
//     status enum from migration 0026_tickets.sql
//     (`tickets_status_check`).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI272_PathsPresent verifies step 1: every tickets.go handler
// is documented under `paths:` in openapi.yaml.
func TestOpenAPI272_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/checkout/{id}/tickets:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI272_OperationIDsPresent verifies the canonical operationId
// used by oapi-codegen / the TS client generator for each tickets
// handler.
func TestOpenAPI272_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: listCheckoutTickets",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI272_SchemasPresent verifies step 4: every ticket component
// schema is declared under components.schemas.
func TestOpenAPI272_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    TicketItem:",
		"    TicketListResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI272_PermissionsDocumented verifies step 2: the permission
// declared by mount_commerce.go for the tickets group is mentioned in
// the spec.
func TestOpenAPI272_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	// listCheckoutTickets must mention ticket.read inside its op window.
	idx := strings.Index(spec, "operationId: listCheckoutTickets")
	if idx < 0 {
		t.Fatal("operationId: listCheckoutTickets missing")
	}
	end := idx + 6000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]
	if !strings.Contains(window, "ticket.read") {
		t.Error("listCheckoutTickets does not mention `ticket.read` permission")
	}
}

// TestOpenAPI272_BearerAuthAndTags verifies that the listCheckoutTickets
// operation declares `bearerAuth: []` and `tags: [v1]`.
func TestOpenAPI272_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: listCheckoutTickets"
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

// TestOpenAPI272_ErrorEnvelopeUsed verifies step 2: the listCheckoutTickets
// endpoint wires the standard ErrorEnvelope on its error responses.
func TestOpenAPI272_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: listCheckoutTickets"
	idx := strings.Index(spec, op)
	if idx < 0 {
		t.Fatal("operation listCheckoutTickets missing")
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

// TestOpenAPI272_TicketErrorCodesDocumented verifies that the spec
// documents the canonical ticket error codes emitted by tickets.go.
func TestOpenAPI272_TicketErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"ticket.invalid_checkout_id",
		"ticket.list_failed",
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document ticket code %q", code)
		}
	}
}

// TestOpenAPI272_StatusEnumPinned verifies that the TicketItem.status
// enum lists every status from the tickets_status_check constraint in
// migration 0026_tickets.sql: ('active', 'cancelled', 'transferred').
func TestOpenAPI272_StatusEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	idx := strings.Index(spec, "    TicketItem:")
	if idx < 0 {
		t.Fatal("TicketItem schema missing")
	}
	end := idx + 6000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, status := range []string{
		"- active",
		"- cancelled",
		"- transferred",
	} {
		if !strings.Contains(window, status) {
			t.Errorf("TicketItem.status enum missing literal %q", status)
		}
	}
}

// TestOpenAPI272_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline response examples for the GET /v1/checkout/{id}/tickets
// handler, asserting per-handler invariants enforced in tickets.go and
// the underlying schema:
//
//   - response wrapper is `{ tickets: [...] }` (TicketListResponse);
//   - each TicketItem carries UUID-shaped id, checkout_session_id, and
//     session_id (handler emits .String() on uuid.UUID);
//   - tier_id when non-null is UUID-shaped (mirrors the *uuid.UUID
//     field in TicketRow);
//   - status is one of the three values pinned in the
//     `tickets_status_check` constraint;
//   - issued_at, created_at, and updated_at look RFC 3339 (handler
//     emits .UTC().Format(time.RFC3339));
//   - holder_email when non-null is a non-empty string (matches the
//     `*string` field in TicketRow and the documented nullable=true
//     property).
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI272_SpecExamplesValidate(t *testing.T) {
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

	// extractResponseExamples walks op.responses["200"].content
	// ["application/json"].examples and returns the {name → value} map.
	extractResponseExamples := func(op map[string]any) map[string]any {
		responses, ok := op["responses"].(map[string]any)
		if !ok {
			return nil
		}
		ok200, ok := responses["200"].(map[string]any)
		if !ok {
			return nil
		}
		content, ok := ok200["content"].(map[string]any)
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

	validStatus := map[string]bool{
		"active":      true,
		"cancelled":   true,
		"transferred": true,
	}

	// ── GET /v1/checkout/{id}/tickets ────────────────────────────────
	listOp := mustOperation(t, paths, "/v1/checkout/{id}/tickets", "get")
	examples := extractResponseExamples(listOp)
	if len(examples) == 0 {
		t.Fatal("listCheckoutTickets must declare at least one response example")
	}
	for name, rawValue := range examples {
		envelope, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("list example %q: value is not a mapping", name)
			continue
		}
		ticketsRaw, present := envelope["tickets"]
		if !present {
			t.Errorf("list example %q: envelope missing `tickets` key (TicketListResponse requires it)", name)
			continue
		}
		// `tickets` may be a nil slice (empty array in YAML).
		tickets, ok := ticketsRaw.([]any)
		if !ok && ticketsRaw != nil {
			t.Errorf("list example %q: tickets %v is not an array", name, ticketsRaw)
			continue
		}
		for i, rawTicket := range tickets {
			ticket, ok := rawTicket.(map[string]any)
			if !ok {
				t.Errorf("list example %q: ticket #%d is not a mapping", name, i)
				continue
			}
			// Required string-UUID fields.
			for _, field := range []string{"id", "checkout_session_id", "session_id"} {
				v, present := ticket[field]
				if !present {
					t.Errorf("list example %q ticket #%d: required field %q missing", name, i, field)
					continue
				}
				s, ok := v.(string)
				if !ok || !looksLikeUUID(s) {
					t.Errorf("list example %q ticket #%d: %s %v is not a UUID-shaped string", name, i, field, v)
				}
			}
			// Nullable UUID tier_id.
			if v, present := ticket["tier_id"]; present && v != nil {
				s, ok := v.(string)
				if !ok || !looksLikeUUID(s) {
					t.Errorf("list example %q ticket #%d: tier_id %v must be null or UUID-shaped", name, i, v)
				}
			}
			// Nullable holder_email; when non-null, must be a non-empty string.
			if v, present := ticket["holder_email"]; present && v != nil {
				s, ok := v.(string)
				if !ok || strings.TrimSpace(s) == "" {
					t.Errorf("list example %q ticket #%d: holder_email %v must be null or non-empty string", name, i, v)
				}
			}
			// Status must be in the migration's check constraint.
			status, ok := ticket["status"].(string)
			if !ok {
				t.Errorf("list example %q ticket #%d: status %v must be a string", name, i, ticket["status"])
			} else if !validStatus[status] {
				t.Errorf("list example %q ticket #%d: status %q not in tickets_status_check (active|cancelled|transferred)", name, i, status)
			}
			// Timestamp fields look RFC 3339.
			for _, field := range []string{"issued_at", "created_at", "updated_at"} {
				v, present := ticket[field]
				if !present {
					t.Errorf("list example %q ticket #%d: required timestamp %q missing", name, i, field)
					continue
				}
				s, ok := v.(string)
				if !ok || !looksLikeRFC3339(s) {
					t.Errorf("list example %q ticket #%d: %s %v does not look like RFC 3339", name, i, field, v)
				}
			}
		}
	}
}
