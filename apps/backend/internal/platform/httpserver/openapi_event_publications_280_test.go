// openapi_event_publications_280_test.go pins the OpenAPI documentation
// contract established by feature #280 (Wave E-2): the event-publication
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/publications.go (feature
// #151) must be documented in apps/backend/openapi/openapi.yaml together
// with its component schemas, permissions, error envelope, and example
// payloads.
//
// Coverage matches feature #280 acceptance steps:
//
//   - Step 1: path + operationId entries for every publications.go
//     handler (handleListPublications, handlePublishEvent,
//     handleUnpublishEvent) together with three component schemas
//     (EventPublication, PublishEventRequest,
//     EventPublicationListResponse).
//   - Step 2: permissions `publication.read`, `publication.create`,
//     `publication.delete` from mount_catalog.go.mountPublicationRoutes
//     are mentioned on the matching operations, the standard
//     ErrorEnvelope is wired on every error status code, and each
//     authenticated operation declares `bearerAuth: []` + `tags: [v1]`.
//   - Step 3 / minimal contract test: validates the spec's example
//     payloads against the invariants enforced by publications.go and
//     the underlying event_publications schema (runs in CI without
//     docker / postgres / oapi-codegen).
//   - Step 4: schemas live under components.schemas with the documented
//     required fields, the nullable city_id, and the RFC 3339
//     published_at timestamp.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI280_PathsPresent verifies step 1: both publication path
// templates are documented under `paths:` in openapi.yaml.
func TestOpenAPI280_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/events/{event_id}/publications:",
		"  /v1/events/{event_id}/publications/{feed_token_id}:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI280_OperationIDsPresent verifies the canonical operationId
// values used by oapi-codegen / the TS client generator for each
// publications.go handler.
func TestOpenAPI280_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: listEventPublications",
		"operationId: publishEvent",
		"operationId: unpublishEvent",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI280_SchemasPresent verifies step 4: every event-publication
// component schema is declared under components.schemas.
func TestOpenAPI280_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    EventPublication:",
		"    PublishEventRequest:",
		"    EventPublicationListResponse:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI280_PermissionsDocumented verifies step 2: each handler's
// permission (from mount_catalog.go.mountPublicationRoutes) is mentioned
// inside its operation window.
func TestOpenAPI280_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	cases := []struct {
		op   string
		perm string
	}{
		{"operationId: listEventPublications", "publication.read"},
		{"operationId: publishEvent", "publication.create"},
		{"operationId: unpublishEvent", "publication.delete"},
	}
	for _, c := range cases {
		idx := strings.Index(spec, c.op)
		if idx < 0 {
			t.Errorf("%s missing", c.op)
			continue
		}
		end := idx + 8000
		if end > len(spec) {
			end = len(spec)
		}
		window := spec[idx:end]
		if !strings.Contains(window, c.perm) {
			t.Errorf("%s does not mention %q permission", c.op, c.perm)
		}
	}
}

// TestOpenAPI280_BearerAuthAndTags verifies every publications operation
// declares `bearerAuth: []` and `tags: [v1]`. All three endpoints are
// authenticated — there is no public-feed equivalent in publications.go.
func TestOpenAPI280_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listEventPublications",
		"operationId: publishEvent",
		"operationId: unpublishEvent",
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

// TestOpenAPI280_ErrorEnvelopeUsed verifies step 2: each publications
// operation wires the standard ErrorEnvelope on its error responses.
func TestOpenAPI280_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: listEventPublications",
		"operationId: publishEvent",
		"operationId: unpublishEvent",
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

// TestOpenAPI280_PublicationErrorCodesDocumented verifies that the spec
// documents the canonical error codes emitted by publications.go across
// its three handlers.
func TestOpenAPI280_PublicationErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	// Codes are derived directly from errorEnvelope("publication.…")
	// call sites in publications.go.
	for _, code := range []string{
		"publication.invalid_event_id",
		"publication.invalid_feed_token_id",
		"publication.invalid_city_id",
		"publication.invalid_json",
		"publication.body_required",
		"publication.feed_token_id_required",
		"publication.content_type_required",
		"publication.internal",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document publication error code %q", code)
		}
	}
}

// TestOpenAPI280_EventPublicationCityIDIsNullable verifies the spec
// matches publications.go's `*string` city_id field shape: nullable on
// both the response schema (EventPublication) and the request schema
// (PublishEventRequest).
func TestOpenAPI280_EventPublicationCityIDIsNullable(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	// Slice EventPublication schema block from its header to the next
	// sibling schema header (PublishEventRequest).
	const epHeader = "    EventPublication:"
	idx := strings.Index(spec, epHeader)
	if idx < 0 {
		t.Fatalf("schema %q missing", epHeader)
	}
	rest := spec[idx+len(epHeader):]
	endIdx := strings.Index(rest, "\n    PublishEventRequest:")
	if endIdx < 0 {
		t.Fatalf("could not locate sibling schema after EventPublication")
	}
	block := rest[:endIdx]
	if !strings.Contains(block, "\n        city_id:") {
		t.Errorf("EventPublication must declare a city_id property; block:\n%s", block)
	}
	if !schemaBlockDeclaresNull(block) {
		t.Errorf("EventPublication.city_id must be nullable (matches *string in publications.go); block:\n%s", block)
	}

	// Slice PublishEventRequest schema block.
	const reqHeader = "    PublishEventRequest:"
	rIdx := strings.Index(spec, reqHeader)
	if rIdx < 0 {
		t.Fatalf("schema %q missing", reqHeader)
	}
	rRest := spec[rIdx+len(reqHeader):]
	rEnd := strings.Index(rRest, "\n    EventPublicationListResponse:")
	if rEnd < 0 {
		t.Fatalf("could not locate sibling schema after PublishEventRequest")
	}
	rBlock := rRest[:rEnd]
	if !strings.Contains(rBlock, "\n        city_id:") {
		t.Errorf("PublishEventRequest must declare a city_id property; block:\n%s", rBlock)
	}
	if !schemaBlockDeclaresNull(rBlock) {
		t.Errorf("PublishEventRequest.city_id must be nullable; block:\n%s", rBlock)
	}
}

// schemaBlockDeclaresNull reports whether a schema block marks a property as
// nullable. OpenAPI 3.1 expresses this as a type array containing "null"
// (the OAS 3.0 `nullable: true` keyword is forbidden by
// TestOpenAPI_NoNullableKeyword but still accepted here for robustness).
func schemaBlockDeclaresNull(block string) bool {
	return strings.Contains(block, `"null"`) ||
		strings.Contains(block, `'null'`) ||
		strings.Contains(block, "nullable: true")
}

// TestOpenAPI280_SpecExamplesValidate is the minimal contract test. It
// parses openapi.yaml as YAML and walks the inline example payloads for
// each publications operation, asserting per-handler invariants:
//
//   - listEventPublications 200 examples: top-level `publications` is a
//     list (possibly empty); every entry has the four required row
//     fields with the expected shapes; city_id is either null or a
//     UUID-shaped string.
//   - publishEvent request examples: required `feed_token_id` is
//     UUID-shaped; when present, `city_id` is null or a UUID-shaped
//     string.
//   - publishEvent 200 response examples: full row shape (id, event_id,
//     feed_token_id, published_at required; city_id null or UUID).
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI280_SpecExamplesValidate(t *testing.T) {
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

	// checkPublicationRow validates an EventPublication instance: four
	// required fields with the expected shapes + nullable-or-UUID
	// city_id.
	checkPublicationRow := func(label string, row map[string]any) {
		t.Helper()
		if v, present := row["id"]; !present {
			t.Errorf("%s: required field id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: id %v not UUID-shaped", label, v)
		}
		if v, present := row["event_id"]; !present {
			t.Errorf("%s: required field event_id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: event_id %v not UUID-shaped", label, v)
		}
		if v, present := row["feed_token_id"]; !present {
			t.Errorf("%s: required field feed_token_id missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeUUID(s) {
			t.Errorf("%s: feed_token_id %v not UUID-shaped", label, v)
		}
		// city_id MUST be either null or a UUID-shaped string.
		if v, present := row["city_id"]; !present {
			t.Errorf("%s: required field city_id missing (expected null or UUID)", label)
		} else if v != nil {
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("%s: city_id %v must be null or a UUID-shaped string", label, v)
			}
		}
		if v, present := row["published_at"]; !present {
			t.Errorf("%s: required field published_at missing", label)
		} else if s, ok := v.(string); !ok || !looksLikeRFC3339(s) {
			t.Errorf("%s: published_at %v does not look like RFC 3339", label, v)
		}
	}

	// ── GET /v1/events/{event_id}/publications ──────────────────────
	listOp := mustOperation(t, paths, "/v1/events/{event_id}/publications", "get")
	listExamples := extractResponseExamples(listOp, "200")
	if len(listExamples) == 0 {
		t.Fatal("listEventPublications must declare at least one 200 response example")
	}
	for name, rawValue := range listExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("list example %q: value is not a mapping", name)
			continue
		}
		pubs, ok := body["publications"].([]any)
		if !ok {
			t.Errorf("list example %q: required field publications must be a list", name)
			continue
		}
		for i, item := range pubs {
			row, ok := item.(map[string]any)
			if !ok {
				t.Errorf("list example %q[%d]: entry is not a mapping", name, i)
				continue
			}
			checkPublicationRow("list example "+name+"["+itoaSmall280(i)+"]", row)
		}
	}

	// ── POST /v1/events/{event_id}/publications ─────────────────────
	postOp := mustOperation(t, paths, "/v1/events/{event_id}/publications", "post")

	// Request examples.
	reqExamples := extractRequestExamples(postOp)
	if len(reqExamples) == 0 {
		t.Fatal("publishEvent must declare at least one requestBody example")
	}
	for name, rawValue := range reqExamples {
		body, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("publish request example %q: value is not a mapping", name)
			continue
		}
		// feed_token_id required + UUID-shaped (publications.go
		// rejects empty or non-UUID values with 400).
		ft, ok := body["feed_token_id"].(string)
		if !ok || !looksLikeUUID(ft) {
			t.Errorf("publish request example %q: feed_token_id %v must be a UUID-shaped string", name, body["feed_token_id"])
		}
		// city_id optional, but when present must be null or
		// UUID-shaped.
		if v, present := body["city_id"]; present && v != nil {
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("publish request example %q: city_id %v must be null or UUID-shaped when present", name, v)
			}
		}
	}

	// 200 response examples — full publication row.
	createdExamples := extractResponseExamples(postOp, "200")
	if len(createdExamples) == 0 {
		t.Fatal("publishEvent must declare at least one 200 response example")
	}
	for name, rawValue := range createdExamples {
		row, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("publish 200 example %q: value is not a mapping", name)
			continue
		}
		checkPublicationRow("publish 200 example "+name, row)
	}

	// ── DELETE has no response body (204) — nothing to walk. ────────
}

// itoaSmall280 returns the decimal representation of a small
// non-negative int without pulling in strconv (local to this file to
// avoid colliding with helper functions in sibling test files).
func itoaSmall280(n int) string {
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
