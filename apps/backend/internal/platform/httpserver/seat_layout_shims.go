// seat_layout_shims.go bridges the seat-layout allocator routes (task S1e)
// to hseating: membership of {org_id} first, then the session must be the
// path organization's (a foreign or unknown session is the route's own 404).
package httpserver

import "net/http"

func (s *Server) handleListLayoutAlgorithms(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	if !s.requireSessionInPathOrg(w, r, "session_id") {
		return
	}
	s.seatingHandler().HandleListLayoutAlgorithms(w, r)
}

func (s *Server) handleSuggestSeatLayout(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	if !s.requireSessionInPathOrg(w, r, "session_id") {
		return
	}
	s.seatingHandler().HandleSuggestLayout(w, r)
}
