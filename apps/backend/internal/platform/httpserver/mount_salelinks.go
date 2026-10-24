package httpserver

// mount_salelinks.go — task QL-4 (spec 43 section 4.2): the organization routes
// of tracked sales links. Reads need sales_link.read, writes sales_link.manage;
// every route also needs membership of {org_id}, and the handlers load each
// link by (id, org_id), so another organization's link is the route's own 404.
// The QR image route (QL-5) is in the read group.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsalelinks"
)

func (s *Server) salesLinksHandler() *hsalelinks.Handler {
	short, tickets := "", ""
	if s.cfg != nil {
		short, tickets = s.cfg.ShortLinkBaseURL, s.cfg.PublicTicketsBaseURL
	}
	return hsalelinks.New(s.pgxPool, s.audit, s.logger, short, tickets)
}

func (s *Server) mountSalesLinkRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then, and the
	// route guards see the routes.
	if !s.authEnabled() {
		return
	}
	h := s.salesLinksHandler()
	const base = "/organizations/{org_id}/sales-links"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "sales_link.read", "sales_links")
		pr.Get(base, s.orgGated(h.HandleList))
		pr.Get(base+"/{id}", s.orgGated(h.HandleGet))
		pr.Get(base+"/{id}/stats", s.orgGated(h.HandleStats))
		pr.Get(base+"/{id}/qr", s.orgGated(h.HandleQR))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "sales_link.manage", "sales_links")
		pr.Post(base, s.orgGated(h.HandleCreate))
		pr.Patch(base+"/{id}", s.orgGated(h.HandlePatch))
	})
}
