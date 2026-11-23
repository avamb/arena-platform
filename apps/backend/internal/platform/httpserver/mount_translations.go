package httpserver

// mount_translations.go - spec 63 section 16.4: the organizer API of an
// event's content translations (htranslations). Reads need event.read, writes
// event.update; every route also needs membership of {org_id} and ties the
// event to it inside the handler.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htranslations"
)

func (s *Server) mountTranslationRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then, and the
	// route guards see the routes.
	if !s.authEnabled() {
		return
	}
	machine := s.cfg != nil && s.cfg.TranslateLLMAPIKey != ""
	h := htranslations.New(s.pgxPool, s.audit, machine, s.logger)
	const ev = "/organizations/{org_id}/events/{event_id}"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.read", "events")
		pr.Get(ev+"/translations", s.orgGated(h.HandleList))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.update", "events")
		pr.Put(ev+"/translations/{locale}", s.orgGated(h.HandlePut))
		pr.Delete(ev+"/translations/{locale}", s.orgGated(h.HandleDelete))
		pr.Post(ev+"/translations/machine", s.orgGated(h.HandleMachine))
		pr.Put(ev+"/report-text", s.orgGated(h.HandlePutReportText))
	})
}
