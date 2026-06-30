// networks_shims.go bridges the *Server god-object to the hnetworks sub-package.
// All handler and audit-helper logic lives in hnetworks/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount_networks.go compile unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hnetworks"
)

// Constant aliases so mount_networks.go and test files that reference the
// unexported names at package scope continue to compile without change.
const (
	networkAssignmentKindOrganizer = hnetworks.NetworkAssignmentKindOrganizer
	networkAssignmentKindAgent     = hnetworks.NetworkAssignmentKindAgent
)

// networksHandler constructs a hnetworks.Handler from the server's
// dependencies. Lightweight — just wraps three pointers.
func (s *Server) networksHandler() *hnetworks.Handler {
	return hnetworks.New(s.networkQueries, s.audit, s.logger)
}

// ──── operator-network CRUD shims (feature #208) ─────────────────────────────

func (s *Server) handleCreateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleCreateOperatorNetwork(w, r)
}

func (s *Server) handleListOperatorNetworks(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleListOperatorNetworks(w, r)
}

func (s *Server) handleGetOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleGetOperatorNetwork(w, r)
}

func (s *Server) handleUpdateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleUpdateOperatorNetwork(w, r)
}

func (s *Server) handleArchiveOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleArchiveOperatorNetwork(w, r)
}

func (s *Server) writeOperatorNetworkAudit(r *http.Request, action, networkID string, metadata map[string]any) {
	s.networksHandler().WriteOperatorNetworkAudit(r, action, networkID, metadata)
}

// ──── network-user assignment shims (feature #209) ───────────────────────────

func (s *Server) handleAssignNetworkUser(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleAssignNetworkUser(w, r)
}

func (s *Server) handleRemoveNetworkUser(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleRemoveNetworkUser(w, r)
}

func (s *Server) handleListNetworkUsers(w http.ResponseWriter, r *http.Request) {
	s.networksHandler().HandleListNetworkUsers(w, r)
}

func (s *Server) writeNetworkUserAudit(r *http.Request, action, networkID, userID string, metadata map[string]any) {
	s.networksHandler().WriteNetworkUserAudit(r, action, networkID, userID, metadata)
}

// ──── network-org assignment shims (feature #210) ────────────────────────────

func (s *Server) handleAttachNetworkOrganization(kind string) http.HandlerFunc {
	return s.networksHandler().HandleAttachNetworkOrganization(kind)
}

func (s *Server) handleDetachNetworkOrganization(kind string) http.HandlerFunc {
	return s.networksHandler().HandleDetachNetworkOrganization(kind)
}

func (s *Server) handleListNetworkOrganizations(kind string) http.HandlerFunc {
	return s.networksHandler().HandleListNetworkOrganizations(kind)
}

func (s *Server) writeNetworkOrgAudit(r *http.Request, action, networkID, orgID, kind string, metadata map[string]any) {
	s.networksHandler().WriteNetworkOrgAudit(r, action, networkID, orgID, kind, metadata)
}
