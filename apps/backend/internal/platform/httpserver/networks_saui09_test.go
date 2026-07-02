// networks_saui09_test.go — SAUI-09 audit-reason gate tests.
//
// Feature SAUI-09 closes the audit-reason gap on operator-network
// mutations by reusing the existing requireAdminReason helper
// (X-Admin-Reason header, error code "superadmin.missing_reason") on
// every mutation handler:
//
//	POST   /v1/operator-networks                              — create
//	PATCH  /v1/operator-networks/{id}                         — update
//	POST   /v1/operator-networks/{id}/archive                 — archive
//	POST   /v1/admin/networks/{id}/users                      — assign user
//	DELETE /v1/admin/networks/{id}/users/{userId}             — remove user
//	POST   /v1/admin/networks/{id}/{organizers|agents}        — attach org
//	DELETE /v1/admin/networks/{id}/{organizers|agents}/{orgId} — detach org
//
// The trimmed reason is stamped into the audit_events metadata under
// `reason` so the immutable audit trail records *why* each mutation
// was performed alongside the existing `network_id` / `target` keys.
//
// These tests exercise:
//
//   - the missing-reason rejection path (400 superadmin.missing_reason
//     before any DB or body parsing work happens);
//   - the present-reason ordering (the reason check fires before body
//     parsing, so the operator gets the audit-reason error first when
//     both are wrong);
//   - the audit metadata "reason" field is populated by the helper
//     when callers pass it through (covers the contract on the audit
//     side, complementing the request-side gate tests above).
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// reasonMissingErr is the error.code emitted by requireAdminReason on a
// missing/empty X-Admin-Reason header. Defined locally here so a rename
// at the superadmin layer surfaces as a single failing assertion
// instead of silently masking the contract drift.
const reasonMissingErr = "superadmin.missing_reason"

// newReasonGateServer builds the minimal Server fixture the SAUI-09
// gate tests need: a real networkQueries pointer (so the nil-check
// short-circuit does not fire) and a stubbed pool (so the handler
// will continue past the dependency.* 503 envelope). The handlers all
// short-circuit on the reason check *before* hitting any sqlc query,
// so we never actually exercise the gen.Queries methods.
func newReasonGateServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
		audit:          &captureAuditWriter{},
	}
}

// assertReasonMissing asserts that the recorded response is a 400
// envelope carrying the canonical reason-missing error code.
func assertReasonMissing(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 superadmin.missing_reason, got %d (body=%s)",
			rec.Code, rec.Body.String())
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != reasonMissingErr {
		t.Fatalf("error.code = %q want %q", code, reasonMissingErr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Operator-network CRUD endpoints
// ─────────────────────────────────────────────────────────────────────────────

func TestSAUI09_CreateOperatorNetwork_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"Net","slug":"net"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_CreateOperatorNetwork_RejectsBlankReason(t *testing.T) {
	// Whitespace-only reason must be rejected the same as a missing
	// header — operators cannot silently bypass the gate with " ".
	s := newReasonGateServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"Net","slug":"net"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "   ")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_UpdateOperatorNetwork_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	req := chiPathRequest(http.MethodPatch,
		"/v1/operator-networks/x",
		strings.NewReader(`{"name":"New"}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleUpdateOperatorNetwork(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_ArchiveOperatorNetwork_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	req := chiPathRequest(http.MethodPost,
		"/v1/operator-networks/x/archive", nil,
		map[string]string{"id": uuid.New().String()})
	rec := httptest.NewRecorder()
	s.handleArchiveOperatorNetwork(rec, req)
	assertReasonMissing(t, rec)
}

// ─────────────────────────────────────────────────────────────────────────────
// Roster mutations: assign/remove users, attach/detach organizations
// ─────────────────────────────────────────────────────────────────────────────

func TestSAUI09_AssignNetworkUser_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`"}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_RemoveNetworkUser_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	req := chiPathRequest(http.MethodDelete,
		"/v1/admin/networks/x/users/y", nil,
		map[string]string{"id": uuid.New().String(), "userId": uuid.New().String()})
	rec := httptest.NewRecorder()
	s.handleRemoveNetworkUser(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_AttachOrganizer_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	h := s.handleAttachNetworkOrganization(networkAssignmentKindOrganizer)
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/organizers",
		strings.NewReader(`{"organization_id":"`+uuid.New().String()+`"}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_DetachOrganizer_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	h := s.handleDetachNetworkOrganization(networkAssignmentKindOrganizer)
	req := chiPathRequest(http.MethodDelete,
		"/v1/admin/networks/x/organizers/y", nil,
		map[string]string{"id": uuid.New().String(), "orgId": uuid.New().String()})
	rec := httptest.NewRecorder()
	h(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_AttachAgent_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	h := s.handleAttachNetworkOrganization(networkAssignmentKindAgent)
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/agents",
		strings.NewReader(`{"organization_id":"`+uuid.New().String()+`"}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	assertReasonMissing(t, rec)
}

func TestSAUI09_DetachAgent_RejectsMissingReason(t *testing.T) {
	s := newReasonGateServer(t)
	h := s.handleDetachNetworkOrganization(networkAssignmentKindAgent)
	req := chiPathRequest(http.MethodDelete,
		"/v1/admin/networks/x/agents/y", nil,
		map[string]string{"id": uuid.New().String(), "orgId": uuid.New().String()})
	rec := httptest.NewRecorder()
	h(rec, req)
	assertReasonMissing(t, rec)
}

// ─────────────────────────────────────────────────────────────────────────────
// Reason ordering: a present X-Admin-Reason header lets the handler
// proceed past the SAUI-09 gate so subsequent validation surfaces.
// (We pair a valid reason with deliberately broken body so the
//  handler must reach the body-validation step — that proves the
//  reason check did not short-circuit it.)
// ─────────────────────────────────────────────────────────────────────────────

func TestSAUI09_CreateOperatorNetwork_PresentReasonProceedsToBodyValidation(t *testing.T) {
	s := newReasonGateServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 happy-path")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid_json, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.invalid_json" {
		t.Errorf("error.code = %q want operator_network.invalid_json (reason gate must "+
			"not mask body validation when the header is present)", code)
	}
}

func TestSAUI09_AssignNetworkUser_PresentReasonProceedsToBodyValidation(t *testing.T) {
	s := newReasonGateServer(t)
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader(`not-json`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 happy-path")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid_json, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "network_user.invalid_json" {
		t.Errorf("error.code = %q want network_user.invalid_json", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit metadata: the reason is stamped onto the emitted audit_events
// row so the immutable trail records *why* the mutation happened.
// We exercise the three audit helpers directly (paralleling the
// audit_215_test.go style) with a captureAuditWriter so the test is
// independent of the SQL path.
// ─────────────────────────────────────────────────────────────────────────────

func TestSAUI09_AuditMetadata_OperatorNetworkCarriesReason(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()

	s.writeOperatorNetworkAudit(req, "v1.operator_network.create", netID,
		map[string]any{"name": "Acme", "slug": "acme", "reason": "saui-09 onboarding"})

	ev := firstEvent(t, aw)
	if got, _ := ev.Metadata["reason"].(string); got != "saui-09 onboarding" {
		t.Errorf("metadata.reason = %q; want %q", got, "saui-09 onboarding")
	}
}

func TestSAUI09_AuditMetadata_NetworkUserCarriesReason(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()
	userID := uuid.New().String()

	s.writeNetworkUserAudit(req, "v1.network.users.assign", netID, userID,
		map[string]any{"role": "operator", "status": "active", "reason": "saui-09 staffing"})

	ev := firstEvent(t, aw)
	if got, _ := ev.Metadata["reason"].(string); got != "saui-09 staffing" {
		t.Errorf("metadata.reason = %q; want %q", got, "saui-09 staffing")
	}
}

func TestSAUI09_AuditMetadata_NetworkOrgCarriesReason(t *testing.T) {
	aw := &captureAuditWriter{}
	s := &Server{audit: aw, logger: discardLogger()}
	req := audit215Request(t, "platform_superadmin")
	netID := uuid.New().String()
	orgID := uuid.New().String()

	s.writeNetworkOrgAudit(req, "v1.network.organizers.attach", netID, orgID,
		networkAssignmentKindOrganizer,
		map[string]any{"status": "active", "reason": "saui-09 partner-onboarding"})

	ev := firstEvent(t, aw)
	if got, _ := ev.Metadata["reason"].(string); got != "saui-09 partner-onboarding" {
		t.Errorf("metadata.reason = %q; want %q", got, "saui-09 partner-onboarding")
	}
}
