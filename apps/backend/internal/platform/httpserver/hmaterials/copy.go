package hmaterials

// copy.go — task 1-2c: copy the page materials of one event to another event
// of the same organization.
//
//	POST /v1/organizations/{org_id}/events/{event_id}/materials/copy-from/{source_event_id}
//
// There is no server-side "duplicate an event": a copy is made by the client
// (read the source with GET .../event-bundle, import the bundles as a new
// event). The import does not carry the materials, so this route moves them:
// the gallery, the video links, the partner links and the map point of the
// SOURCE event land on the TARGET event. The photo report is NOT copied (it
// is about a show that already happened).
//
// Both events must be live events of the path organization (a foreign,
// unknown or deleted one is the route's own 404 event.not_found). The target
// must hold none of the four lists (409 event.materials_not_empty), which is
// also what makes a repeated call harmless: the second one finds the first
// one's rows and refuses. Gallery rows keep the SAME media_id as the source:
// nothing ties a media object to one event (media_objects.owner_id is never
// set to an event and a gallery row's removal never deletes the object), so
// sharing it is safe. Everything runs in one transaction under the target's
// four materials locks and writes one audit event.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Lock kinds shared with the gallery and video routes of task 1-2a (the same
// key scheme: event id + ':' + kind).
const (
	galleryLockKind = "gallery"
	videosLockKind  = "videos"
)

// CopyMaterialsResponse is the 201 body: how many rows of each list were copied.
type CopyMaterialsResponse struct {
	EventID       string `json:"event_id"`
	SourceEventID string `json:"source_event_id"`
	Gallery       int    `json:"gallery"`
	Videos        int    `json:"videos"`
	PartnerLinks  int    `json:"partner_links"`
	MapPoint      bool   `json:"map_point"`
}

var errMaterialsNotEmpty = errors.New("target event already has materials")

// HandleCopyMaterials serves POST .../materials/copy-from/{source_event_id}.
func (h *ExtrasHandler) HandleCopyMaterials(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	sourceID, ok := pathID(w, r, "source_event_id", "event.not_found", "event")
	if !ok {
		return
	}
	ctx := r.Context()
	if sourceID == eventID {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.materials_copy_same_event",
			"the source event must be a different event than the one that receives the materials",
			r, map[string]any{"field": "source_event_id"},
		))
		return
	}
	owner, err := h.queries.GetEventOrgForMaterials(ctx, sourceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.failed(w, r, "copy materials: source lookup", err)
		return
	}
	if err != nil || owner != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
		return
	}

	var res CopyMaterialsResponse
	err = func() error {
		tx, err := h.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := h.queries.WithTx(tx)
		// The target's four locks, in one fixed order, before anything is read.
		for _, kind := range []string{galleryLockKind, videosLockKind, partnerLinksLockKind, mapPointLockKind} {
			if err := q.LockEventExtras(ctx, eventID, kind); err != nil {
				return err
			}
		}
		have, err := q.CountEventMaterials(ctx, eventID)
		if err != nil {
			return err
		}
		if have.Gallery+have.Videos+have.PartnerLinks+have.MapPoints > 0 {
			return errMaterialsNotEmpty
		}
		res = CopyMaterialsResponse{EventID: eventID.String(), SourceEventID: sourceID.String()}
		n, err := q.CopyEventGalleryItems(ctx, orgID, eventID, sourceID)
		if err != nil {
			return err
		}
		res.Gallery = int(n)
		if n, err = q.CopyEventVideos(ctx, orgID, eventID, sourceID); err != nil {
			return err
		}
		res.Videos = int(n)
		if n, err = q.CopyEventPartnerLinks(ctx, orgID, eventID, sourceID); err != nil {
			return err
		}
		res.PartnerLinks = int(n)
		if n, err = q.CopyEventMapPoint(ctx, orgID, eventID, sourceID); err != nil {
			return err
		}
		res.MapPoint = n > 0
		if err := h.writeAudit(ctx, tx, r, "v1.event.materials.copy", eventID, map[string]any{
			"source_event_id": sourceID.String(),
			"gallery":         res.Gallery,
			"videos":          res.Videos,
			"partner_links":   res.PartnerLinks,
			"map_point":       res.MapPoint,
		}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusCreated, res)
	case errors.Is(err, errMaterialsNotEmpty):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.materials_not_empty",
			"this event already has gallery photos, videos, partner links or a map point; remove them first or copy into an event without materials",
			r,
		))
	default:
		if _, isUnique := uniqueViolation(err); isUnique {
			// A writer outside these locks added a row meanwhile.
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"event.materials_not_empty", "the event's materials changed while copying, reload and try again", r,
			))
			return
		}
		h.failed(w, r, "copy materials", err)
	}
}
