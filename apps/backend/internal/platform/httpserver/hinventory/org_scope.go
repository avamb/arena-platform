package hinventory

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// requireSessionInOrgEvent closes the SEC-2 hole of the inventory ledger
// routes: they carry {org_id}/{event_id}/{session_id}, the server shim proves
// the caller belongs to {org_id}, and nothing tied the session to that
// organization — so a member of organization B (or a key bound to it holding
// inventory.reserve/release/confirm) read, initialised, reserved, released and
// confirmed organization A's capacity by A's session UUID (a sold-out DoS and an
// oversell). The session must belong to the path event and the event to the path
// organization; anything else, or a session that does not exist, is the route's
// own 404 so existence is not leaked.
func (h *Handler) requireSessionInOrgEvent(w http.ResponseWriter, r *http.Request, orgID, eventID, sessionID uuid.UUID) bool {
	ok, err := h.inventoryQueries.SessionInEventAndOrg(r.Context(), sessionID, eventID, orgID)
	if err != nil {
		h.logger.Error("inventory: session organization lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.session_lookup_failed", "failed to look up the session", r,
		))
		return false
	}
	if !ok {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return false
	}
	return true
}

// pathOrgEventSession reads the three path parameters of the ledger routes and
// applies requireSessionInOrgEvent. It returns the session id.
func (h *Handler) pathOrgEventSession(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, false
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return uuid.Nil, false
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return uuid.Nil, false
	}
	if !h.requireSessionInOrgEvent(w, r, orgID, eventID, sessionID) {
		return uuid.Nil, false
	}
	return sessionID, true
}
