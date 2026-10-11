package hcatalog

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// posterMediaInOrg refuses a poster that is not usable by orgID. A media
// object is usable when it is the organization's own or carries no
// organization at all (uploads made before media were org-scoped). An object
// of another organization, or one that does not exist, answers 422 with the
// caller's route code, so the id cannot be used to probe other tenants.
func (h *Handler) posterMediaInOrg(w http.ResponseWriter, r *http.Request, code string, mediaID, orgID uuid.UUID) bool {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	var owner *uuid.UUID
	err := h.sessionQueries.DB().QueryRow(r.Context(),
		`SELECT org_id FROM media_objects WHERE id = $1 AND deleted_at IS NULL`, mediaID).Scan(&owner)
	if err == nil && owner != nil && *owner != orgID && !auth.HasSuperadminOrgAccess(r.Context()) {
		err = pgx.ErrNoRows
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				code, "poster_media_id does not name a media object of this organization", r,
				map[string]any{"field": "poster_media_id"},
			))
			return false
		}
		h.logger.Error("catalog: poster media lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"catalog.poster_lookup_failed", "failed to verify poster media", r,
		))
		return false
	}
	return true
}
