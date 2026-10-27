// seating_layout_store_shims.go bridges the saved-layout routes and the plan
// annotation routes (task S1d) to hseating. Every route carries {org_id}: the
// shim checks the membership of the path organization (a superadmin needs
// X-Admin-Reason), the session route also that the session is the
// organization's. The plan and layout ids are checked by the handlers against
// the path organization (a foreign or unknown id is the route's own 404).
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountSeatLayoutStoreRoutes registers the layout store. The gate is
// session.seats.manage like the rest of the seat surface (task S1f).
func (s *Server) mountSeatLayoutStoreRoutes(r chi.Router) {
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "session.seats.manage", "seating")
		plan := "/organizations/{org_id}/seating-plans/{plan_id}"
		pr.Get(plan+"/layouts", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleListSeatLayouts(w, r)
		}))
		pr.Post(plan+"/layouts", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleCreateSeatLayout(w, r)
		}))
		pr.Get(plan+"/layouts/{layout_id}", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleGetSeatLayout(w, r)
		}))
		pr.Patch(plan+"/layouts/{layout_id}", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleRenameSeatLayout(w, r)
		}))
		pr.Delete(plan+"/layouts/{layout_id}", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleDeleteSeatLayout(w, r)
		}))
		pr.Get(plan+"/annotations", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleGetPlanAnnotations(w, r)
		}))
		pr.Patch(plan+"/annotations", s.layoutOrgShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandlePatchPlanAnnotations(w, r)
		}))
		pr.Post("/organizations/{org_id}/sessions/{session_id}/layouts/apply", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleApplySeatLayout(w, r)
		}))
	})
}

func (s *Server) layoutOrgShim(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.enforceOrgMembership(w, r, "org_id") {
			return
		}
		next(w, r)
	}
}
