// imports_shims.go bridges the *Server god-object to the himports
// sub-package (feature #517, W1-C3c; spec §13.2). All handler and upsert logic
// lives in himports/; this file only constructs the handler from the server's
// dependencies and exposes the unexported *Server method the mount file binds.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/himports"
)

// importsHandler constructs a himports.Handler.
//
// The import touches venues, events, sessions, ticket tiers and the geo
// tables, all of which live on the same *gen.Queries surface; s.eventQueries
// is the handle that is always wired whenever the catalog routes are mounted,
// so it is the one used here.
func (s *Server) importsHandler() *himports.Handler {
	return himports.New(
		s.eventQueries,
		s.pool,
		s.audit,
		s.logger,
	).WithMembershipQueries(s.membershipQueries).
		WithMedia(s.media)
}

// ─── import handler shims ─────────────────────────────────────────────────────

func (s *Server) handleImportBil24Session(w http.ResponseWriter, r *http.Request) {
	s.importsHandler().HandleBil24Session(w, r)
}

// mountImportRoutes mounts the operator-facing bulk import surface
// (spec §13.2):
//
//	POST /v1/organizations/{org_id}/imports/bil24-session
//
// Gated on the `import.bil24_session` permission, which spec §13.1 lists among
// the scopes an organization API key may carry — the site-side import module
// (spec §13.4) is the primary caller.
func (s *Server) mountImportRoutes(r chi.Router) {
	if !s.authEnabled() || s.eventQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "import.bil24_session", "imports")
		pr.Post("/organizations/{org_id}/imports/bil24-session", s.handleImportBil24Session)
	})
}
