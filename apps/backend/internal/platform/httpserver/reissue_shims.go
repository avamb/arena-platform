package httpserver

import "net/http"

// Spec 63 ticket reissue routes: the membership check here, the permission in
// mount_orders.go, the org-scoped order lookup and the work in htickets and
// ticketreissue.

func (s *Server) handleReissueOptions(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleReissueOptions(w, r)
}

func (s *Server) handleReissueTickets(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleReissueTickets(w, r)
}

func (s *Server) handleReissueResend(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleReissueResend(w, r)
}

func (s *Server) handleReissueHistory(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleReissueHistory(w, r)
}

// Invitations without an order (spec 63 R-5).

func (s *Server) handleInvitationReissueOptions(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleInvitationReissueOptions(w, r)
}

func (s *Server) handleInvitationReissue(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleInvitationReissue(w, r)
}

func (s *Server) handleInvitationReissueResend(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleInvitationReissueResend(w, r)
}

func (s *Server) handleInvitationReissueHistory(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ticketsHandler().HandleInvitationReissueHistory(w, r)
}
