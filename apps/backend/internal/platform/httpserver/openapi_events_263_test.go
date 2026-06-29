// openapi_events_263_test.go pins the OpenAPI documentation contract
// established by feature #263 (Wave A-2): every event endpoint implemented
// in apps/backend/internal/platform/httpserver/events.go must be documented
// in apps/backend/openapi/openapi.yaml together with its component schemas,
// permissions, error envelope, translations support, and example payloads.
//
// Coverage matches feature #263 acceptance steps:
//
//   - Step 1: path entries for every events.go handler
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint; the translations map is documented for
//     localizable create / update bodies
//   - Step 3: minimal contract test validates the spec's request/response
//     examples for the events group against the schema (yaml parse + key
//     presence so the test runs without docker / postgres / oapi-codegen)
//   - Step 4: schemas (EventItem, EventEnvelope, EventListResponse,
//     EventDeleteResponse, CreateEventRequest, UpdateEventRequest,
//     UpdateEventStatusRequest, EventTranslations) live under
//     components.schemas with the documented enums and required fields
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readOpenAPISpec263 returns the raw bytes of openapi.yaml using the
// shared path resolver from openapi_drift_test.go.
func readOpenAPISpec263(t *testing.T) string {
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

// TestOpenAPI263_PathsPresent verifies step 1: every events.go handler is
// documented under `paths:` in openapi.yaml.
func TestOpenAPI263_PathsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	expected := []string{
		"  /v1/events:",
		"  /v1/events/{id}:",
		"  /v1/organizations/{org_id}/events:",
		"  /v1/organizations/{org_id}/events/{id}:",
		"  /v1/organizations/{org_id}/events/{id}/status:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI263_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator.
func TestOpenAPI263_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	expected := []string{
		"operationId: listEvents",
		"operationId: getEvent",
		"operationId: listOrgEvents",
		"operationId: createEvent",
		"operationId: updateEvent",
		"operationId: deleteEvent",
		"operationId: updateEventStatus",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI263_SchemasPresent verifies step 4: every event-group component
// schema is declared under components.schemas.
func TestOpenAPI263_SchemasPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	expected := []string{
		"    EventItem:",
		"    EventEnvelope:",
		"    EventListResponse:",
		"    EventDeleteResponse:",
		"    CreateEventRequest:",
		"    UpdateEventRequest:",
		"    UpdateEventStatusRequest:",
		"    EventTranslations:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI263_PermissionsDocumented verifies step 2: every event
// permission used by mount_catalog.go is mentioned in the spec so operators
// reading the docs can map endpoints to RBAC entries.
func TestOpenAPI263_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	for _, perm := range []string{
		"event.read",
		"event.create",
		"event.update",
		"event.publish",
		"event.delete",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI263_BearerAuthAndTags verifies every event operation declares
// `bearerAuth: []` and the `v1` tag, matching the rest of the v1 surface.
func TestOpenAPI263_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	for _, op := range []string{
		"operationId: listEvents",
		"operationId: getEvent",
		"operationId: listOrgEvents",
		"operationId: createEvent",
		"operationId: updateEvent",
		"operationId: deleteEvent",
		"operationId: updateEventStatus",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 2000
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

// TestOpenAPI263_ErrorEnvelopeUsed verifies step 2: every event endpoint
// wires the standard ErrorEnvelope for all error responses. We assert by
// counting `$ref: "#/components/schemas/ErrorEnvelope"` mentions inside
// each operation block; the exact number per op is not pinned, but it must
// be > 0 so the spec never drifts to ad-hoc error shapes.
func TestOpenAPI263_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	for _, op := range []string{
		"operationId: listEvents",
		"operationId: getEvent",
		"operationId: listOrgEvents",
		"operationId: createEvent",
		"operationId: updateEvent",
		"operationId: deleteEvent",
		"operationId: updateEventStatus",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			continue
		}
		end := idx + 4000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, `$ref: "#/components/schemas/ErrorEnvelope"`) {
			t.Errorf("operation %q does not reference ErrorEnvelope on any error response", op)
		}
	}
}

// TestOpenAPI263_TranslationsDocumented verifies the translations map is
// documented on both create and update bodies (localizable fields).
func TestOpenAPI263_TranslationsDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	// EventTranslations must be referenced from both create and update.
	for _, owner := range []string{"CreateEventRequest:", "UpdateEventRequest:"} {
		window := schemaWindow(spec, owner)
		if window == "" {
			t.Fatalf("schema %q not found", owner)
		}
		if !strings.Contains(window, `$ref: "#/components/schemas/EventTranslations"`) {
			t.Errorf("schema %s should reference EventTranslations for localizable fields", owner)
		}
	}
}

// schemaWindow returns the slice of the spec spanning the named component
// schema definition. owner is the schema name with a trailing colon
// (e.g. "EventItem:"). Returns "" when not found.
func schemaWindow(spec, owner string) string {
	marker := "    " + owner
	idx := strings.Index(spec, marker)
	if idx < 0 {
		return ""
	}
	// Tail starts just after the owner header so the lookahead for the
	// next sibling cannot match the owner itself.
	body := spec[idx+len(marker):]
	// Next sibling = a new line that starts with exactly 4 spaces + non-space.
	// Walk line by line and stop on that.
	lines := strings.SplitAfter(body, "\n")
	end := 0
	for i, line := range lines {
		if i == 0 {
			// owner line tail (everything up to first newline) — always part of window
			end += len(line)
			continue
		}
		// Detect sibling: 4-space indent then non-space.
		if len(line) >= 5 && line[0] == ' ' && line[1] == ' ' && line[2] == ' ' && line[3] == ' ' && line[4] != ' ' {
			break
		}
		// Detect top-level (no indent) — also a stop.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '\n' {
			break
		}
		end += len(line)
	}
	return body[:end]
}

// TestOpenAPI263_StatusTransitionEnumPinned verifies that the
// UpdateEventStatusRequest enum covers the four lifecycle states the
// handler accepts. Catches accidental enum drift away from the events.go
// validation.
func TestOpenAPI263_StatusTransitionEnumPinned(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec263(t)

	window := schemaWindow(spec, "UpdateEventStatusRequest:")
	if window == "" {
		t.Fatal("UpdateEventStatusRequest schema not found")
	}
	for _, want := range []string{"draft", "published", "cancelled", "archived"} {
		if !strings.Contains(window, want) {
			t.Errorf("UpdateEventStatusRequest.status enum missing %q", want)
		}
	}
}

// TestOpenAPI263_SpecExamplesValidate parses openapi.yaml as YAML and
// walks the events-group request/response examples, asserting that:
//
//   - Every example body in CreateEventRequest passes the documented
//     required field set (`name`, `start_at`, `end_at`) and uses RFC 3339
//     timestamps where applicable.
//   - The UpdateEventStatusRequest examples carry a valid enum value.
//
// This is the "minimal contract test" called out by step 3 — it runs in
// CI without docker / postgres / oapi-codegen because it only needs the
// YAML file itself.
func TestOpenAPI263_SpecExamplesValidate(t *testing.T) {
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

	validStatuses := map[string]bool{
		"draft":     true,
		"published": true,
		"cancelled": true,
		"archived":  true,
	}
	validVisibility := map[string]bool{
		"public":   true,
		"private":  true,
		"unlisted": true,
	}

	// ── POST /v1/organizations/{org_id}/events ─────────────────────────
	createOp := mustOperation(t, paths, "/v1/organizations/{org_id}/events", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Fatal("createEvent body must declare at least one example payload")
	}
	for name, ex := range createExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("create example %q: not a mapping", name)
			continue
		}
		for _, req := range []string{"name", "start_at", "end_at"} {
			if _, ok := body[req]; !ok {
				t.Errorf("create example %q: missing required field %q", name, req)
			}
		}
		if vis, ok := body["visibility"].(string); ok && !validVisibility[vis] {
			t.Errorf("create example %q: visibility %q not in enum", name, vis)
		}
		if st, ok := body["status"].(string); ok && !validStatuses[st] {
			t.Errorf("create example %q: status %q not in enum", name, st)
		}
		if startStr, ok := body["start_at"].(string); ok {
			if !looksLikeRFC3339(startStr) {
				t.Errorf("create example %q: start_at %q is not RFC 3339", name, startStr)
			}
		}
		if endStr, ok := body["end_at"].(string); ok {
			if !looksLikeRFC3339(endStr) {
				t.Errorf("create example %q: end_at %q is not RFC 3339", name, endStr)
			}
		}
	}

	// ── POST /v1/organizations/{org_id}/events/{id}/status ────────────
	statusOp := mustOperation(t, paths, "/v1/organizations/{org_id}/events/{id}/status", "post")
	statusExamples := extractExamples(t, statusOp)
	if len(statusExamples) == 0 {
		t.Fatal("updateEventStatus body must declare at least one example payload")
	}
	for name, ex := range statusExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("status example %q: not a mapping", name)
			continue
		}
		st, ok := body["status"].(string)
		if !ok {
			t.Errorf("status example %q: missing required field %q", name, "status")
			continue
		}
		if !validStatuses[st] {
			t.Errorf("status example %q: status %q not in enum", name, st)
		}
	}
}

// mustOperation returns paths[p][method] as a mapping or fails the test.
func mustOperation(t *testing.T, paths map[string]any, p, method string) map[string]any {
	t.Helper()
	pNode, ok := paths[p].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing", p)
	}
	op, ok := pNode[method].(map[string]any)
	if !ok {
		t.Fatalf("path %q has no %s operation", p, method)
	}
	return op
}

// extractExamples returns the map of example_name → example value from an
// operation's requestBody.content["application/json"].examples mapping.
// Returns an empty map when the operation has no body or no examples.
func extractExamples(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	rb, ok := op["requestBody"].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := rb["content"].(map[string]any)
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
	for name, raw := range examples {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out[name] = entry["value"]
	}
	return out
}

// looksLikeRFC3339 does a cheap shape check so we do not pull a heavy
// parser into a lint-only test. Real validation happens at runtime.
func looksLikeRFC3339(s string) bool {
	// Minimum: "YYYY-MM-DDTHH:MM:SSZ" = 20 chars.
	if len(s) < 20 {
		return false
	}
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return false
	}
	return true
}
