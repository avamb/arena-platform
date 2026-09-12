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
		WithMedia(s.media).
		WithCatalogEventPublisher(s.publishCatalogEvent)
}

// ─── import handler shims ─────────────────────────────────────────────────────

func (s *Server) handleImportBil24Session(w http.ResponseWriter, r *http.Request) {
	s.importsHandler().HandleBil24Session(w, r)
}

// handleImportEventBundle serves POST .../imports/event-bundle (event-bundle
// spec §2). Same handler family as the legacy alias, but the source is not
// pinned — the request body must declare source=bil24|arena itself.
func (s *Server) handleImportEventBundle(w http.ResponseWriter, r *http.Request) {
	s.importsHandler().HandleEventBundle(w, r)
}

// mountImportRoutes mounts the operator-facing bulk import surface
// (spec §13.2, event-bundle spec §2):
//
//	POST /v1/organizations/{org_id}/imports/bil24-session   (legacy alias, source pinned to bil24)
//	POST /v1/organizations/{org_id}/imports/event-bundle    (source declared in body: bil24|arena)
//
// Gated on the `import.bil24_session` permission (reused for both routes —
// renaming it is cosmetic, deferred per event-bundle spec §2), which spec
// §13.1 lists among the scopes an organization API key may carry — the
// site-side import module (spec §13.4 / event-bundle spec §10) is the
// primary caller.
func (s *Server) mountImportRoutes(r chi.Router) {
	if !s.authEnabled() || s.eventQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "import.bil24_session", "imports")
		pr.Post("/organizations/{org_id}/imports/bil24-session", s.handleImportBil24Session)
		pr.Post("/organizations/{org_id}/imports/event-bundle", s.handleImportEventBundle)
	})
}
