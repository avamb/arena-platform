package hcatalog

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// requireSessionInOrg closes the same hole requireEventInOrg closed for
// events: the tier routes carry {org_id} in the path and requireOrgMembership
// proves the caller belongs to THAT organization, but nothing tied the
// {session_id} of the path to it, so a member of organization B (or a key bound
// to it holding tier.read/tier.update) read, repriced, closed, resized and
// deleted organization A's categories by their UUIDs (found while building the
// bot's category screens, EC-15). A session of another organization — or one
// that does not exist — reads as not found, so existence is not leaked.
func (h *Handler) requireSessionInOrg(w http.ResponseWriter, r *http.Request, sessionID, orgID uuid.UUID) bool {
	row, err := h.tierQueries.GetSessionOrgContext(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return false
		}
		h.logger.Error("tier: session organization lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.session_lookup_failed", "failed to look up the session", r))
		return false
	}
	if row.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return false
	}
	return true
}
