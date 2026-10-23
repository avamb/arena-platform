// seating_batch_shims.go bridges the seat-batch routes (task S1c) to the
// hseating sub-package. Every route carries {org_id} and {session_id}: the
// shim checks the membership of the path organization and that the session
// belongs to it (a foreign session is the route's own 404) before the
// handler runs; seatlayout scopes every query by (session, org) again.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountSeatBatchRoutes registers the batch surface. The gate is
// session.seats.manage (task S1f, migration 0161), like the PATCH routes it
// replaces.
func (s *Server) mountSeatBatchRoutes(r chi.Router) {
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "session.seats.manage", "seating")
		base := "/organizations/{org_id}/sessions/{session_id}"
		pr.Post(base+"/seat-batches", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleCreateSeatBatch(w, r)
		}))
		pr.Get(base+"/seat-batches", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleListSeatBatches(w, r)
		}))
		pr.Get(base+"/seat-batches/{batch_id}", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleGetSeatBatch(w, r)
		}))
		pr.Post(base+"/seat-batches/{batch_id}/revert", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleRevertSeatBatch(w, r)
		}))
		pr.Get(base+"/seats/{seat_key}/history", s.seatBatchShim(func(w http.ResponseWriter, r *http.Request) {
			s.seatingHandler().HandleSeatHistory(w, r)
		}))
	})
}

func (s *Server) seatBatchShim(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.enforceOrgMembership(w, r, "org_id") {
			return
		}
		if !s.requireSessionInPathOrg(w, r, "session_id") {
			return
		}
		next(w, r)
	}
}
