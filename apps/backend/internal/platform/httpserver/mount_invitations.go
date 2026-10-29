package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hinvitations"
)

// mountInvitationRoutes mounts the staff invitations of spec 46 (INV-02):
// issue and list per session, cancel per invitation. Each handler loads the
// session or order scoped to {org_id}, so a foreign id answers 404.
func (s *Server) mountInvitationRoutes(r chi.Router) {
	if !s.authEnabled() || s.ticketQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.issue", "complimentary")
		pr.Post("/organizations/{org_id}/sessions/{session_id}/invitations", s.handleIssueInvitation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.read", "complimentary")
		pr.Get("/organizations/{org_id}/sessions/{session_id}/invitations", s.handleListInvitations)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.revoke", "complimentary")
		pr.Post("/organizations/{org_id}/invitations/{order_id}/cancel", s.handleCancelInvitation)
	})
}

func (s *Server) invitationsHandler() *hinvitations.Handler {
	return hinvitations.New(s.pool, s.ticketQueries, s.audit, s.logger, s.ticketsHandler())
}

func (s *Server) handleIssueInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleIssue(w, r)
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleList(w, r)
}

func (s *Server) handleCancelInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleCancel(w, r)
}
