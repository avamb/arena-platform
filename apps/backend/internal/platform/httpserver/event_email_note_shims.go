package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// emailNoteTestQuota is the process-wide cap of test letters per organization
// (eventnote.TestEmailsPerHour an hour), kept in memory like the poster-read
// cap: a restart resets it, and with several replicas the ceiling is the
// limit times the replica count.
func (s *Server) emailNoteTestQuota() hcatalog.EmailNoteTestQuota {
	s.emailNoteTestOnce.Do(func() {
		s.emailNoteTestQ = ratelimit.New(ratelimit.Config{
			MaxAttempts: eventnote.TestEmailsPerHour,
			Window:      hcatalog.EmailNoteTestWindow,
		})
	})
	return s.emailNoteTestQ
}

// handleEventEmailNoteTest queues the organizer's sample letter (EC-31).
func (s *Server) handleEventEmailNoteTest(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().WithEmailNoteTestLimiter(s.emailNoteTestQuota()).HandleEmailNoteTest(w, r)
}
