package httpserver

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/mediaresolver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hinvitations"
)

// mountInvitationRoutes mounts the staff invitations of spec 46 (INV-02):
// issue and list per session, cancel, resend and the order-wide PDF per invitation, and the CSV
// batches (INV-06). Each handler loads the
// session or order scoped to {org_id}, so a foreign id answers 404.
func (s *Server) mountInvitationRoutes(r chi.Router) {
	if !s.authEnabled() || s.ticketQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.issue", "complimentary")
		pr.Post("/organizations/{org_id}/sessions/{session_id}/invitations", s.handleIssueInvitation)
		pr.Post("/organizations/{org_id}/invitations/{order_id}/resend", s.handleResendInvitation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.read", "complimentary")
		pr.Get("/organizations/{org_id}/sessions/{session_id}/invitations", s.handleListInvitations)
		pr.Get("/organizations/{org_id}/invitations/{order_id}/tickets.pdf", s.handleInvitationTicketsPDF)
	})
	// Bulk invitations from a CSV file (INV-06): the upload is a write
	// (complimentary.issue), the batch and its result file are reads.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.issue", "complimentary")
		pr.Post("/organizations/{org_id}/sessions/{session_id}/invitation-batches", s.handleCreateInvitationBatch)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.read", "complimentary")
		pr.Get("/organizations/{org_id}/invitation-batches/{id}", s.handleGetInvitationBatch)
		pr.Get("/organizations/{org_id}/invitation-batches/{id}/result.csv", s.handleInvitationBatchResult)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.revoke", "complimentary")
		pr.Post("/organizations/{org_id}/invitations/{order_id}/cancel", s.handleCancelInvitation)
	})
}

func (s *Server) invitationsHandler() *hinvitations.Handler {
	return hinvitations.New(s.pool, s.ticketQueries, s.audit, s.logger, s.ticketsHandler()).
		WithResender(s.ticketsHandler()).
		WithPDFRenderer(s.invitationPDFRenderer())
}

// invitationPDFRenderer renders the order-wide PDF of an invitation with the
// same resolution the e-mailed tickets get (seat, EAN-13, event, holder,
// branding, poster), using the API's own media store for the artwork.
func (s *Server) invitationPDFRenderer() hinvitations.PDFRenderer {
	var media delivery.MediaResolver
	if s.media != nil {
		media = mediaresolver.New(s.media, s.apiPublicURL(), s.logger)
	}
	opts := delivery.OrderPDFOptions{HandlerOptions: delivery.HandlerOptions{
		TicketQueries:     s.ticketQueries,
		CredentialQueries: s.credentialQueries,
		Media:             media,
		Logger:            s.logger,
	}}
	if s.cfg != nil {
		opts.TicketsBaseURL = s.cfg.PublicTicketsBaseURL
		opts.ContactFormEnabled = s.cfg.ContactFormReady()
	}
	return func(ctx context.Context, ticketIDs []uuid.UUID, locale string) ([]byte, error) {
		return delivery.RenderOrderPDF(ctx, opts, ticketIDs, locale, true)
	}
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

func (s *Server) handleResendInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleResend(w, r)
}

func (s *Server) handleInvitationTicketsPDF(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleTicketsPDF(w, r)
}

func (s *Server) handleCancelInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleCancel(w, r)
}

func (s *Server) handleCreateInvitationBatch(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleBatchCreate(w, r)
}

func (s *Server) handleGetInvitationBatch(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleBatchGet(w, r)
}

func (s *Server) handleInvitationBatchResult(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.invitationsHandler().HandleBatchResult(w, r)
}
