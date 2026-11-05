package httpserver

// mount_org_modules.go — the paid modules of an organization (spec
// 08_architecture/61 section 4.4). Only a platform superadmin connects, ends
// and re-prices a module: the routes need superadmin.read AND the platform
// superadmin marker (an organization owner gets 403), every write needs
// X-Admin-Reason and writes its audit row with the reason on the change's
// transaction (hmodules). The organizer reads the periods and the price
// through the billing section, read-only like the white label route.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmodules"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

func (s *Server) orgModulesHandler() *hmodules.Handler {
	return hmodules.New(s.pgxPool, s.audit, s.logger)
}

// requireModulesSuperadmin lets only a platform superadmin through.
func (s *Server) requireModulesSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasSuperadminOrgAccess(r.Context()) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"org_module.superadmin_only", "only a platform superadmin connects or disconnects modules", r))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mountOrgModuleRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then.
	if !s.authEnabled() {
		return
	}
	h := s.orgModulesHandler()
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "superadmin")
		pr.Use(s.requireModulesSuperadmin)
		pr.Get("/admin/organizations/{org_id}/modules", h.HandleAdminList)
		pr.Post("/admin/organizations/{org_id}/modules/shop", h.HandleConnectShop)
		pr.Post("/admin/org-modules/{id}/end", h.HandleEnd)
		pr.Post("/admin/org-modules/{id}/reprice", h.HandleReprice)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.read", "billing")
		pr.Get("/organizations/{org_id}/billing/modules", func(w http.ResponseWriter, r *http.Request) {
			if !s.enforceOrgMembership(w, r, "org_id") {
				return
			}
			h.HandleOrgModules(w, r)
		})
	})
}
