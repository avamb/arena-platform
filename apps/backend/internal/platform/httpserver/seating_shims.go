// seating_shims.go bridges the *Server god-object to the hseating
// sub-package (feature #304, Wave SEAT-A3). All handler / validation
// logic lives in hseating/; these thin delegating methods preserve the
// unexported *Server method surface so mount_seating.go and any future
// test files compile against the same identifiers as the rest of the
// httpserver package.
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
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

func (s *Server) handleListSeatingPlanVersions(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleListSeatingPlanVersions(w, r)
}

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

// ─── layout.svg shim (feature #314, Wave SEAT-D3) ────────────────────────────

func (s *Server) handleGetPublicSessionLayoutSVG(w http.ResponseWriter, r *http.Request) {
	s.seatingHandler().HandleGetPublicSessionLayoutSVG(w, r)
}

// ─── bind handler shim (feature #306, Wave SEAT-B2) ──────────────────────────

func (s *Server) handleBindSessionSeating(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.seatingHandler().HandleBindSessionSeating(w, r)
}

// ─── seats block/unblock handler shim (feature #308, Wave SEAT-B4) ───────────

func (s *Server) handlePatchSessionSeats(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.seatingHandler().HandlePatchSessionSeats(w, r)
}

// ─── AB-39 per-seat category assignment shim (feature #429) ─────────────────
//
// Flat path: PATCH /v1/event-sessions/{id}/seats/category. Since there is no
// {org_id} URL segment we resolve the session's owning org via
// GetSessionOrgContext and then delegate to the standard membership check
// (server_orgauth.go) before invoking the hseating handler.
func (s *Server) handlePatchSessionSeatsCategory(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	orgCtx, err := s.sessionQueries.GetSessionOrgContext(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		s.logger.Error("seating: category patch org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to resolve session organization", r,
		))
		return
	}
	if !s.enforceMembershipInOrg(w, r, orgCtx.OrgID) {
		return
	}
	s.seatingHandler().HandlePatchSessionSeatsCategory(w, r)
}

// handleListSessionSeatsAdmin serves the admin seat inventory GET (AB-39,
// feature #429). Same auth model as the category PATCH above.
func (s *Server) handleListSessionSeatsAdmin(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	orgCtx, err := s.sessionQueries.GetSessionOrgContext(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		s.logger.Error("seating: admin seats org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.admin_seats_failed", "failed to resolve session organization", r,
		))
		return
	}
	if !s.enforceMembershipInOrg(w, r, orgCtx.OrgID) {
		return
	}
	s.seatingHandler().HandleListSessionSeatsAdmin(w, r)
}
