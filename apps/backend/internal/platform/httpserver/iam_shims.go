// iam_shims.go bridges the *Server god-object to the hiam sub-package.
// All handler and validation logic lives in hiam/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount files compile unchanged.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hiam"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// iamHandler constructs a hiam.Handler from the server's dependencies.
func (s *Server) iamHandler() *hiam.Handler {
	return hiam.New(
		s.orgQueries,
		s.membershipQueries,
		s.superadminQueries,
		s.pool,
		s.audit,
		s.logger,
		s.stub,
	)
}

// ─── const forwarders ────────────────────────────────────────────────────────

const maxImpersonationDuration = time.Duration(hiam.MaxImpersonationDuration)
const defaultImpersonationDuration = time.Duration(hiam.DefaultImpersonationDuration)

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hiam without importing that package directly.

type orgResponse = hiam.OrgResponse

// ─── var forwarders ──────────────────────────────────────────────────────────

// validMembershipRoles forwards hiam.ValidMembershipRoles so that
// memberships_network_operator_203_test.go (package httpserver) can inspect
// the allowed roles map without importing hiam directly.
var validMembershipRoles = hiam.ValidMembershipRoles

// ─── function forwarders ─────────────────────────────────────────────────────

func membershipRoleList() []string { return hiam.MembershipRoleList() }

//nolint:unused // source-grep witness: superadmin_166_test.go asserts the symbol name.
func requireAdminReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	return httputil.RequireAdminReason(w, r)
}

// ─── org handler shims ────────────────────────────────────────────────────────

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleCreateOrg(w, r)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleListOrgs(w, r)
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleGetOrg(w, r)
}

func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleUpdateOrg(w, r)
}

func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleDeleteOrg(w, r)
}

// ─── membership handler shims ─────────────────────────────────────────────────

func (s *Server) handleGrantMembership(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleGrantMembership(w, r)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleListMembers(w, r)
}

func (s *Server) handleRevokeMembership(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleRevokeMembership(w, r)
}

// ─── superadmin handler shims ─────────────────────────────────────────────────

func (s *Server) handleSuperadminListOrganizations(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleSuperadminListOrganizations(w, r)
}

func (s *Server) handleSuperadminListOrders(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleSuperadminListOrders(w, r)
}

func (s *Server) handleSuperadminListTickets(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleSuperadminListTickets(w, r)
}

func (s *Server) handleSuperadminListRefunds(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleSuperadminListRefunds(w, r)
}

// ─── impersonation handler shims ──────────────────────────────────────────────

func (s *Server) handleImpersonate(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleImpersonate(w, r)
}

// ─── admin org handler shims ──────────────────────────────────────────────────

func (s *Server) handleAdminCreateOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminCreateOrg(w, r)
}

func (s *Server) handleAdminUpdateOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminUpdateOrg(w, r)
}

func (s *Server) handleAdminArchiveOrg(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminArchiveOrg(w, r)
}

// ─── admin membership handler shims ───────────────────────────────────────────

func (s *Server) handleAdminListMembers(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminListMembers(w, r)
}

func (s *Server) handleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminAddMember(w, r)
}

func (s *Server) handleAdminChangeMemberRole(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminChangeMemberRole(w, r)
}

func (s *Server) handleAdminDeactivateMember(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminDeactivateMember(w, r)
}

// ─── admin user handler shims ─────────────────────────────────────────────────

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	s.iamHandler().HandleAdminCreateUser(w, r)
}
