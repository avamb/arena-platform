// event_materials.go implements the gallery photos and video links of an
// event's page (HP-1, migration 0153, task 1-2a).
//
// Endpoints (the event must belong to the path organization, else 404
// event.not_found; reads need event.read, writes event.update):
//
//	GET    /v1/organizations/{org_id}/events/{event_id}/media-items            — list photos
//	POST   /v1/organizations/{org_id}/events/{event_id}/media-items            — add a photo (media_id of this organization, owner type event_gallery)
//	PUT    /v1/organizations/{org_id}/events/{event_id}/media-items/order      — reorder
//	PATCH  /v1/organizations/{org_id}/events/{event_id}/media-items/{item_id}  — set or clear the caption
//	DELETE /v1/organizations/{org_id}/events/{event_id}/media-items/{item_id}  — remove
//	GET    /v1/organizations/{org_id}/events/{event_id}/videos                 — list links
//	POST   /v1/organizations/{org_id}/events/{event_id}/videos                 — add a link (parsed, never fetched)
//	PUT    /v1/organizations/{org_id}/events/{event_id}/videos/order           — reorder
//	DELETE /v1/organizations/{org_id}/events/{event_id}/videos/{video_id}      — remove
//
// Limits (6 photos, 4 videos) are enforced by the schema (position CHECK plus
// UNIQUE (event_id, position)); a write picks the lowest free position itself
// and answers 409 when none is left. Every write runs under one advisory lock
// per event and kind, so a pick, a delete with its compaction and a reorder
// never interleave. A reorder is ONE UPDATE statement.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	galleryLockKind = "gallery"
	videosLockKind  = "videos"

	// Constraint names of migration 0153 that a 23505 can name.
	galleryMediaConstraint = "event_gallery_items_media_uq"
)

// GalleryItemResponse is the JSON shape of one gallery photo.
type GalleryItemResponse struct {
	ID        string  `json:"id"`
	EventID   string  `json:"event_id"`
	MediaID   string  `json:"media_id"`
	Position  int     `json:"position"`
	Caption   *string `json:"caption"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// GalleryItemFromRow renders an event_gallery_items row.
func GalleryItemFromRow(g gen.EventGalleryItemRow) GalleryItemResponse {
	return GalleryItemResponse{
		ID:        g.ID.String(),
		EventID:   g.EventID.String(),
		MediaID:   g.MediaID.String(),
		Position:  int(g.Position),
		Caption:   g.Caption,
		CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: g.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// VideoResponse is the JSON shape of one video link.
type VideoResponse struct {
	ID        string  `json:"id"`
	EventID   string  `json:"event_id"`
	Provider  string  `json:"provider"`
	VideoID   *string `json:"video_id"`
	URL       string  `json:"url"`
	Position  int     `json:"position"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// VideoFromRow renders an event_videos row.
func VideoFromRow(v gen.EventVideoRow) VideoResponse {
	return VideoResponse{
		ID:        v.ID.String(),
		EventID:   v.EventID.String(),
		Provider:  v.Provider,
		VideoID:   v.VideoID,
		URL:       v.URL,
		Position:  int(v.Position),
		CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: v.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// NormalizeGalleryCaption trims a caption; blank means "no caption" (NULL).
// ok is false when it is longer than eventmaterials.MaxCaptionRunes.
func NormalizeGalleryCaption(raw string) (value *string, ok bool) {
	// A NUL byte cannot be stored in a PostgreSQL text column (22021, a 500).
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\x00", ""))
	if s == "" {
		return nil, true
	}
	if utf8.RuneCountInString(s) > eventmaterials.MaxCaptionRunes {
		return nil, false
	}
	return &s, true
}

// materialsPrep runs the checks every materials route shares: dependencies,
// path ids, membership of the path organization and the event's ownership.
func (h *Handler) materialsPrep(w http.ResponseWriter, r *http.Request, needTx bool) (orgID, eventID uuid.UUID, ok bool) {
	if !h.promoterDepsReady(w, r, needTx) {
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok = httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	eventID, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return uuid.Nil, uuid.Nil, false
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, eventID, true
}

// materialsTx runs fn in a transaction whose first statement is the event's
// materials lock. A nil error commits.
func (h *Handler) materialsTx(ctx context.Context, eventID uuid.UUID, kind string, fn func(q *gen.Queries, tx pgx.Tx) error) error {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return errMaterialsBegin
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.eventQueries.WithTx(tx)
	if err := q.LockEventMaterials(ctx, eventID, kind); err != nil {
		return err
	}
	if err := fn(q, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var errMaterialsBegin = errors.New("event materials: begin transaction failed")

func (h *Handler) writeMaterialsFailure(w http.ResponseWriter, r *http.Request, code, msg string, err error) {
	if errors.Is(err, errMaterialsBegin) {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	h.logger.Error("event materials: "+code, slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(code, msg, r))
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgUniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func writeEventGone(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
}

// parseOrderIDs validates the id list of a reorder body: UUIDs, no repeats.
func parseOrderIDs(raw []string) ([]uuid.UUID, bool) {
	if raw == nil {
		return nil, false
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	ids := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, false
		}
		if _, dup := seen[id]; dup {
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}

// sameIDSet reports whether want and have name exactly the same ids.
func sameIDSet(want []uuid.UUID, have []uuid.UUID) bool {
	if len(want) != len(have) {
		return false
	}
	set := make(map[uuid.UUID]struct{}, len(have))
	for _, id := range have {
		set[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Gallery
// ─────────────────────────────────────────────────────────────────────────────

// HandleListEventGallery serves GET .../events/{event_id}/media-items.
func (h *Handler) HandleListEventGallery(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, false)
	if !ok {
		return
	}
	rows, err := h.eventQueries.ListEventGalleryItems(r.Context(), eventID)
	if err != nil {
		h.writeMaterialsFailure(w, r, "event.gallery_list_failed", "failed to list the gallery", err)
		return
	}
	items := make([]GalleryItemResponse, 0, len(rows))
	for _, g := range rows {
		items = append(items, GalleryItemFromRow(g))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "max_items": eventmaterials.MaxGalleryPhotos})
}

type addGalleryItemRequest struct {
	MediaID string  `json:"media_id"`
	Caption *string `json:"caption"`
}

// HandleAddEventGalleryItem serves POST .../events/{event_id}/media-items.
func (h *Handler) HandleAddEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	var req addGalleryItemRequest
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	mediaID, err := uuid.Parse(strings.TrimSpace(req.MediaID))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_media_id", "media_id must be a UUID", r, map[string]any{"field": "media_id"}))
		return
	}
	var caption *string
	if req.Caption != nil {
		c, ok := NormalizeGalleryCaption(*req.Caption)
		if !ok {
			writeInvalidCaption(w, r)
			return
		}
		caption = c
	}
	// The foreign key proves only that the media object exists: check that it
	// is this organization's, a live gallery photo, before anything is written.
	if !h.requireGalleryMedia(w, r, orgID, mediaID) {
		return
	}

	var item gen.EventGalleryItemRow
	err = h.materialsTx(r.Context(), eventID, galleryLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		row, err := q.InsertEventGalleryItem(r.Context(), orgID, eventID, mediaID, caption, int32(eventmaterials.MaxGalleryPhotos-1))
		if err != nil {
			return err
		}
		item = row
		return h.promoterAudit(r.Context(), tx, r, "v1.event_gallery.add", "event_gallery_item", row.ID.String(), map[string]any{
			"event_id": eventID.String(), "media_id": mediaID.String(), "position": int(row.Position),
		})
	})
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusCreated, GalleryItemFromRow(item))
	case errors.Is(err, pgx.ErrNoRows):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"event.gallery_full", "the gallery already holds the maximum number of photos", r,
			map[string]any{"max_items": eventmaterials.MaxGalleryPhotos}))
	case isUniqueViolation(err, galleryMediaConstraint):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.gallery_duplicate", "this photo is already in the gallery", r))
	case isUniqueViolation(err, ""):
		// Lost a race for a position despite the lock (another writer path).
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.gallery_full", "the gallery changed, reload it and try again", r))
	case isForeignKeyViolation(err):
		writeEventGone(w, r)
	default:
		h.writeMaterialsFailure(w, r, "event.gallery_add_failed", "failed to add the photo", err)
	}
}

func writeInvalidCaption(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_caption", "caption is too long", r,
		map[string]any{"field": "caption", "max_length": eventmaterials.MaxCaptionRunes}))
}

// requireGalleryMedia answers 422 event.invalid_media unless mediaID is a live
// media object of orgID created for the gallery. An unknown id, another
// organization's, a deleted one and one of another owner type all read the
// same, so a foreign id is not confirmed to exist.
func (h *Handler) requireGalleryMedia(w http.ResponseWriter, r *http.Request, orgID, mediaID uuid.UUID) bool {
	m, err := h.eventQueries.GetMediaObjectOwnership(r.Context(), mediaID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.writeMaterialsFailure(w, r, "event.gallery_media_lookup_failed", "failed to look up the photo", err)
		return false
	}
	if err != nil || m.DeletedAt != nil || m.OrgID == nil || *m.OrgID != orgID || m.OwnerType != eventmaterials.OwnerTypeGallery {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_media", "media_id is not a gallery photo of this organization", r,
			map[string]any{"field": "media_id"}))
		return false
	}
	return true
}

// HandleUpdateEventGalleryItem serves PATCH .../media-items/{item_id}: only the
// caption changes. The body must carry "caption": a string sets it, null or a
// blank string clears it.
func (h *Handler) HandleUpdateEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	itemID, ok := httputil.UUIDPathParam(w, r, "item_id")
	if !ok {
		return
	}
	var req struct {
		Caption json.RawMessage `json:"caption"`
	}
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	if req.Caption == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_caption", "caption is required (null clears it)", r, map[string]any{"field": "caption"}))
		return
	}
	var caption *string
	if string(req.Caption) != "null" {
		var s string
		if err := json.Unmarshal(req.Caption, &s); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_caption", "caption must be a string or null", r, map[string]any{"field": "caption"}))
			return
		}
		c, ok := NormalizeGalleryCaption(s)
		if !ok {
			writeInvalidCaption(w, r)
			return
		}
		caption = c
	}

	var item gen.EventGalleryItemRow
	err := h.materialsTx(r.Context(), eventID, galleryLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		row, err := q.UpdateEventGalleryItemCaption(r.Context(), itemID, eventID, caption)
		if err != nil {
			return err
		}
		item = row
		return h.promoterAudit(r.Context(), tx, r, "v1.event_gallery.update", "event_gallery_item", row.ID.String(), map[string]any{
			"event_id": eventID.String(), "caption_set": caption != nil,
		})
	})
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusOK, GalleryItemFromRow(item))
	case errors.Is(err, pgx.ErrNoRows):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.gallery_item_not_found", "gallery photo not found", r))
	default:
		h.writeMaterialsFailure(w, r, "event.gallery_update_failed", "failed to update the photo", err)
	}
}

// HandleDeleteEventGalleryItem serves DELETE .../media-items/{item_id}. The
// remaining photos keep their order and close the gap.
func (h *Handler) HandleDeleteEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	itemID, ok := httputil.UUIDPathParam(w, r, "item_id")
	if !ok {
		return
	}
	err := h.materialsTx(r.Context(), eventID, galleryLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		row, err := q.DeleteEventGalleryItem(r.Context(), itemID, eventID)
		if err != nil {
			return err
		}
		if err := q.CompactEventGalleryPositions(r.Context(), eventID); err != nil {
			return err
		}
		return h.promoterAudit(r.Context(), tx, r, "v1.event_gallery.remove", "event_gallery_item", row.ID.String(), map[string]any{
			"event_id": eventID.String(), "media_id": row.MediaID.String(),
		})
	})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, pgx.ErrNoRows):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.gallery_item_not_found", "gallery photo not found", r))
	default:
		h.writeMaterialsFailure(w, r, "event.gallery_delete_failed", "failed to remove the photo", err)
	}
}

type reorderGalleryRequest struct {
	ItemIDs []string `json:"item_ids"`
}

// HandleReorderEventGallery serves PUT .../media-items/order. item_ids must
// name every photo of the event exactly once; the list is the new order.
func (h *Handler) HandleReorderEventGallery(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	var req reorderGalleryRequest
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	ids, ok := parseOrderIDs(req.ItemIDs)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_order", "item_ids must be a list of distinct UUIDs", r, map[string]any{"field": "item_ids"}))
		return
	}
	var items []gen.EventGalleryItemRow
	mismatch := false
	err := h.materialsTx(r.Context(), eventID, galleryLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		current, err := q.ListEventGalleryItems(r.Context(), eventID)
		if err != nil {
			return err
		}
		have := make([]uuid.UUID, 0, len(current))
		for _, c := range current {
			have = append(have, c.ID)
		}
		if !sameIDSet(ids, have) {
			mismatch = true
			return errReorderMismatch
		}
		n, err := q.ReorderEventGalleryItems(r.Context(), eventID, ids)
		if err != nil {
			return err
		}
		if n != int64(len(ids)) {
			mismatch = true
			return errReorderMismatch
		}
		if items, err = q.ListEventGalleryItems(r.Context(), eventID); err != nil {
			return err
		}
		return h.promoterAudit(r.Context(), tx, r, "v1.event_gallery.reorder", "event", eventID.String(), map[string]any{
			"event_id": eventID.String(), "count": len(ids),
		})
	})
	if mismatch {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.gallery_order_mismatch", "item_ids must list every photo of the event once, reload the gallery and try again", r))
		return
	}
	if err != nil {
		h.writeMaterialsFailure(w, r, "event.gallery_reorder_failed", "failed to reorder the gallery", err)
		return
	}
	out := make([]GalleryItemResponse, 0, len(items))
	for _, g := range items {
		out = append(out, GalleryItemFromRow(g))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "max_items": eventmaterials.MaxGalleryPhotos})
}

var errReorderMismatch = errors.New("event materials: reorder list does not match the stored rows")

// ─────────────────────────────────────────────────────────────────────────────
// Videos
// ─────────────────────────────────────────────────────────────────────────────

// HandleListEventVideos serves GET .../events/{event_id}/videos.
func (h *Handler) HandleListEventVideos(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, false)
	if !ok {
		return
	}
	rows, err := h.eventQueries.ListEventVideos(r.Context(), eventID)
	if err != nil {
		h.writeMaterialsFailure(w, r, "event.videos_list_failed", "failed to list the videos", err)
		return
	}
	items := make([]VideoResponse, 0, len(rows))
	for _, v := range rows {
		items = append(items, VideoFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "max_items": eventmaterials.MaxVideos})
}

type addVideoRequest struct {
	URL string `json:"url"`
}

// HandleAddEventVideo serves POST .../events/{event_id}/videos. Only the link
// and the id parsed out of it are stored; the link is never fetched.
func (h *Handler) HandleAddEventVideo(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	var req addVideoRequest
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	ref, err := eventmaterials.ParseVideoURL(req.URL)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_video_url", "url must be an https link without blanks, up to 500 characters", r,
			map[string]any{"field": "url", "max_length": eventmaterials.MaxURLLength}))
		return
	}
	var videoID *string
	if ref.VideoID != "" {
		v := ref.VideoID
		videoID = &v
	}

	var video gen.EventVideoRow
	err = h.materialsTx(r.Context(), eventID, videosLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		row, err := q.InsertEventVideo(r.Context(), orgID, eventID, ref.Provider, videoID, ref.URL, int32(eventmaterials.MaxVideos-1))
		if err != nil {
			return err
		}
		video = row
		return h.promoterAudit(r.Context(), tx, r, "v1.event_video.add", "event_video", row.ID.String(), map[string]any{
			"event_id": eventID.String(), "provider": row.Provider, "position": int(row.Position),
		})
	})
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusCreated, VideoFromRow(video))
	case errors.Is(err, pgx.ErrNoRows):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"event.videos_full", "the event already holds the maximum number of videos", r,
			map[string]any{"max_items": eventmaterials.MaxVideos}))
	case isUniqueViolation(err, ""):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.videos_full", "the videos changed, reload them and try again", r))
	case isForeignKeyViolation(err):
		writeEventGone(w, r)
	default:
		h.writeMaterialsFailure(w, r, "event.video_add_failed", "failed to add the video", err)
	}
}

// HandleDeleteEventVideo serves DELETE .../videos/{video_id}.
func (h *Handler) HandleDeleteEventVideo(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	videoID, ok := httputil.UUIDPathParam(w, r, "video_id")
	if !ok {
		return
	}
	err := h.materialsTx(r.Context(), eventID, videosLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		row, err := q.DeleteEventVideo(r.Context(), videoID, eventID)
		if err != nil {
			return err
		}
		if err := q.CompactEventVideoPositions(r.Context(), eventID); err != nil {
			return err
		}
		return h.promoterAudit(r.Context(), tx, r, "v1.event_video.remove", "event_video", row.ID.String(), map[string]any{
			"event_id": eventID.String(), "provider": row.Provider,
		})
	})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, pgx.ErrNoRows):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.video_not_found", "video not found", r))
	default:
		h.writeMaterialsFailure(w, r, "event.video_delete_failed", "failed to remove the video", err)
	}
}

type reorderVideosRequest struct {
	VideoIDs []string `json:"video_ids"`
}

// HandleReorderEventVideos serves PUT .../videos/order. video_ids must name
// every video of the event exactly once; the list is the new order.
func (h *Handler) HandleReorderEventVideos(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.materialsPrep(w, r, true)
	if !ok {
		return
	}
	var req reorderVideosRequest
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	ids, ok := parseOrderIDs(req.VideoIDs)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_order", "video_ids must be a list of distinct UUIDs", r, map[string]any{"field": "video_ids"}))
		return
	}
	var videos []gen.EventVideoRow
	mismatch := false
	err := h.materialsTx(r.Context(), eventID, videosLockKind, func(q *gen.Queries, tx pgx.Tx) error {
		current, err := q.ListEventVideos(r.Context(), eventID)
		if err != nil {
			return err
		}
		have := make([]uuid.UUID, 0, len(current))
		for _, c := range current {
			have = append(have, c.ID)
		}
		if !sameIDSet(ids, have) {
			mismatch = true
			return errReorderMismatch
		}
		n, err := q.ReorderEventVideos(r.Context(), eventID, ids)
		if err != nil {
			return err
		}
		if n != int64(len(ids)) {
			mismatch = true
			return errReorderMismatch
		}
		if videos, err = q.ListEventVideos(r.Context(), eventID); err != nil {
			return err
		}
		return h.promoterAudit(r.Context(), tx, r, "v1.event_video.reorder", "event", eventID.String(), map[string]any{
			"event_id": eventID.String(), "count": len(ids),
		})
	})
	if mismatch {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"event.videos_order_mismatch", "video_ids must list every video of the event once, reload the videos and try again", r))
		return
	}
	if err != nil {
		h.writeMaterialsFailure(w, r, "event.videos_reorder_failed", "failed to reorder the videos", err)
		return
	}
	out := make([]VideoResponse, 0, len(videos))
	for _, v := range videos {
		out = append(out, VideoFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "max_items": eventmaterials.MaxVideos})
}
