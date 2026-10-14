package httpserver

// mount_seller.go — the owner's "Organization data" routes (HP-4a): the
// seller profile of the hosted sales page and the per-channel seller-block
// settings. Both need org.update in the path organization (the owner).

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseller"
)

func (s *Server) sellerHandler() *hseller.Handler {
	return hseller.New(s.pgxPool, s.audit, s.logger)
}

func (s *Server) mountSellerRoutes(r chi.Router) {
	// Mounted without a database too: the handler answers 503 then, and the
	// route guards see the route.
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Get("/organizations/{org_id}/seller-profile", s.handleGetSellerProfile)
		pr.Patch("/organizations/{org_id}/seller-profile", s.handlePatchSellerProfile)
		pr.Patch("/organizations/{org_id}/channels/{channel_id}/seller-display", s.handlePatchSellerDisplay)
	})
}

func (s *Server) handleGetSellerProfile(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.sellerHandler().HandleGetProfile(w, r)
}

// handlePatchSellerDisplay: the channel is looked up inside the path
// organization only (hseller answers 404 for another organization's id).
func (s *Server) handlePatchSellerDisplay(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.sellerHandler().HandlePatchDisplay(w, r)
}

func (s *Server) handlePatchSellerProfile(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.sellerHandler().HandlePatchProfile(w, r)
}
