package hmaterials

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ReportPhoto is the JSON shape of one photo of the photo report.
type ReportPhoto struct {
	ID        string  `json:"id"`
	EventID   string  `json:"event_id"`
	MediaID   string  `json:"media_id"`
	Position  int     `json:"position"`
	Caption   *string `json:"caption"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func reportPhotoJSON(r gen.EventReportPhotoRow) ReportPhoto {
	return ReportPhoto{
		ID: r.ID.String(), EventID: r.EventID.String(), MediaID: r.MediaID.String(),
		Position: int(r.Position), Caption: r.Caption, CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

func reportPhotosJSON(rows []gen.EventReportPhotoRow) []ReportPhoto {
	out := make([]ReportPhoto, 0, len(rows))
	for _, r := range rows {
		out = append(out, reportPhotoJSON(r))
	}
	return out
}

func photoNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"event.photo_not_found", "photo not found", r,
	))
}

func reportFull(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		"event.photo_report_full",
		"a photo report holds up to 30 photos; please remove one before adding another",
		r, map[string]any{"limit": eventmaterials.MaxReportPhotos},
	))
}

func invalidCaption(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_caption", "the caption must be up to 200 characters without line breaks",
		r, map[string]any{"field": "caption"},
	))
}

// HandleListReportPhotos serves GET .../photo-report: the organizer sees every
// photo at any time. Buyers get the date-gated ListEventReportPhotosPublic
// through the public routes of task 1-2c.
func (h *ExtrasHandler) HandleListReportPhotos(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	rows, err := h.queries.ListEventReportPhotos(r.Context(), eventID)
	if err != nil {
		h.failed(w, r, "list report photos", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"photos": reportPhotosJSON(rows)})
}

type createReportPhotoRequest struct {
	MediaID uuid.UUID `json:"media_id"`
	Caption *string   `json:"caption"`
}

// HandleCreateReportPhoto serves POST .../photo-report. The media object must
// be an active event_report object of the path organization: the foreign key
// proves only that it exists, so a foreign or wrongly typed id is checked here.
func (h *ExtrasHandler) HandleCreateReportPhoto(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req createReportPhotoRequest
	if !readBody(w, r, &req) {
		return
	}
	if req.MediaID == uuid.Nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_body", "media_id is required: upload the photo first, then send its id", r, map[string]any{"field": "media_id"},
		))
		return
	}
	var caption *string
	if req.Caption != nil {
		if caption, ok = NormalizeCaption(*req.Caption); !ok {
			invalidCaption(w, r)
			return
		}
	}
	ctx := r.Context()
	good, err := h.queries.IsEventReportMedia(ctx, req.MediaID, orgID)
	if err != nil {
		h.failed(w, r, "check report media", err)
		return
	}
	if !good {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_report_media",
			"this file cannot be used: upload it to your organization as an event_report photo first",
			r, map[string]any{"field": "media_id"},
		))
		return
	}

	for attempt := 0; attempt < insertAttempts; attempt++ {
		var row gen.EventReportPhotoRow
		err := h.inTx(ctx, func(q *gen.Queries, tx pgx.Tx) error {
			var err error
			row, err = q.InsertEventReportPhoto(ctx, orgID, eventID, req.MediaID, caption, eventmaterials.MaxReportPhotos)
			if err != nil {
				return err
			}
			return h.writeAudit(ctx, tx, r, "v1.event.report_photo.add", eventID, map[string]any{
				"photo_id": row.ID.String(), "media_id": req.MediaID.String(), "position": row.Position,
			})
		})
		if err == nil {
			httputil.WriteJSON(w, http.StatusCreated, map[string]any{"photo": reportPhotoJSON(row)})
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			reportFull(w, r)
			return
		}
		if c, isUnique := uniqueViolation(err); isUnique {
			switch c {
			case reportMediaConstraint:
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"event.photo_report_duplicate", "this photo is already in the report", r,
				))
				return
			case reportPositionConstraint:
				continue // another request took the position: pick again
			}
		}
		h.failed(w, r, "add report photo", err)
		return
	}
	// Every attempt lost its position to a faster request. If those requests
	// filled the list, say so; otherwise ask the client to try again.
	if rows, err := h.queries.ListEventReportPhotos(ctx, eventID); err == nil && len(rows) >= eventmaterials.MaxReportPhotos {
		reportFull(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		"event.concurrent_update", "the list was changed at the same moment; please try again", r,
	))
}

type patchReportPhotoRequest struct {
	// Caption is tri-state: absent keeps it, null or "" clears it.
	Caption json.RawMessage `json:"caption"`
}

// HandlePatchReportPhoto serves PATCH .../photo-report/{photo_id}.
func (h *ExtrasHandler) HandlePatchReportPhoto(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	photoID, ok := pathID(w, r, "photo_id", "event.photo_not_found", "photo")
	if !ok {
		return
	}
	var req patchReportPhotoRequest
	if !readBody(w, r, &req) {
		return
	}
	if len(req.Caption) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"event.invalid_body", "send caption (a text, or null to remove it)", r,
		))
		return
	}
	var caption *string
	if string(req.Caption) != "null" {
		var raw string
		if err := json.Unmarshal(req.Caption, &raw); err != nil {
			invalidCaption(w, r)
			return
		}
		if caption, ok = NormalizeCaption(raw); !ok {
			invalidCaption(w, r)
			return
		}
	}
	ctx := r.Context()
	var row gen.EventReportPhotoRow
	err := h.inTx(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		var err error
		if row, err = q.UpdateEventReportPhotoCaption(ctx, photoID, eventID, caption); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.report_photo.update", eventID, map[string]any{"photo_id": photoID.String()})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		photoNotFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "update report photo", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"photo": reportPhotoJSON(row)})
}

// HandleDeleteReportPhoto serves DELETE .../photo-report/{photo_id}; the
// photos behind it move up so positions stay 0..n-1. The media object stays
// (the media sweep owns the bytes).
func (h *ExtrasHandler) HandleDeleteReportPhoto(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	photoID, ok := pathID(w, r, "photo_id", "event.photo_not_found", "photo")
	if !ok {
		return
	}
	ctx := r.Context()
	err := h.inTx(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		removed, err := q.DeleteEventReportPhoto(ctx, photoID, eventID)
		if err != nil {
			return err
		}
		if !removed {
			return pgx.ErrNoRows
		}
		if err := q.CompactEventReportPhotos(ctx, eventID); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.report_photo.delete", eventID, map[string]any{"photo_id": photoID.String()})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		photoNotFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "delete report photo", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderReportPhotos serves PUT .../photo-report/order. ids must be
// exactly the report's current photo ids in the wanted order.
func (h *ExtrasHandler) HandleReorderReportPhotos(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req orderRequest
	if !readBody(w, r, &req) {
		return
	}
	ctx := r.Context()
	var rows []gen.EventReportPhotoRow
	err := h.inTx(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		cur, err := q.ListEventReportPhotos(ctx, eventID)
		if err != nil {
			return err
		}
		have := make([]uuid.UUID, 0, len(cur))
		for _, c := range cur {
			have = append(have, c.ID)
		}
		if !idsMatch(have, req.IDs) {
			return errInvalidOrder
		}
		if len(req.IDs) > 0 {
			if _, err := q.ReorderEventReportPhotos(ctx, eventID, req.IDs); err != nil {
				return err
			}
		}
		if rows, err = q.ListEventReportPhotos(ctx, eventID); err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.report_photo.reorder", eventID, map[string]any{"count": len(req.IDs)})
	})
	if errors.Is(err, errInvalidOrder) {
		invalidOrder(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "reorder report photos", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"photos": reportPhotosJSON(rows)})
}
