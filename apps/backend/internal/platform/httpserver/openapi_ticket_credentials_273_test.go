// openapi_ticket_credentials_273_test.go pins the OpenAPI documentation
// contract established by feature #273 (Wave A-12): the ticket_credentials
// endpoint group implemented in
// apps/backend/internal/platform/httpserver/credentials.go must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schema, permission, error envelope, and example payloads.
//
// Coverage matches feature #273 acceptance steps:
//
//   - Step 1: path + operationId entries for the ticket-credential
//     handler (handleGetCredential) together with the
//     TicketCredentialItem component schema.
//   - Step 2: permission `credential.read` is mentioned and the
//     standard ErrorEnvelope is wired on every error status code.
//   - Step 3: minimal contract test validates the spec's response
//     example payloads against the schema (yaml parse + key presence
//     + invariants from credentials.go and migration
//     0027_ticket_credentials.sql — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas live under components.schemas with the
//     documented required fields and the canonical two-value type
//     enum from migration 0027_ticket_credentials.sql
//     (`ticket_credentials_type_check`).
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI273_PathsPresent verifies step 1: every credentials.go
// handler is documented under `paths:` in openapi.yaml.
func TestOpenAPI273_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/tickets/{id}/credential:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI273_OperationIDsPresent verifies the canonical operationId
// used by oapi-codegen / the TS client generator for each
// ticket-credential handler.
func TestOpenAPI273_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: getTicketCredential",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI273_SchemasPresent verifies step 4: every
// ticket-credential component schema is declared under
// components.schemas.
func TestOpenAPI273_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    TicketCredentialItem:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI273_PermissionsDocumented verifies step 2: the permission
// declared by mount_commerce.go for the credentials group is mentioned
// in the spec.
func TestOpenAPI273_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	// getTicketCredential must mention credential.read inside its op window.
	idx := strings.Index(spec, "operationId: getTicketCredential")
	if idx < 0 {
		t.Fatal("operationId: getTicketCredential missing")
	}
	end := idx + 6000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]
	if !strings.Contains(window, "credential.read") {
		t.Error("getTicketCredential does not mention `credential.read` permission")
	}
}

// TestOpenAPI273_BearerAuthAndTags verifies that the getTicketCredential
// operation declares `bearerAuth: []` and `tags: [v1]`.
func TestOpenAPI273_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: getTicketCredential"
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

// TestOpenAPI273_ErrorEnvelopeUsed verifies step 2: the
// getTicketCredential endpoint wires the standard ErrorEnvelope on its
// error responses.
func TestOpenAPI273_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	op := "operationId: getTicketCredential"
	idx := strings.Index(spec, op)
	if idx < 0 {
		t.Fatal("operation getTicketCredential missing")
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

// TestOpenAPI273_CredentialErrorCodesDocumented verifies that the spec
// documents the canonical credential error codes emitted by
// credentials.go.
func TestOpenAPI273_CredentialErrorCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"credential.invalid_ticket_id",
		"credential.invalid_type",
		"credential.fetch_failed",
		"credential.generation_failed",
		"credential.store_failed",
		"dependency.database_unavailable",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document credential code %q", code)
		}
	}
}

// TestOpenAPI273_TypeEnumPinned verifies that the
// TicketCredentialItem.type enum lists every value from the
// ticket_credentials_type_check constraint in migration
// 0027_ticket_credentials.sql: ('static_qr', 'pdf').
func TestOpenAPI273_TypeEnumPinned(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	idx := strings.Index(spec, "    TicketCredentialItem:")
	if idx < 0 {
		t.Fatal("TicketCredentialItem schema missing")
	}
	end := idx + 6000
	if end > len(spec) {
		end = len(spec)
	}
	window := spec[idx:end]

	for _, literal := range []string{
		"- static_qr",
		"- pdf",
	} {
		if !strings.Contains(window, literal) {
			t.Errorf("TicketCredentialItem.type enum missing literal %q", literal)
		}
	}
}

// TestOpenAPI273_SpecExamplesValidate is the "minimal contract test"
// called out by step 3. It parses openapi.yaml as YAML and walks the
// inline response examples for the GET /v1/tickets/{id}/credential
// handler, asserting per-handler invariants enforced in credentials.go
// and the underlying schema/migration:
//
//   - response payload has the six required fields (id, ticket_id,
//     type, payload, issued_at, revoked_at);
//   - id and ticket_id are UUID-shaped strings (handler emits
//     .String() on uuid.UUID);
//   - type is one of the two values pinned in the
//     `ticket_credentials_type_check` constraint;
//   - payload is a non-empty string. For static_qr, it must be exactly
//     64 lowercase hex characters (generateQRToken in credentials.go
//     reads 32 random bytes and hex-encodes them). For pdf, it must
//     be standard base64 (printable ASCII subset).
//   - issued_at looks RFC 3339 (handler emits .UTC().Format(
//     time.RFC3339)); revoked_at is null or looks RFC 3339.
//
// Runs in CI without docker / postgres / oapi-codegen.
func TestOpenAPI273_SpecExamplesValidate(t *testing.T) {
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

	isHex64 := func(s string) bool {
		if len(s) != 64 {
			return false
		}
		for _, c := range s {
			ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !ok {
				return false
			}
		}
		return true
	}

	// Base64 standard alphabet: A-Z, a-z, 0-9, +, /, =.
	isStdBase64 := func(s string) bool {
		if s == "" {
			return false
		}
		for _, c := range s {
			ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
				(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '='
			if !ok {
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

	validType := map[string]bool{
		"static_qr": true,
		"pdf":       true,
	}

	// ── GET /v1/tickets/{id}/credential ───────────────────────────────
	getOp := mustOperation(t, paths, "/v1/tickets/{id}/credential", "get")
	examples := extractResponseExamples(getOp)
	if len(examples) == 0 {
		t.Fatal("getTicketCredential must declare at least one response example")
	}
	for name, rawValue := range examples {
		cred, ok := rawValue.(map[string]any)
		if !ok {
			t.Errorf("credential example %q: value is not a mapping", name)
			continue
		}
		// Required string-UUID fields.
		for _, field := range []string{"id", "ticket_id"} {
			v, present := cred[field]
			if !present {
				t.Errorf("credential example %q: required field %q missing", name, field)
				continue
			}
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("credential example %q: %s %v is not a UUID-shaped string", name, field, v)
			}
		}
		// type must be in the migration's check constraint.
		credType, ok := cred["type"].(string)
		if !ok {
			t.Errorf("credential example %q: type %v must be a string", name, cred["type"])
		} else if !validType[credType] {
			t.Errorf("credential example %q: type %q not in ticket_credentials_type_check (static_qr|pdf)", name, credType)
		}
		// payload must be a non-empty string and shape-correct for its type.
		payload, ok := cred["payload"].(string)
		if !ok || payload == "" {
			t.Errorf("credential example %q: payload %v must be a non-empty string", name, cred["payload"])
		} else {
			switch credType {
			case "static_qr":
				if !isHex64(payload) {
					t.Errorf("credential example %q: static_qr payload must be 64 lowercase hex chars (got %d chars)", name, len(payload))
				}
			case "pdf":
				if !isStdBase64(payload) {
					t.Errorf("credential example %q: pdf payload must be standard base64", name)
				}
			}
		}
		// issued_at must be RFC 3339.
		v, present := cred["issued_at"]
		if !present {
			t.Errorf("credential example %q: required timestamp issued_at missing", name)
		} else {
			s, ok := v.(string)
			if !ok || !looksLikeRFC3339(s) {
				t.Errorf("credential example %q: issued_at %v does not look like RFC 3339", name, v)
			}
		}
		// revoked_at: must be present (required field), null OR RFC 3339.
		revokedRaw, present := cred["revoked_at"]
		if !present {
			t.Errorf("credential example %q: required field revoked_at missing", name)
		} else if revokedRaw != nil {
			s, ok := revokedRaw.(string)
			if !ok || !looksLikeRFC3339(s) {
				t.Errorf("credential example %q: revoked_at %v must be null or RFC 3339", name, revokedRaw)
			}
		}
	}
}
