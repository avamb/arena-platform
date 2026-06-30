// audit_215_test.go — unit tests for feature #215
// (cross-network and platform-level actions are auditable).
//
// Feature #215 verifies and extends the audit emission already wired by
// #208 (operator-network CRUD), #209 (network user assignment), and
// #210 (organizer/agent attach/detach). Each mutating handler must
// produce one audit_events row with:
//
//   - actor        — captured from auth.ActorFromContext (ActorType+ActorID)
//   - action       — a versioned, dotted verb ("v1.operator_network.create",
//                    "v1.network.users.assign", "v1.network.organizers.attach", …)
//   - resource     — ResourceType / ResourceID identifying the row
//   - network_id   — metadata key tagging the originating operator_network
//                    (so audit consumers can index by network without parsing
//                    the resource_type column)
//   - target       — metadata key tagging the primary subject of the mutation
//                    (the network itself for operator_network mutations, the
//                    user_id for network_users mutations, the organization_id
//                    for network_organizations mutations)
//
// These tests exercise the three audit helpers (writeOperatorNetworkAudit,
// writeNetworkUserAudit, writeNetworkOrgAudit) directly with a
// captureAuditWriter so we can assert the Event values without spinning up
// a database. They are deliberately decoupled from the DB-driven happy-path
// in #208/#209/#210 — the contract under test here is the audit emission
// contract, not the SQL path.
package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// discardLogger returns a slog.Logger that swallows output, sufficient for
// the warn paths inside the audit helpers (we exercise the success path so
// the logger is never actually invoked, but a non-nil Server.logger is
// required to avoid a nil dereference if a path ever changes).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// audit215Actor is the canonical fake actor used across the #215 tests.
const audit215ActorID = "aaaaaaaa-0000-0000-0000-000000000215"

// audit215Request builds a request carrying an authenticated actor in its
// context so the audit helpers exercise the "user" actor_type branch.
func audit215Request(t *testing.T, roles ...string) *http.Request {
	t.Helper()
	if len(roles) == 0 {
		roles = []string{"network_operator"}
	}
	req := httptest.NewRequest("POST", "/", nil)
	ctx := auth.WithActor(req.Context(), auth.Actor{
		ID:    audit215ActorID,
		Roles: roles,
	})
	return req.WithContext(ctx)
}

// firstEvent returns the first captured Event or fails the test.
func firstEvent(t *testing.T, aw *captureAuditWriter) audit.Event {
	t.Helper()
	ev := aw.getEvents()
	if len(ev) == 0 {
		t.Fatalf("audit writer captured no events")
	}
	return ev[0]
}

// ─────────────────────────────────────────────────────────────────────────────
// writeOperatorNetworkAudit — feature #208 (operator-network CRUD)
// ─────────────────────────────────────────────────────────────────────────────

func TestAudit215_OperatorNetwork_CreateEventCarriesAllFields(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()

	s.writeOperatorNetworkAudit(req, "v1.operator_network.create", netID,
		map[string]any{"name": "Acme Net", "slug": "acme-net"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.operator_network.create" {
		t.Errorf("Action = %q; want v1.operator_network.create", ev.Action)
	}
	if ev.ResourceType != "operator_network" {
		t.Errorf("ResourceType = %q; want operator_network", ev.ResourceType)
	}
	if ev.ResourceID != netID {
		t.Errorf("ResourceID = %q; want %q", ev.ResourceID, netID)
	}
	if ev.ActorType != "user" {
		t.Errorf("ActorType = %q; want user", ev.ActorType)
	}
	if ev.ActorID != audit215ActorID {
		t.Errorf("ActorID = %q; want %q", ev.ActorID, audit215ActorID)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["target"].(string); got != netID {
		t.Errorf("metadata.target = %q; want %q (network is its own target)", got, netID)
	}
	if got, _ := ev.Metadata["name"].(string); got != "Acme Net" {
		t.Errorf("metadata.name = %q; want Acme Net", got)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("OccurredAt should be set")
	}
}

func TestAudit215_OperatorNetwork_UpdateActionAndMetadata(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t)
	netID := uuid.New().String()

	s.writeOperatorNetworkAudit(req, "v1.operator_network.update", netID,
		map[string]any{"name_changed": true, "slug_changed": false})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.operator_network.update" {
		t.Errorf("Action = %q; want v1.operator_network.update", ev.Action)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["name_changed"].(bool); !got {
		t.Errorf("metadata.name_changed should be preserved")
	}
}

func TestAudit215_OperatorNetwork_ArchiveActionAndMetadata(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()

	s.writeOperatorNetworkAudit(req, "v1.operator_network.archive", netID,
		map[string]any{"slug": "acme"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.operator_network.archive" {
		t.Errorf("Action = %q; want v1.operator_network.archive", ev.Action)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["target"].(string); got != netID {
		t.Errorf("metadata.target = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["slug"].(string); got != "acme" {
		t.Errorf("metadata.slug should be preserved")
	}
}

func TestAudit215_OperatorNetwork_AnonymousActorTagged(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	// No actor in context — exercises the anonymous fallback.
	req := httptest.NewRequest("POST", "/", nil).
		WithContext(context.Background())
	netID := uuid.New().String()

	s.writeOperatorNetworkAudit(req, "v1.operator_network.create", netID, nil)

	ev := firstEvent(t, aw)
	if ev.ActorType != "anonymous" {
		t.Errorf("ActorType = %q; want anonymous", ev.ActorType)
	}
	if ev.ActorID != "" {
		t.Errorf("ActorID = %q; want empty for anonymous", ev.ActorID)
	}
	// network_id must still be present regardless of actor.
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
}

func TestAudit215_OperatorNetwork_NilAuditWriterIsSafe(t *testing.T) {
	// writeOperatorNetworkAudit must early-return rather than panic when the
	// server has no audit writer wired (this is the production code's
	// fire-and-forget contract — see networks.go).
	s := &Server{audit: nil, logger: discardLogger()}
	req := audit215Request(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("writeOperatorNetworkAudit panicked with nil writer: %v", r)
		}
	}()
	s.writeOperatorNetworkAudit(req, "v1.operator_network.create",
		uuid.New().String(), map[string]any{})
}

// ─────────────────────────────────────────────────────────────────────────────
// writeNetworkUserAudit — feature #209 (network user assignment)
// ─────────────────────────────────────────────────────────────────────────────

func TestAudit215_NetworkUser_AssignEventCarriesAllFields(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()
	userID := uuid.New().String()

	s.writeNetworkUserAudit(req, "v1.network.users.assign", netID, userID,
		map[string]any{"role": "operator", "status": "active"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.network.users.assign" {
		t.Errorf("Action = %q; want v1.network.users.assign", ev.Action)
	}
	if ev.ResourceType != "network_user" {
		t.Errorf("ResourceType = %q; want network_user", ev.ResourceType)
	}
	wantRID := netID + ":" + userID
	if ev.ResourceID != wantRID {
		t.Errorf("ResourceID = %q; want %q", ev.ResourceID, wantRID)
	}
	if ev.ActorID != audit215ActorID {
		t.Errorf("ActorID = %q; want %q", ev.ActorID, audit215ActorID)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["target_user_id"].(string); got != userID {
		t.Errorf("metadata.target_user_id = %q; want %q", got, userID)
	}
	if got, _ := ev.Metadata["target"].(string); got != userID {
		t.Errorf("metadata.target = %q; want %q (user is the target)", got, userID)
	}
	if got, _ := ev.Metadata["role"].(string); got != "operator" {
		t.Errorf("metadata.role preserved expected")
	}
}

func TestAudit215_NetworkUser_RemoveActionAndTarget(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()
	userID := uuid.New().String()

	s.writeNetworkUserAudit(req, "v1.network.users.remove", netID, userID,
		map[string]any{"status": "revoked"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.network.users.remove" {
		t.Errorf("Action = %q; want v1.network.users.remove", ev.Action)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["target"].(string); got != userID {
		t.Errorf("metadata.target = %q; want %q", got, userID)
	}
	if got, _ := ev.Metadata["status"].(string); got != "revoked" {
		t.Errorf("metadata.status should be preserved")
	}
}

func TestAudit215_NetworkUser_ReactivateAction(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t)
	netID := uuid.New().String()
	userID := uuid.New().String()

	s.writeNetworkUserAudit(req, "v1.network.users.reactivate", netID, userID, nil)
	ev := firstEvent(t, aw)
	if ev.Action != "v1.network.users.reactivate" {
		t.Errorf("Action = %q; want v1.network.users.reactivate", ev.Action)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
}

func TestAudit215_NetworkUser_NetworkOperatorActorTagged(t *testing.T) {
	// network_operator is the role that should NOT in practice hit
	// network.manage_users (per 0044_network_permissions.sql), but the audit
	// helper itself is role-agnostic — if a network_operator did mutate a
	// roster (e.g. a future feature), the audit row must still carry their
	// actor_id and the originating network_id so the action is traceable.
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "network_operator")
	netID := uuid.New().String()
	userID := uuid.New().String()

	s.writeNetworkUserAudit(req, "v1.network.users.assign", netID, userID, nil)
	ev := firstEvent(t, aw)
	if ev.ActorType != "user" {
		t.Errorf("ActorType = %q; want user", ev.ActorType)
	}
	if ev.ActorID != audit215ActorID {
		t.Errorf("ActorID = %q; want %q", ev.ActorID, audit215ActorID)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q (originating network)",
			got, netID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// writeNetworkOrgAudit — feature #210 (organizer/agent attach/detach)
// ─────────────────────────────────────────────────────────────────────────────

func TestAudit215_NetworkOrg_AttachOrganizerCarriesAllFields(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "network_operator")
	netID := uuid.New().String()
	orgID := uuid.New().String()

	s.writeNetworkOrgAudit(req, "v1.network.organizers.attach",
		netID, orgID, networkAssignmentKindOrganizer,
		map[string]any{"status": "active"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.network.organizers.attach" {
		t.Errorf("Action = %q; want v1.network.organizers.attach", ev.Action)
	}
	if ev.ResourceType != "network_organization" {
		t.Errorf("ResourceType = %q; want network_organization", ev.ResourceType)
	}
	wantRID := netID + ":" + orgID + ":" + networkAssignmentKindOrganizer
	if ev.ResourceID != wantRID {
		t.Errorf("ResourceID = %q; want %q", ev.ResourceID, wantRID)
	}
	if ev.ActorID != audit215ActorID {
		t.Errorf("ActorID = %q; want %q", ev.ActorID, audit215ActorID)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q (originating network)",
			got, netID)
	}
	if got, _ := ev.Metadata["organization_id"].(string); got != orgID {
		t.Errorf("metadata.organization_id = %q; want %q", got, orgID)
	}
	if got, _ := ev.Metadata["target"].(string); got != orgID {
		t.Errorf("metadata.target = %q; want %q", got, orgID)
	}
	if got, _ := ev.Metadata["assignment_kind"].(string); got != "organizer" {
		t.Errorf("metadata.assignment_kind = %q; want organizer", got)
	}
}

func TestAudit215_NetworkOrg_DetachAgentActionAndTarget(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "network_operator")
	netID := uuid.New().String()
	orgID := uuid.New().String()

	s.writeNetworkOrgAudit(req, "v1.network.agents.detach",
		netID, orgID, networkAssignmentKindAgent,
		map[string]any{"status": "revoked"})

	ev := firstEvent(t, aw)
	if ev.Action != "v1.network.agents.detach" {
		t.Errorf("Action = %q; want v1.network.agents.detach", ev.Action)
	}
	if got, _ := ev.Metadata["assignment_kind"].(string); got != "agent" {
		t.Errorf("metadata.assignment_kind = %q; want agent", got)
	}
	if got, _ := ev.Metadata["network_id"].(string); got != netID {
		t.Errorf("metadata.network_id = %q; want %q", got, netID)
	}
	if got, _ := ev.Metadata["target"].(string); got != orgID {
		t.Errorf("metadata.target = %q; want %q", got, orgID)
	}
}

func TestAudit215_NetworkOrg_ReactivateBothKinds(t *testing.T) {
	for _, kind := range []string{networkAssignmentKindOrganizer, networkAssignmentKindAgent} {
		t.Run(kind, func(t *testing.T) {
			aw := &captureAuditWriter{}
			s := &Server{audit: aw, logger: discardLogger()}
			req := audit215Request(t)
			netID := uuid.New().String()
			orgID := uuid.New().String()

			action := "v1.network." + kind + "s.reactivate"
			s.writeNetworkOrgAudit(req, action, netID, orgID, kind,
				map[string]any{"status": "active"})

			ev := firstEvent(t, aw)
			if ev.Action != action {
				t.Errorf("Action = %q; want %q", ev.Action, action)
			}
			if got, _ := ev.Metadata["assignment_kind"].(string); got != kind {
				t.Errorf("metadata.assignment_kind = %q; want %q", got, kind)
			}
			if got, _ := ev.Metadata["network_id"].(string); got != netID {
				t.Errorf("metadata.network_id = %q; want %q", got, netID)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Source-file content guards — ensure the audit emission stays wired into
// each mutation site so a future refactor cannot silently strip it.
// ─────────────────────────────────────────────────────────────────────────────

func TestAudit215_NetworksGoEmitsAuditOnAllMutations(t *testing.T) {
	src := readSourceFile(t, "hnetworks/networks.go")
	for _, want := range []string{
		`WriteOperatorNetworkAudit(r, "v1.operator_network.create"`,
		`WriteOperatorNetworkAudit(r, "v1.operator_network.update"`,
		`WriteOperatorNetworkAudit(r, "v1.operator_network.archive"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/networks.go missing audit call %q", want)
		}
	}
	// The helper itself must stamp network_id + target into metadata.
	for _, want := range []string{
		`metadata["network_id"] = networkID`,
		`metadata["target"] = networkID`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/networks.go: WriteOperatorNetworkAudit missing %q", want)
		}
	}
}

func TestAudit215_NetworkUsersGoEmitsAuditOnAllMutations(t *testing.T) {
	src := readSourceFile(t, "hnetworks/network_users.go")
	for _, want := range []string{
		`WriteNetworkUserAudit(r, "v1.network.users.assign"`,
		`WriteNetworkUserAudit(r, "v1.network.users.reactivate"`,
		`WriteNetworkUserAudit(r, "v1.network.users.remove"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/network_users.go missing audit call %q", want)
		}
	}
	for _, want := range []string{
		`metadata["network_id"] = networkID`,
		`metadata["target_user_id"] = userID`,
		`metadata["target"] = userID`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/network_users.go: WriteNetworkUserAudit missing %q", want)
		}
	}
}

func TestAudit215_NetworkOrgsGoEmitsAuditOnAllMutations(t *testing.T) {
	src := readSourceFile(t, "hnetworks/network_orgs.go")
	for _, want := range []string{
		`"v1.network."+kind+"s.attach"`,
		`"v1.network."+kind+"s.reactivate"`,
		`"v1.network."+kind+"s.detach"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/network_orgs.go missing audit call template %q", want)
		}
	}
	for _, want := range []string{
		`metadata["network_id"] = networkID`,
		`metadata["organization_id"] = orgID`,
		`metadata["assignment_kind"] = kind`,
		`metadata["target"] = orgID`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hnetworks/network_orgs.go: WriteNetworkOrgAudit missing %q", want)
		}
	}
}
