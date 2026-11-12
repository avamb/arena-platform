// mount_seating_admin.go registers the verification of a seating plan by a
// platform superadmin and the verified templates of a venue (spec
// 08_architecture/64 section 3.4, migration 0216), served by hseating/verify.go.
//
//	GET  /v1/admin/seating-plans                                     seating_plan.verify + superadmin
//	POST /v1/admin/seating-plans/{plan_id}/verify                    seating_plan.verify + superadmin + X-Admin-Reason
//	POST /v1/admin/seating-plans/{plan_id}/needs-fix                 seating_plan.verify + superadmin + X-Admin-Reason
//	GET  /v1/organizations/{org_id}/venues/{venue_id}/plan-templates seating_plan.read + membership of {org_id}
//
// seating_plan.verify (migration 0057) is held by platform roles only and is
// refused as an API-key scope (apikeys.ForbiddenScopeExact).
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountSeatingAdminRoutes is called by mountSeatingRoutes after its own
// auth/queries/pool gate.
func (s *Server) mountSeatingAdminRoutes(r chi.Router) {
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.verify", "seating")
		pr.Get("/admin/seating-plans", func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleAdminListSeatingPlans(w, r)
		})
		pr.Post("/admin/seating-plans/{plan_id}/verify", func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleVerifySeatingPlan(w, r)
		})
		pr.Post("/admin/seating-plans/{plan_id}/needs-fix", func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleNeedsFixSeatingPlan(w, r)
		})
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.read", "seating")
		pr.Get("/organizations/{org_id}/venues/{venue_id}/plan-templates", func(w http.ResponseWriter, r *http.Request) {
			if !s.enforceOrgMembership(w, r, "org_id") {
				return
			}
			s.seatingHandler().HandleListPlanTemplates(w, r)
		})
	})
}
