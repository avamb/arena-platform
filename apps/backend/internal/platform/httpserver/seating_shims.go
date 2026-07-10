// seating_shims.go bridges the *Server god-object to the hseating
// sub-package (feature #304, Wave SEAT-A3). All handler / validation
// logic lives in hseating/; these thin delegating methods preserve the
// unexported *Server method surface so mount_seating.go and any future
// test files compile against the same identifiers as the rest of the
// httpserver package.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseating"
)

// seatingHandler constructs a hseating.Handler from the server's dependencies.
func (s *Server) seatingHandler() *hseating.Handler {
	return hseating.New(
		s.seatingQueries,
		s.pool,
		s.audit,
		s.logger,
	)
}

// ─── plan CRUD + fork handler shims ───────────────────────────────────────────

func (s *Server) handleListSeatingPlansByVenue(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleListSeatingPlansByVenue(w, r)
}

func (s *Server) handleCreateSeatingPlan(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleCreateSeatingPlan(w, r)
}

func (s *Server) handleGetSeatingPlan(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleGetSeatingPlan(w, r)
}

func (s *Server) handleUpdateSeatingPlan(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleUpdateSeatingPlan(w, r)
}

func (s *Server) handleForkSeatingPlan(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleForkSeatingPlan(w, r)
}

// ─── version handler shims ────────────────────────────────────────────────────

func (s *Server) handleCreateSeatingPlanVersion(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleCreateSeatingPlanVersion(w, r)
}

func (s *Server) handleGetSeatingPlanVersion(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleGetSeatingPlanVersion(w, r)
}

// ─── public schema + seat-status shims (feature #307, Wave SEAT-B3) ──────────

func (s *Server) handleGetPublicSessionSchema(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleGetPublicSessionSchema(w, r)
}

func (s *Server) handleGetPublicSessionSeatStatus(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleGetPublicSessionSeatStatus(w, r)
}

// ─── bind handler shim (feature #306, Wave SEAT-B2) ──────────────────────────

func (s *Server) handleBindSessionSeating(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleBindSessionSeating(w, r)
}
