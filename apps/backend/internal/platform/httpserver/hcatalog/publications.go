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
package hcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// PostgreSQL foreign_key_violation SQLSTATE. See publications.go docstring
// (AB-43): the row rejects with 23503 when either agent_feed_tokens.id or
// cities.id is missing; both used to surface as a generic 500.
const pgForeignKeyViolation = "23503"

// ClassifyPublicationFKError inspects err for a 23503 PostgreSQL
// foreign-key violation on the event_publications table and returns the
// caller-facing HTTP status, error code and message tuple that should be
// surfaced (AB-43). It returns ok=false when err is not a recognised FK
// violation — callers should then fall back to their generic 5xx branch.
func ClassifyPublicationFKError(err error) (status int, code, msg string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgForeignKeyViolation {
		return 0, "", "", false
	}
	switch pgErr.ConstraintName {
	case "event_publications_feed_token_id_fkey":
		return http.StatusNotFound,
			"publication.feed_token_not_found",
			"feed token not found: create the token on the sales channel first",
			true
	case "event_publications_city_id_fkey":
		return http.StatusNotFound,
			"publication.city_not_found",
			"city not found in geo registry",
			true
	case "event_publications_event_id_fkey":
		return http.StatusNotFound,
			"publication.event_not_found",
			"event not found",
			true
	}
	return 0, "", "", false
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// PublicationResponse is the exported JSON representation of a single event
// publication, for use by the httpserver shim layer (publications_test.go
// references publicationResponse from package httpserver via catalog_shims.go).
type PublicationResponse struct {
	ID          string  `json:"id"`
	EventID     string  `json:"event_id"`
	FeedTokenID string  `json:"feed_token_id"`
	CityID      *string `json:"city_id"`
	PublishedAt string  `json:"published_at"`
}

// publicationResponse is a package-level alias for PublicationResponse so
// existing handler code continues to compile unchanged.
type publicationResponse = PublicationResponse

// PublicationFromRow converts a gen.EventPublicationRow to PublicationResponse.
// publicationFromRow is the package-internal alias for backward compatibility.
func PublicationFromRow(ep gen.EventPublicationRow) PublicationResponse {
	resp := PublicationResponse{
		ID:          ep.ID.String(),
		EventID:     ep.EventID.String(),
		FeedTokenID: ep.FeedTokenID.String(),
		PublishedAt: ep.PublishedAt.UTC().Format(time.RFC3339),
	}
	if ep.CityID != nil {
		s := ep.CityID.String()
		resp.CityID = &s
	}
	return resp
}

func publicationFromRow(ep gen.EventPublicationRow) publicationResponse {
	return PublicationFromRow(ep)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/events/{event_id}/publications
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandlePublishEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		msg := i18n.Localize(ctx, "error.content_type", "Content-Type must be application/json", nil)
		httputil.WriteJSON(w, http.StatusUnsupportedMediaType, httputil.ErrorEnvelope("publication.content_type_required", msg, r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		msg := i18n.Localize(ctx, "error.body_required", "request body is required", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.body_required", msg, r))
		return
	}

	var req struct {
		FeedTokenID string  `json:"feed_token_id"`
		CityID      *string `json:"city_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		msg := i18n.Localize(ctx, "error.invalid_json", "invalid JSON body", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_json", msg, r))
		return
	}

	if req.FeedTokenID == "" {
		msg := i18n.Localize(ctx, "error.missing_field", "feed_token_id is required", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.feed_token_id_required", msg, r))
		return
	}
	feedTokenID, err := uuid.Parse(req.FeedTokenID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid feed_token_id: must be a UUID", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_feed_token_id", msg, r))
		return
	}

	var cityID *uuid.UUID
	if req.CityID != nil && *req.CityID != "" {
		parsed, err := uuid.Parse(*req.CityID)
		if err != nil {
			msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid city_id: must be a UUID", nil)
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_city_id", msg, r))
			return
		}
		cityID = &parsed
	}

	pub, err := h.publicationQueries.PublishEvent(ctx, eventID, feedTokenID, cityID)
	if err != nil {
		// AB-43: map the specific FK violation (23503) to an actionable 404
		// instead of collapsing every DB error into publication.internal.
		if status, code, defaultMsg, ok := ClassifyPublicationFKError(err); ok {
			msg := i18n.Localize(ctx, code, defaultMsg, nil)
			httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
			return
		}
		h.logger.Error("handlePublishEvent: PublishEvent failed",
			"event_id", eventID, "feed_token_id", feedTokenID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("publication.internal", msg, r))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, publicationFromRow(pub))
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/events/{event_id}/publications/{feed_token_id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleUnpublishEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	rawFeedTokenID := chi.URLParam(r, "feed_token_id")
	feedTokenID, err := uuid.Parse(rawFeedTokenID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid feed_token_id: must be a UUID", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_feed_token_id", msg, r))
		return
	}

	if err := h.publicationQueries.UnpublishEvent(ctx, eventID, feedTokenID); err != nil {
		h.logger.Error("handleUnpublishEvent: UnpublishEvent failed",
			"event_id", eventID, "feed_token_id", feedTokenID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("publication.internal", msg, r))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events/{event_id}/publications
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListPublications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawEventID := chi.URLParam(r, "event_id")
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		msg := i18n.Localize(ctx, "error.invalid_uuid", "invalid event_id: must be a UUID", nil)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("publication.invalid_event_id", msg, r))
		return
	}

	pubs, err := h.publicationQueries.ListPublicationsByEvent(ctx, eventID)
	if err != nil && err != pgx.ErrNoRows {
		h.logger.Error("handleListPublications: ListPublicationsByEvent failed",
			"event_id", eventID, "err", err)
		msg := i18n.Localize(ctx, "error.internal", "internal server error", nil)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("publication.internal", msg, r))
		return
	}

	resp := make([]publicationResponse, 0, len(pubs))
	for _, ep := range pubs {
		resp = append(resp, publicationFromRow(ep))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"publications": resp})
}
