// event_artists.go implements CRUD for the event_artists child table (AB-45).
//
// Endpoints:
//
//	GET    /v1/organizations/{org_id}/events/{id}/artists        — list (event.read)
//	POST   /v1/organizations/{org_id}/events/{id}/artists        — create (event.update)
//	PATCH  /v1/organizations/{org_id}/events/{id}/artists/{aid}  — update (event.update)
//	DELETE /v1/organizations/{org_id}/events/{id}/artists/{aid}  — delete (event.update)
package hcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

type artistResponse struct {
	ID           string  `json:"id"`
	EventID      string  `json:"event_id"`
	Name         string  `json:"name"`
	Role         *string `json:"role"`
	Bio          *string `json:"bio"`
	PhotoMediaID *string `json:"photo_media_id"`
	SortOrder    int32   `json:"sort_order"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func artistFromRow(a gen.EventArtistRow) artistResponse {
	resp := artistResponse{
		ID:        a.ID.String(),
		EventID:   a.EventID.String(),
		Name:      a.Name,
		Role:      a.Role,
		Bio:       a.Bio,
		SortOrder: a.SortOrder,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: a.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if a.PhotoMediaID != nil {
		v := a.PhotoMediaID.String()
		resp.PhotoMediaID = &v
	}
	return resp
}

// HandleListEventArtists serves GET /v1/organizations/{org_id}/events/{id}/artists.
func (h *Handler) HandleListEventArtists(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}
	_ = orgID // validated via org membership

	artists, err := h.eventQueries.ListEventArtists(ctx, eventID)
	if err != nil {
		h.logger.Error("event_artists: list failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event_artists.list_failed", "failed to list event artists", r))
		return
	}
	result := make([]artistResponse, 0, len(artists))
	for _, a := range artists {
		result = append(result, artistFromRow(a))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"artists": result})
}

// createArtistRequest is the request body for POST .../artists.
type createArtistRequest struct {
	Name         string  `json:"name"`
	Role         *string `json:"role"`
	Bio          *string `json:"bio"`
	PhotoMediaID *string `json:"photo_media_id"`
	SortOrder    int32   `json:"sort_order"`
}

// HandleCreateEventArtist serves POST /v1/organizations/{org_id}/events/{id}/artists.
func (h *Handler) HandleCreateEventArtist(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.empty_body", "request body is required", r))
		return
	}
	var req createArtistRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.invalid_json", "invalid JSON", r))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("event_artists.invalid_name", "name is required", r, map[string]any{"field": "name"}))
		return
	}
	var photoMediaID *uuid.UUID
	if req.PhotoMediaID != nil && strings.TrimSpace(*req.PhotoMediaID) != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(*req.PhotoMediaID))
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("event_artists.invalid_photo_media_id", "photo_media_id must be a valid UUID", r, map[string]any{"field": "photo_media_id"}))
			return
		}
		photoMediaID = &parsed
	}
	artist, err := h.eventQueries.InsertEventArtist(ctx, eventID, req.Name, req.Role, req.Bio, photoMediaID, req.SortOrder)
	if err != nil {
		h.logger.Error("event_artists: insert failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event_artists.insert_failed", "failed to create artist", r))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"artist": artistFromRow(artist)})
}

// updateArtistRequest is the request body for PATCH .../artists/{aid}.
type updateArtistRequest struct {
	Name         string  `json:"name"`
	Role         *string `json:"role"`
	Bio          *string `json:"bio"`
	PhotoMediaID *string `json:"photo_media_id"`
	SortOrder    *int32  `json:"sort_order"`
}

// HandleUpdateEventArtist serves PATCH /v1/organizations/{org_id}/events/{id}/artists/{aid}.
func (h *Handler) HandleUpdateEventArtist(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	artistIDStr := chi.URLParam(r, "aid")
	artistID, parseErr := uuid.Parse(artistIDStr)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.invalid_artist_id", "artist id must be a valid UUID", r))
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.empty_body", "request body is required", r))
		return
	}
	var req updateArtistRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.invalid_json", "invalid JSON", r))
		return
	}
	var photoMediaID *uuid.UUID
	if req.PhotoMediaID != nil && strings.TrimSpace(*req.PhotoMediaID) != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(*req.PhotoMediaID))
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("event_artists.invalid_photo_media_id", "photo_media_id must be a valid UUID", r, map[string]any{"field": "photo_media_id"}))
			return
		}
		photoMediaID = &parsed
	}
	artist, err := h.eventQueries.UpdateEventArtist(ctx, artistID, eventID, req.Name, req.Role, req.Bio, photoMediaID, req.SortOrder)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event_artists.not_found", "artist not found", r))
			return
		}
		h.logger.Error("event_artists: update failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event_artists.update_failed", "failed to update artist", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"artist": artistFromRow(artist)})
}

// HandleDeleteEventArtist serves DELETE /v1/organizations/{org_id}/events/{id}/artists/{aid}.
func (h *Handler) HandleDeleteEventArtist(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	artistIDStr := chi.URLParam(r, "aid")
	artistID, parseErr := uuid.Parse(artistIDStr)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event_artists.invalid_artist_id", "artist id must be a valid UUID", r))
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}

	deleted, err := h.eventQueries.SoftDeleteEventArtist(ctx, artistID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event_artists.not_found", "artist not found", r))
			return
		}
		h.logger.Error("event_artists: delete failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event_artists.delete_failed", "failed to delete artist", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"artist": artistFromRow(deleted), "deleted": true})
}

// requireEventInOrg closes the pass-6 review finding: org membership alone
// does not prove the event belongs to that org. Cross-org ids read as
// not-found so existence is not leaked.
func (h *Handler) requireEventInOrg(w http.ResponseWriter, r *http.Request, eventID, orgID uuid.UUID) bool {
	ev, err := h.eventQueries.GetEventByID(r.Context(), eventID, "")
	if err != nil || ev.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"event.not_found", "event not found", r,
		))
		return false
	}
	return true
}
