// openapi_networks_212_test.go pins the OpenAPI documentation contract
// established by feature #212: every operator-network endpoint introduced
// by features #208 / #209 / #210 / #211 must be documented in
// apps/backend/openapi/openapi.yaml together with its component schemas,
// security requirement, and tag, so the spec cannot silently drift from
// the chi router.
//
// Coverage matches feature #212 acceptance steps:
//
//   - Step 1: path entries for new endpoints
//   - Step 2: component schemas (OperatorNetwork, NetworkUserAssignment,
//     NetworkOrganizationAssignment, plus the MeResponse extension that
//     surfaces assigned_networks / available_scopes)
//   - Step 3: security tags consistent with the existing bearerAuth + tag
//     pattern used by other admin routes
//   - Step 4: spec parses as YAML (structural check; the heavier
//     openapi_drift_test / openapi_valid_test files cover full structural
//     validation)
//
// This file deliberately stays at the textual / structural level so it
// runs without docker, postgres, or oapi-codegen.
package httpserver

import (
	"os"
	"strings"
	"testing"
)

// readOpenAPISpec212 returns the raw bytes of openapi.yaml using the
// shared path resolver from openapi_drift_test.go.
func readOpenAPISpec212(t *testing.T) string {
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

// TestOpenAPI212_PathsPresent verifies step 1: every operator-network
// path is documented under `paths:` in openapi.yaml.
func TestOpenAPI212_PathsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	// We look for the YAML mapping key (two-space indent + path + colon)
	// so a stray reference inside a description or example cannot satisfy
	// the assertion.
	expected := []string{
		"  /v1/operator-networks:",
		"  /v1/operator-networks/{id}:",
		"  /v1/operator-networks/{id}/archive:",
		"  /v1/admin/networks/{id}/users:",
		"  /v1/admin/networks/{id}/users/{userId}:",
		"  /v1/admin/networks/{id}/organizers:",
		"  /v1/admin/networks/{id}/organizers/{orgId}:",
		"  /v1/admin/networks/{id}/agents:",
		"  /v1/admin/networks/{id}/agents/{orgId}:",
		"  /v1/me:",
	}

	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing path mapping %q", key)
		}
	}
}

// TestOpenAPI212_OperationIDsPresent verifies that every required operation
// is wired with its canonical operationId (used by oapi-codegen + the TS
// client generator).
func TestOpenAPI212_OperationIDsPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	expected := []string{
		"operationId: listOperatorNetworks",
		"operationId: createOperatorNetwork",
		"operationId: getOperatorNetwork",
		"operationId: updateOperatorNetwork",
		"operationId: archiveOperatorNetwork",
		"operationId: listNetworkUsers",
		"operationId: assignNetworkUser",
		"operationId: removeNetworkUser",
		"operationId: listNetworkOrganizers",
		"operationId: attachNetworkOrganizer",
		"operationId: detachNetworkOrganizer",
		"operationId: listNetworkAgents",
		"operationId: attachNetworkAgent",
		"operationId: detachNetworkAgent",
		"operationId: getV1Me",
	}

	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing %q", key)
		}
	}
}

// TestOpenAPI212_SchemasPresent verifies step 2: every component schema
// added or extended for the operator-network surface is documented under
// `components.schemas`.
func TestOpenAPI212_SchemasPresent(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	// Component schemas live at column-4 indent under components.schemas.
	expected := []string{
		"    OperatorNetwork:",
		"    OperatorNetworkEnvelope:",
		"    OperatorNetworkListResponse:",
		"    OperatorNetworkArchiveResponse:",
		"    CreateOperatorNetworkRequest:",
		"    UpdateOperatorNetworkRequest:",
		"    NetworkUserAssignment:",
		"    AssignNetworkUserRequest:",
		"    NetworkUserAssignResponse:",
		"    NetworkUserRemoveResponse:",
		"    NetworkUserListResponse:",
		"    NetworkOrganizationAssignment:",
		"    AttachNetworkOrganizationRequest:",
		"    NetworkOrganizationAttachResponse:",
		"    NetworkOrganizationDetachResponse:",
		"    NetworkOrganizersListResponse:",
		"    MeResponse:",
		"    MeAssignedNetwork:",
	}

	for _, key := range expected {
		if !strings.Contains(spec, key) {
			t.Errorf("openapi.yaml missing schema %q", key)
		}
	}
}

// TestOpenAPI212_MeResponseExtended verifies that MeResponse exposes both
// new top-level keys introduced by feature #211 so clients can rely on
// the operator-network context in the documented contract.
func TestOpenAPI212_MeResponseExtended(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	idx := strings.Index(spec, "    MeResponse:")
	if idx < 0 {
		t.Fatal("MeResponse schema not found")
	}
	// Take a window that ends at the next sibling schema entry. MeUser is
	// always emitted immediately after MeResponse (see openapi.yaml).
	tail := spec[idx:]
	if endIdx := strings.Index(tail, "    MeUser:"); endIdx > 0 {
		tail = tail[:endIdx]
	}

	for _, key := range []string{
		"assigned_networks",
		"available_scopes",
	} {
		if !strings.Contains(tail, key) {
			t.Errorf("MeResponse schema missing %q", key)
		}
	}
}

// TestOpenAPI212_NetworksTagRegistered verifies step 3: the `networks`
// tag is declared at the document root so admin operator-network routes
// group together in generated docs, matching the existing pattern.
func TestOpenAPI212_NetworksTagRegistered(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	if !strings.Contains(spec, "  - name: networks") {
		t.Error("openapi.yaml missing top-level tag `networks`")
	}
}

// TestOpenAPI212_AdminRoutesUseBearerAuth verifies step 3: every admin
// operator-network endpoint declares `bearerAuth: []` so the generated
// clients enforce the JWT requirement consistent with the rest of the
// admin surface.
func TestOpenAPI212_AdminRoutesUseBearerAuth(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	// Each operationId must be followed within ~40 lines by a security
	// stanza that lists bearerAuth.
	for _, op := range []string{
		"operationId: listOperatorNetworks",
		"operationId: createOperatorNetwork",
		"operationId: getOperatorNetwork",
		"operationId: updateOperatorNetwork",
		"operationId: archiveOperatorNetwork",
		"operationId: listNetworkUsers",
		"operationId: assignNetworkUser",
		"operationId: removeNetworkUser",
		"operationId: listNetworkOrganizers",
		"operationId: attachNetworkOrganizer",
		"operationId: detachNetworkOrganizer",
		"operationId: listNetworkAgents",
		"operationId: attachNetworkAgent",
		"operationId: detachNetworkAgent",
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
		if !strings.Contains(window, "tags: [v1, networks]") {
			t.Errorf("operation %q missing `tags: [v1, networks]`", op)
		}
	}
}

// TestOpenAPI212_PermissionDocumented verifies the description blocks
// reference the permission used to gate each route group, so the spec
// stays a useful source-of-truth for operators reading the docs.
func TestOpenAPI212_PermissionDocumented(t *testing.T) {
	t.Parallel()

	spec := readOpenAPISpec212(t)

	for _, perm := range []string{
		"network.read",
		"network.create",
		"network.update",
		"network.archive",
		"network.manage_users",
		"network.manage_organizers",
		"network.manage_agents",
	} {
		if !strings.Contains(spec, perm) {
			t.Errorf("openapi.yaml does not mention permission %q", perm)
		}
	}
}
