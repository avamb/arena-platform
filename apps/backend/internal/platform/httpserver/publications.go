// publications.go implements the event publication API endpoints (feature #151).
//
// Event publications are the join between events and agent feed tokens:
// "publish event E to feed F" means external consumers of feed F will see
// event E.  This mirrors the legacy Bil24 Subscriptions panel.
//
// Design decisions:
//   - POST is idempotent (ON CONFLICT DO NOTHING in the DB): re-publishing the
//     same event to the same feed returns 200 with the existing row.
//   - DELETE is idempotent: unpublishing a non-existent entry returns 204 (no
//     error — the desired state is already achieved).
//   - city_id in the POST body is optional; omitting it means the publication
//     is visible in all geographies.
//   - Permissions: publication.create, publication.read, publication.delete.
//
// Endpoints:
//
//	POST   /v1/events/{event_id}/publications                         — publish (publication.create)
//	DELETE /v1/events/{event_id}/publications/{feed_token_id}         — unpublish (publication.delete)
//	GET    /v1/events/{event_id}/publications                         — list (publication.read)
package httpserver

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// publicationResponse is the JSON representation of a single event publication.
type publicationResponse struct {
	ID          string  `json:"id"`
	EventID     string  `json:"event_id"`
	FeedTokenID string  `json:"feed_token_id"`
	CityID      *string `json:"city_id"`
	PublishedAt string  `json:"published_at"`
}

// publicationFromRow converts an EventPublicationRow to a publicationResponse.
func publicationFromRow(ep gen.EventPublicationRow) publicationResponse {
	resp := publicationResponse{
		ID:          ep.ID.String(),
		EventID:     ep.EventID.String(),
		FeedTokenID: ep.FeedTokenID.String(),
		PublishedAt: ep.PublishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if ep.CityID != nil {
		s := ep.CityID.String()
		resp.CityID = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/events/{event_id}/publications
// ─────────────────────────────────────────────────────────────────────────────

// handlePublishEvent publishes an event to an agent feed token.
// Body: {"feed_token_id": "<uuid>", "city_id": "<uuid|null>"}
// Idempotent: re-publishing the same pair returns 200 with the existing row.
func (s *Server) handlePublishEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse and validate event_id from path.
	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	// Require Content-Type: application/json.
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		msg := i18n.Localize(ctx, "error.content_type", "Content-Type must be application/json", nil)
		writeJSON(w, http.StatusUnsupportedMediaType, errorEnvelope("publication.content_type_required", msg, r))
		return
	}

	// Parse request body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		msg := i18n.Localize(ctx, "error.body_required", "request body is required", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.body_required", msg, r))
		return
	}

	var req struct {
		FeedTokenID string  `json:"feed_token_id"`
		CityID      *string `json:"city_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		msg := i18n.Localize(ctx, "error.invalid_json", "invalid JSON body", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_json", msg, r))
		return
	}

	// Validate feed_token_id.
	if req.FeedTokenID == "" {
		msg := i18n.Localize(ctx, "error.missing_field", "feed_token_id is required", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.feed_token_id_required", msg, r))
		return
	}
	feedTokenID, err := uuid.Parse(req.FeedTokenID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid feed_token_id: must be a UUID", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_feed_token_id", msg, r))
		return
	}

	// Parse optional city_id.
	var cityID *uuid.UUID
	if req.CityID != nil && *req.CityID != "" {
		parsed, err := uuid.Parse(*req.CityID)
		if err != nil {
			msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid city_id: must be a UUID", nil)
			writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_city_id", msg, r))
			return
		}
		cityID = &parsed
	}

	// Insert (or return existing) publication row.
	pub, err := s.publicationQueries.PublishEvent(ctx, eventID, feedTokenID, cityID)
	if err != nil {
		s.logger.Error("handlePublishEvent: PublishEvent failed",
			"event_id", eventID, "feed_token_id", feedTokenID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("publication.internal", msg, r))
		return
	}

	writeJSON(w, http.StatusOK, publicationFromRow(pub))
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/events/{event_id}/publications/{feed_token_id}
// ─────────────────────────────────────────────────────────────────────────────

// handleUnpublishEvent removes a publication entry (unpublishes an event from a feed).
// Idempotent: deleting a non-existent entry returns 204 without error.
func (s *Server) handleUnpublishEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse event_id from path.
	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	// Parse feed_token_id from path.
	rawFeedTokenID := chi.URLParam(r, "feed_token_id")
	feedTokenID, err := uuid.Parse(rawFeedTokenID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid feed_token_id: must be a UUID", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_feed_token_id", msg, r))
		return
	}

	// Delete the publication entry (idempotent — no error if already absent).
	if err := s.publicationQueries.UnpublishEvent(ctx, eventID, feedTokenID); err != nil {
		s.logger.Error("handleUnpublishEvent: UnpublishEvent failed",
			"event_id", eventID, "feed_token_id", feedTokenID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("publication.internal", msg, r))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events/{event_id}/publications
// ─────────────────────────────────────────────────────────────────────────────

// handleListPublications returns all feed tokens an event is currently published to.
func (s *Server) handleListPublications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse event_id from path.
	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		writeJSON(w, http.StatusBadRequest, errorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	pubs, err := s.publicationQueries.ListPublicationsByEvent(ctx, eventID)
	if err != nil && err != pgx.ErrNoRows {
		s.logger.Error("handleListPublications: ListPublicationsByEvent failed",
			"event_id", eventID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("publication.internal", msg, r))
		return
	}

	resp := make([]publicationResponse, 0, len(pubs))
	for _, ep := range pubs {
		resp = append(resp, publicationFromRow(ep))
	}
	writeJSON(w, http.StatusOK, map[string]any{"publications": resp})
}
