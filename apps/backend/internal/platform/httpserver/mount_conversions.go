package httpserver

// mount_conversions.go - the owner's server-side conversion settings (spec 62
// section 8.3, П-9): Meta pixel + CAPI token, GA4 measurement id + secret.
// org.update in the path organization (the owner); the secrets are write-only.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hconversions"
)

func (s *Server) conversionsHandler() *hconversions.Handler {
	return hconversions.New(s.pgxPool, s.audit)
}

func (s *Server) mountConversionRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Get("/organizations/{org_id}/conversion-settings", s.handleGetConversionSettings)
		pr.Patch("/organizations/{org_id}/conversion-settings", s.handlePatchConversionSettings)
	})
}

func (s *Server) handleGetConversionSettings(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.conversionsHandler().HandleGet(w, r)
}

func (s *Server) handlePatchConversionSettings(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.conversionsHandler().HandlePatch(w, r)
}
