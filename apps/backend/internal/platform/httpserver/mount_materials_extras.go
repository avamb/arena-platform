package httpserver

// mount_materials_extras.go — task 1-2b: the "where else to buy" partner
// links, the map point and the photo report of an event (hmaterials). Reads
// need event.read, writes event.update; every route also needs membership of
// {org_id} and ties the event to it inside the handler.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmaterials"
)

func (s *Server) materialsExtrasHandler() *hmaterials.ExtrasHandler {
	return hmaterials.NewExtras(s.pgxPool, s.audit, s.logger)
}

// orgGated wraps a materials handler with the {org_id} membership check.
func (s *Server) orgGated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.enforceOrgMembership(w, r, "org_id") {
			return
		}
		h(w, r)
	}
}

func (s *Server) mountMaterialsExtrasRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then, and the
	// route guards see the routes.
	if !s.authEnabled() {
		return
	}
	h := s.materialsExtrasHandler()
	const ev = "/organizations/{org_id}/events/{event_id}"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.read", "events")
		pr.Get(ev+"/partner-links", s.orgGated(h.HandleListPartnerLinks))
		pr.Get(ev+"/map-point", s.orgGated(h.HandleGetMapPoint))
		pr.Get(ev+"/photo-report", s.orgGated(h.HandleListReportPhotos))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.update", "events")
		pr.Post(ev+"/partner-links", s.orgGated(h.HandleCreatePartnerLink))
		pr.Put(ev+"/partner-links/order", s.orgGated(h.HandleReorderPartnerLinks))
		pr.Patch(ev+"/partner-links/{link_id}", s.orgGated(h.HandlePatchPartnerLink))
		pr.Delete(ev+"/partner-links/{link_id}", s.orgGated(h.HandleDeletePartnerLink))
		pr.Put(ev+"/map-point", s.orgGated(h.HandlePutMapPoint))
		pr.Delete(ev+"/map-point", s.orgGated(h.HandleDeleteMapPoint))
		pr.Post(ev+"/photo-report", s.orgGated(h.HandleCreateReportPhoto))
		pr.Put(ev+"/photo-report/order", s.orgGated(h.HandleReorderReportPhotos))
		pr.Patch(ev+"/photo-report/{photo_id}", s.orgGated(h.HandlePatchReportPhoto))
		pr.Delete(ev+"/photo-report/{photo_id}", s.orgGated(h.HandleDeleteReportPhoto))
	})
}
