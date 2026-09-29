package httpserver

// sample_ticket_shims.go bridges the *Server to hsample: the sample e-ticket
// of a session (migration 0119), rendered for the organizer with the same
// media store the API signs artwork URLs with.

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/mediaresolver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsample"
)

func (s *Server) sampleTicketHandler() *hsample.Handler {
	var media delivery.MediaResolver
	if s.media != nil {
		media = mediaresolver.New(s.media, s.apiPublicURL(), s.logger)
	}
	return hsample.New(s.sessionQueries, media, s.logger)
}

func (s *Server) handleSessionSampleTicket(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.sampleTicketHandler().HandleSessionSampleTicket(w, r)
}
