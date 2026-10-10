package htickets

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// requireIssuanceTarget closes the SEC-2 hole of POST
// /v1/organizations/{org_id}/complimentary: the session and the category come
// from the BODY and nothing tied them to {org_id}, so a member of organization B
// issued free tickets on organization A's session — drawing A's capacity and
// category places down and mailing the recipients an invitation. The session must
// belong to the path organization and the category to that session; anything
// else, or nothing, is the route's own 404 so existence is not leaked.
func (h *Handler) requireIssuanceTarget(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID, tierID *uuid.UUID) bool {
	ctx := r.Context()
	row, err := h.complimentaryQueries.GetSessionOrgContext(ctx, sessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("complimentary: session organization lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"complimentary.session_lookup_failed", "failed to look up the session", r,
		))
		return false
	}
	if err != nil || row.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return false
	}
	if tierID == nil {
		return true
	}
	if _, err := h.complimentaryQueries.GetTicketTierByID(ctx, *tierID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket category not found", r))
			return false
		}
		h.logger.Error("complimentary: tier lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"complimentary.tier_lookup_failed", "failed to look up the ticket category", r,
		))
		return false
	}
	return true
}
