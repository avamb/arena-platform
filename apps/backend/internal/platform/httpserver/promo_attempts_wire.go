package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpromoattempts"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

// promoAttemptRecorder is the one journal writer of the process. It is nil
// (a valid no-op) when the server has no raw pool, so a degraded composition
// simply keeps no journal.
func (s *Server) promoAttemptRecorder() *promoattempts.Recorder {
	s.promoAttemptsOnce.Do(func() {
		if s.pgxPool == nil {
			return
		}
		salt := ""
		if s.cfg != nil {
			salt = s.cfg.JWTSecretStub
		}
		s.promoAttempts = promoattempts.New(s.pgxPool, salt, s.logger)
	})
	return s.promoAttempts
}

func (s *Server) promoAttemptsHandler() *hpromoattempts.Handler {
	return hpromoattempts.New(s.pgxPool, s.promoQueries, s.logger)
}

func (s *Server) handlePromoAttemptsForCode(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.promoAttemptsHandler().HandleListForCode(w, r)
}

func (s *Server) handlePromoAttemptsSummary(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.promoAttemptsHandler().HandleSummary(w, r)
}

func (s *Server) handlePromoAttemptsForOrg(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.promoAttemptsHandler().HandleListForOrg(w, r)
}
