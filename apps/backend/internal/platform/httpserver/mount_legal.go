package httpserver

// mount_legal.go — the organizer's terms of sale (line P, task P3a): the
// public terms page of a hosted page slug, and the platform superadmin's
// template routes (legal.manage, platform_superadmin only).

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hlegal"
)

func (s *Server) legalHandler() *hlegal.Handler {
	trusted := 0
	if s.cfg != nil {
		trusted = s.cfg.TrustedProxyCount
	}
	var rl hlegal.IPLimiter
	if s.publicFeedRL != nil {
		rl = s.publicFeedRL
	}
	return hlegal.New(s.pgxPool, s.audit, s.logger, rl, trusted)
}

// legalTermsAPIBase is the base the buyer consent's terms link is built on
// (API_PUBLIC_URL, else APP_PUBLIC_URL). Empty keeps the platform terms link.
func (s *Server) legalTermsAPIBase() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.APIPublicBaseURL()
}

func (s *Server) mountLegalRoutes(r chi.Router) {
	// Public, like the hosted page it belongs to. The static segment "terms"
	// wins over the page route's {event_slug}; himports never mints the
	// event slug "terms" (reservedEventSlugs).
	r.Get("/public/pages/{org_slug}/terms", s.handlePublicTerms)

	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "legal.manage", "legal")
		pr.Get("/admin/legal-documents", s.handleListLegalDocuments)
		pr.Post("/admin/legal-documents", s.handleCreateLegalDocument)
		pr.Get("/admin/legal-documents/{id}", s.handleGetLegalDocument)
		pr.Post("/admin/legal-documents/{id}/publish", s.handlePublishLegalDocument)
	})
}

func (s *Server) handlePublicTerms(w http.ResponseWriter, r *http.Request) {
	s.legalHandler().HandlePublicTerms(w, r)
}

func (s *Server) handleListLegalDocuments(w http.ResponseWriter, r *http.Request) {
	s.legalHandler().HandleList(w, r)
}

func (s *Server) handleCreateLegalDocument(w http.ResponseWriter, r *http.Request) {
	s.legalHandler().HandleCreate(w, r)
}

func (s *Server) handleGetLegalDocument(w http.ResponseWriter, r *http.Request) {
	s.legalHandler().HandleGet(w, r)
}

func (s *Server) handlePublishLegalDocument(w http.ResponseWriter, r *http.Request) {
	s.legalHandler().HandlePublish(w, r)
}
