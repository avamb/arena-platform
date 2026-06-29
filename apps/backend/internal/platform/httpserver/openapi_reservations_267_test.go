// openapi_reservations_267_test.go pins the OpenAPI documentation contract
// established by feature #267 (Wave A-6): every reservation state-machine
// endpoint implemented in
// apps/backend/internal/platform/httpserver/reservations.go must be
// documented in apps/backend/openapi/openapi.yaml together with its
// component schemas, permissions, error envelope, and example payloads.
//
// Coverage matches feature #267 acceptance steps:
//
//   - Step 1: path + operation entries for every reservations.go handler
//   - Step 2: permissions are mentioned and the standard ErrorEnvelope is
//     wired on every endpoint
//   - Step 3: minimal contract test validates the spec's request examples
//     for the reservations group against the schema (yaml parse + key
//     presence + invariants — runs without docker / postgres /
//     oapi-codegen).
//   - Step 4: schemas (Reservation, ReservationEnvelope,
//     ReservationCancelEnvelope, CreateReservationRequest) live under
//     components.schemas with the documented required fields.
package httpserver

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPI267_PathsPresent verifies step 1: every reservation path from
// reservations.go is documented under `paths:` in openapi.yaml.
func TestOpenAPI267_PathsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"  /v1/reservations:",
		"  /v1/reservations/{id}:",
		"  /v1/reservations/{id}/activate:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI267_OperationIDsPresent verifies the canonical operationIds
// used by oapi-codegen / the TS client generator for the reservations
// group.
func TestOpenAPI267_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"operationId: createReservation",
		"operationId: getReservation",
		"operationId: cancelReservation",
		"operationId: activateReservation",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI267_SchemasPresent verifies step 4: every reservations-group
// component schema is declared under components.schemas.
func TestOpenAPI267_SchemasPresent(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	expected := []string{
		"    Reservation:",
		"    ReservationEnvelope:",
		"    ReservationCancelEnvelope:",
		"    CreateReservationRequest:",
	}
	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI267_PermissionsDocumented verifies step 2: every reservation
// permission used by mount_inventory.go is mentioned in the spec.
func TestOpenAPI267_PermissionsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, perm := range []string{
		"reservation.create",
		"reservation.read",
		"reservation.activate",
		"reservation.cancel",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}

// TestOpenAPI267_BearerAuthAndTags verifies every reservation operation
// declares `bearerAuth: []` and the `v1` tag.
func TestOpenAPI267_BearerAuthAndTags(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createReservation",
		"operationId: getReservation",
		"operationId: cancelReservation",
		"operationId: activateReservation",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			t.Errorf("operation %q missing from openapi.yaml", op)
			continue
		}
		end := idx + 4000
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

// TestOpenAPI267_ErrorEnvelopeUsed verifies step 2: every reservation
// endpoint wires the standard ErrorEnvelope for its error responses.
func TestOpenAPI267_ErrorEnvelopeUsed(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, op := range []string{
		"operationId: createReservation",
		"operationId: getReservation",
		"operationId: cancelReservation",
		"operationId: activateReservation",
	} {
		idx := strings.Index(spec, op)
		if idx < 0 {
			continue
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
}

// TestOpenAPI267_StateMachineCodesDocumented verifies that the spec
// documents the canonical state-machine error codes emitted by
// reservations.go (over_capacity on create, expired on activate,
// invalid_transition on activate/cancel).
func TestOpenAPI267_StateMachineCodesDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}
	spec := string(data)

	for _, code := range []string{
		"reservation.over_capacity",
		"reservation.expired",
		"reservation.invalid_transition",
		"reservation.not_found",
	} {
		if !strings.Contains(spec, code) {
			t.Errorf("openapi.yaml does not document reservation code %q", code)
		}
	}
}

// TestOpenAPI267_SpecExamplesValidate parses openapi.yaml as YAML and
// walks the reservations-group request examples, asserting that:
//
//   - createReservation examples each carry valid UUID-shaped
//     session_id / channel_id / org_id strings, a positive integer
//     quantity, and (when present) a valid UUID-shaped tier_id.
//
// This is the "minimal contract test" called out by step 3 — it runs in
// CI without docker / postgres / oapi-codegen because it only needs the
// YAML file itself.
func TestOpenAPI267_SpecExamplesValidate(t *testing.T) {
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

	// Lightweight UUID shape check: 36 chars, four dashes at the canonical
	// offsets. We deliberately avoid pulling in the uuid package — the
	// contract test should run on the raw YAML bytes alone.
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

	createOp := mustOperation(t, paths, "/v1/reservations", "post")
	examples := extractExamples(t, createOp)
	if len(examples) == 0 {
		t.Fatal("createReservation body must declare at least one example payload")
	}
	requiredUUIDFields := []string{"session_id", "channel_id", "org_id"}
	for name, ex := range examples {
		body, ok := ex.(map[string]any)
		if !ok {
			t.Errorf("create example %q: not a mapping", name)
			continue
		}
		for _, field := range requiredUUIDFields {
			v, present := body[field]
			if !present {
				t.Errorf("create example %q: missing required field %q", name, field)
				continue
			}
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("create example %q: field %q value %v is not a UUID-shaped string", name, field, v)
			}
		}
		if v, present := body["tier_id"]; present {
			s, ok := v.(string)
			if !ok || !looksLikeUUID(s) {
				t.Errorf("create example %q: optional tier_id %v is not a UUID-shaped string", name, v)
			}
		}
		qRaw, present := body["quantity"]
		if !present {
			t.Errorf("create example %q: missing required field %q", name, "quantity")
			continue
		}
		q, ok := asInt64(qRaw)
		if !ok {
			t.Errorf("create example %q: quantity %v is not an integer", name, qRaw)
			continue
		}
		if q <= 0 {
			t.Errorf("create example %q: quantity %d must be > 0 (handler rejects with reservation.invalid_quantity)", name, q)
		}
	}
}
