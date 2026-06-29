// openapi_sessions_264_test.go pins the OpenAPI documentation contract
// established by feature #264 (Wave A-3): every session endpoint implemented
// in apps/backend/internal/platform/httpserver/sessions.go must be documented
// in apps/backend/openapi/openapi.yaml together with its component schemas,
// permissions, error envelope, and example payloads.
//
// Coverage matches feature #264 acceptance steps:
//
//   - Step 1: path + operation entries for every sessions.go handler
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint
//   - Step 3: minimal contract test validates the spec's request/response
//     examples for the sessions group against the schema (yaml parse + key
//     presence so the test runs without docker / postgres / oapi-codegen)
//   - Step 4: schemas (SessionItem, SessionEnvelope, SessionListResponse,
//     SessionDeleteResponse, CreateSessionRequest, UpdateSessionRequest)
//     live under components.schemas with the documented enums and required
//     fields
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readOpenAPISpec264 returns the raw bytes of openapi.yaml using the shared
// path resolver from openapi_drift_test.go.
func readOpenAPISpec264(t *testing.T) string {
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

// TestOpenAPI264_PathsPresent verifies step 1: both sessions paths from
// sessions.go are documented under `paths:` in openapi.yaml.
func TestOpenAPI264_PathsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	expected := []string{
		"  /v1/organizations/{org_id}/events/{event_id}/sessions:",
		"  /v1/organizations/{org_id}/events/{event_id}/sessions/{id}:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI264_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator for the sessions group.
func TestOpenAPI264_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	expected := []string{
		"operationId: createSession",
		"operationId: listSessions",
		"operationId: getSession",
		"operationId: updateSession",
		"operationId: deleteSession",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI264_SchemasPresent verifies step 4: every session-group
// component schema is declared under components.schemas.
func TestOpenAPI264_SchemasPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	expected := []string{
		"    SessionItem:",
		"    SessionEnvelope:",
		"    SessionListResponse:",
		"    SessionDeleteResponse:",
		"    CreateSessionRequest:",
		"    UpdateSessionRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI264_PermissionsDocumented verifies step 2: every session
// permission used by mount_catalog.go is mentioned in the spec so operators
// reading the docs can map endpoints to RBAC entries.
func TestOpenAPI264_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	for _, perm := range []string{
		"session.read",
		"session.create",
		"session.update",
		"session.delete",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI264_BearerAuthAndTags verifies every session operation declares
// `bearerAuth: []` and the `v1` tag, matching the rest of the v1 surface.
func TestOpenAPI264_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	for _, op := range []string{
		"operationId: createSession",
		"operationId: listSessions",
		"operationId: getSession",
		"operationId: updateSession",
		"operationId: deleteSession",
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

// TestOpenAPI264_ErrorEnvelopeUsed verifies step 2: every session endpoint
// wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI264_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	for _, op := range []string{
		"operationId: createSession",
		"operationId: listSessions",
		"operationId: getSession",
		"operationId: updateSession",
		"operationId: deleteSession",
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

// TestOpenAPI264_StatusEnumPinned verifies that the SessionItem and
// UpdateSessionRequest status enums match the four lifecycle states the
// handler accepts (validSessionStatuses in sessions.go).
func TestOpenAPI264_StatusEnumPinned(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec264(t)

	for _, owner := range []string{"SessionItem:", "UpdateSessionRequest:", "CreateSessionRequest:"} {
		window := schemaWindow(spec, owner)
		if window == "" {
			t.Fatalf("%s schema not found", owner)
		}
		for _, want := range []string{"draft", "scheduled", "cancelled", "completed"} {
			if !strings.Contains(window, want) {
				t.Errorf("%s status enum missing %q", owner, want)
			}
		}
	}
}

// TestOpenAPI264_SpecExamplesValidate parses openapi.yaml as YAML and
// walks the sessions-group request examples, asserting that:
//
//   - The createSession example body has the documented required fields
//     (`start_at`, `end_at`, `capacity_total`), uses RFC 3339 timestamps,
//     and a positive capacity_total.
//   - The updateSession examples (when they carry status) use values from
//     the documented enum, and any timestamps remain RFC 3339-shaped.
//
// This is the "minimal contract test" called out by step 3 — it runs in
// CI without docker / postgres / oapi-codegen because it only needs the
// YAML file itself.
func TestOpenAPI264_SpecExamplesValidate(t *testing.T) {
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
		"scheduled": true,
		"cancelled": true,
		"completed": true,
	}

	// ── POST /v1/organizations/{org_id}/events/{event_id}/sessions ────
	createOp := mustOperation(t, paths, "/v1/organizations/{org_id}/events/{event_id}/sessions", "post")
	createExamples := extractExamples(t, createOp)
	if len(createExamples) == 0 {
		t.Fatal("createSession body must declare at least one example payload")
	}
	for name, ex := range createExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("create example %q: not a mapping", name)
			continue
		}
		for _, req := range []string{"start_at", "end_at", "capacity_total"} {
			if _, ok := body[req]; !ok {
				t.Errorf("create example %q: missing required field %q", name, req)
			}
		}
		if st, ok := body["status"].(string); ok && !validStatuses[st] {
			t.Errorf("create example %q: status %q not in enum", name, st)
		}
		if startStr, ok := body["start_at"].(string); ok && !looksLikeRFC3339(startStr) {
			t.Errorf("create example %q: start_at %q is not RFC 3339", name, startStr)
		}
		if endStr, ok := body["end_at"].(string); ok && !looksLikeRFC3339(endStr) {
			t.Errorf("create example %q: end_at %q is not RFC 3339", name, endStr)
		}
		// capacity_total must coerce to a positive integer.
		switch v := body["capacity_total"].(type) {
		case int:
			if v <= 0 {
				t.Errorf("create example %q: capacity_total %d must be > 0", name, v)
			}
		case int64:
			if v <= 0 {
				t.Errorf("create example %q: capacity_total %d must be > 0", name, v)
			}
		case float64:
			if v <= 0 {
				t.Errorf("create example %q: capacity_total %v must be > 0", name, v)
			}
		}
	}

	// ── PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id} ──
	updateOp := mustOperation(t, paths, "/v1/organizations/{org_id}/events/{event_id}/sessions/{id}", "patch")
	updateExamples := extractExamples(t, updateOp)
	if len(updateExamples) == 0 {
		t.Fatal("updateSession body must declare at least one example payload")
	}
	for name, ex := range updateExamples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("update example %q: not a mapping", name)
			continue
		}
		if st, ok := body["status"].(string); ok && !validStatuses[st] {
			t.Errorf("update example %q: status %q not in enum", name, st)
		}
		if startStr, ok := body["start_at"].(string); ok && !looksLikeRFC3339(startStr) {
			t.Errorf("update example %q: start_at %q is not RFC 3339", name, startStr)
		}
		if endStr, ok := body["end_at"].(string); ok && !looksLikeRFC3339(endStr) {
			t.Errorf("update example %q: end_at %q is not RFC 3339", name, endStr)
		}
	}
}
