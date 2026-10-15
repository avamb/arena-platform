package hmaterials

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// MapPoint is the JSON shape of an event's map point.
type MapPoint struct {
	EventID   string  `json:"event_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     *string `json:"label"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func mapPointJSON(r gen.EventMapPointRow) MapPoint {
	return MapPoint{
		EventID: r.EventID.String(), Latitude: r.Latitude, Longitude: r.Longitude, Label: r.Label,
		CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

// HandleGetMapPoint serves GET .../map-point and answers the bare point. An
// event without a point answers 404 event.map_point_not_found, like DELETE.
func (h *ExtrasHandler) HandleGetMapPoint(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	row, err := h.queries.GetEventMapPoint(r.Context(), eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		mapPointNotFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "read map point", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapPointJSON(row))
}

func mapPointNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"event.map_point_not_found", "this event has no map point", r,
	))
}

type putMapPointRequest struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Label     *string  `json:"label"`
}

// HandlePutMapPoint serves PUT .../map-point: it creates the point or
// replaces it as a whole (an absent label clears the label).
func (h *ExtrasHandler) HandlePutMapPoint(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req putMapPointRequest
	if !readBody(w, r, &req) {
		return
	}
	if req.Latitude == nil || req.Longitude == nil || !ValidMapPoint(*req.Latitude, *req.Longitude) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_map_point",
			"latitude must be between -90 and 90 and longitude between -180 and 180, for example 50.0755 and 14.4378",
			r, map[string]any{"fields": []string{"latitude", "longitude"}},
		))
		return
	}
	var label *string
	if req.Label != nil {
		if label, ok = NormalizeMapLabel(*req.Label); !ok {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_map_point", "the label must be up to 200 characters without line breaks",
				r, map[string]any{"field": "label"},
			))
			return
		}
	}
	ctx := r.Context()
	var row gen.EventMapPointRow
	err := h.inTx(ctx, eventID, mapPointLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		var err error
		row, err = q.UpsertEventMapPoint(ctx, eventID, orgID, coordString(*req.Latitude), coordString(*req.Longitude), label)
		if err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.map_point.set", eventID, nil)
	})
	if err != nil {
		h.failed(w, r, "save map point", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapPointJSON(row))
}

// HandleDeleteMapPoint serves DELETE .../map-point.
func (h *ExtrasHandler) HandleDeleteMapPoint(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := h.inTx(ctx, eventID, mapPointLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		removed, err := q.DeleteEventMapPoint(ctx, eventID)
		if err != nil {
			return err
		}
		if !removed {
			return pgx.ErrNoRows
		}
		return h.writeAudit(ctx, tx, r, "v1.event.map_point.delete", eventID, nil)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		mapPointNotFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "delete map point", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
