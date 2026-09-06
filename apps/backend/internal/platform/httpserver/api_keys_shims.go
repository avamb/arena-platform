// api_keys_shims.go bridges the *Server god-object to the hapikeys
// sub-package (feature #514, W1-C1c). All handler and validation logic lives
// in hapikeys/; these thin delegating methods preserve the unexported
// *Server method surface so mount files (mount_iam.go) compile unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hapikeys"
)

// apiKeysHandler constructs a hapikeys.Handler from the server's
// dependencies.
func (s *Server) apiKeysHandler() *hapikeys.Handler {
	return hapikeys.New(
		s.apiKeyQueries,
		s.pool,
		s.audit,
		s.logger,
	).WithMembershipQueries(s.membershipQueries)
}

// ─── api key handler shims ────────────────────────────────────────────────────

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	s.apiKeysHandler().HandleList(w, r)
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	s.apiKeysHandler().HandleCreate(w, r)
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	s.apiKeysHandler().HandleRevoke(w, r)
}
