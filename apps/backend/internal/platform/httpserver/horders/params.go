package horders

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Query-parameter parsing shared by the list handler.

func parsePagination(w http.ResponseWriter, r *http.Request) (limit, offset int32, ok bool) {
	limit = defaultLimit
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxLimit || n > math.MaxInt32 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"orders.invalid_limit", "limit must be a positive integer up to 200", r))
			return 0, 0, false
		}
		limit = int32(n) // #nosec G109 -- bounded above by maxLimit (200) and math.MaxInt32
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > math.MaxInt32 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"orders.invalid_offset", "offset must be a non-negative integer", r))
			return 0, 0, false
		}
		offset = int32(n) // #nosec G109 -- bounded above by math.MaxInt32
	}
	return limit, offset, true
}

// parseTimeParam parses an RFC3339 query parameter, returning nil (and ok)
// when the parameter is absent so the corresponding SQL filter is skipped.
func parseTimeParam(w http.ResponseWriter, r *http.Request, name string) (*time.Time, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"orders.invalid_"+name, name+" must be an RFC3339 timestamp", r))
		return nil, false
	}
	return &t, true
}

// scopedIDParam reads an optional UUID query parameter that names a row of
// the organization (session_id, event_id). A malformed value is 400; a row
// of another organization — or an unknown one — is 404, exactly like an
// unknown id, so a foreign id cannot be probed through the filter.
func (h *Handler) scopedIDParam(
	w http.ResponseWriter, r *http.Request, orgID uuid.UUID, name string,
	owns func(context.Context, uuid.UUID, uuid.UUID) (bool, error),
) (*uuid.UUID, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("orders.invalid_"+name, name+" must be a valid UUID", r))
		return nil, false
	}
	ok, err := owns(r.Context(), orgID, id)
	if err != nil {
		h.logger.Error("horders: "+name+" lookup failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to resolve "+name, r))
		return nil, false
	}
	if !ok {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("orders."+name+"_not_found", name+" not found in this organization", r))
		return nil, false
	}
	return &id, true
}
