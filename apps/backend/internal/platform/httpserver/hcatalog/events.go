// events.go implements the event CRUD API endpoints (feature #125).
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// IsValidEventTransition reports whether the transition from → to is allowed
// by the Event state machine. Exported so httpserver shims and tests can
// reference it without importing the domain layer directly.
func IsValidEventTransition(from, to string) bool {
	return catalogdomain.IsValidEventTransition(from, to)
}

// NegotiateLocale resolves the preferred locale from the request.
func NegotiateLocale(r *http.Request) string {
	return i18n.NegotiateLocale(
		r.Header.Get("Accept-Language"),
		r.URL.Query().Get("lang"),
		"",
		"en",
		[]string{"en", "ru"},
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type eventResponse = EventResponse

// EventResponse is the exported form of eventResponse, for use by the httpserver
// shim layer (events_test.go references eventFromRow from package httpserver via
// a forwarder in catalog_shims.go).
//
// Wave 4 (AB-36/AB-37): the event carries no venue and no own dates.
// first_session_at / last_session_at are the trigger-maintained cache over
// the event's sessions (nil / absent for an event with no sessions), and
// venue_names lists the distinct venues of those sessions (empty for an
// event with no sessions; more than one entry for a tour).
type EventResponse struct {
	ID               string   `json:"id"`
	DisplayNumber    int64    `json:"display_number"`
	OrgID            string   `json:"org_id"`
	Name             string   `json:"name"`
	Description      *string  `json:"description"`
	Status           string   `json:"status"`
	FirstSessionAt   *string  `json:"first_session_at"`
	LastSessionAt    *string  `json:"last_session_at"`
	VenueNames       []string `json:"venue_names"`
	Visibility       string   `json:"visibility"`
	ImageURL         *string  `json:"image_url"`
	PosterMediaID    *string  `json:"poster_media_id"`
	Slug             *string  `json:"slug"`
	ShortDescription *string  `json:"short_description"`
	Genre            *string  `json:"genre"`
	AgeRating        *string  `json:"age_rating"`
	DurationMinutes  *int32   `json:"duration_minutes"`
	TeaserURL        *string  `json:"teaser_url"`
	TrailerURL       *string  `json:"trailer_url"`
	MetaDescription  *string  `json:"meta_description"`
	MetaKeywords     *string  `json:"meta_keywords"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func eventFromRow(e gen.EventRow) eventResponse {
	return EventFromRow(e)
}

// EventFromRow is the exported form of eventFromRow, for use by the httpserver
// shim layer (events_test.go calls eventFromRow from package httpserver via a
// forwarder in catalog_shims.go). VenueNames starts empty; list/get handlers
// hydrate it via ListEventVenueNames.
func EventFromRow(e gen.EventRow) EventResponse {
	resp := eventResponse{
		ID:            e.ID.String(),
		DisplayNumber: e.DisplayNumber,
		OrgID:         e.OrgID.String(),
		Name:          e.Name,
		Description:   e.Description,
		Status:        e.Status,
		VenueNames:    []string{},
		Visibility:    e.Visibility,
		ImageURL:      e.ImageURL,
		CreatedAt:     e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     e.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if e.FirstSessionAt != nil {
		s := e.FirstSessionAt.UTC().Format(time.RFC3339)
		resp.FirstSessionAt = &s
	}
	if e.LastSessionAt != nil {
		s := e.LastSessionAt.UTC().Format(time.RFC3339)
		resp.LastSessionAt = &s
	}
	if e.PosterMediaID != nil {
		v := e.PosterMediaID.String()
		resp.PosterMediaID = &v
	}
	resp.Slug = e.Slug
	resp.ShortDescription = e.ShortDescription
	resp.Genre = e.Genre
	resp.AgeRating = e.AgeRating
	resp.DurationMinutes = e.DurationMinutes
	resp.TeaserURL = e.TeaserURL
	resp.TrailerURL = e.TrailerURL
	resp.MetaDescription = e.MetaDescription
	resp.MetaKeywords = e.MetaKeywords
	return resp
}

// hydrateVenueNames fills VenueNames on the given responses from one
// ListEventVenueNames round trip. Failures are non-fatal — the venue
// column is presentational; the list must not 500 because of it.
func (h *Handler) hydrateVenueNames(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	names, err := h.eventQueries.ListEventVenueNames(ctx, ids)
	if err != nil {
		h.logger.Warn("event: venue-name hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		if id, err := uuid.Parse(responses[i].ID); err == nil {
			if vn, ok := names[id]; ok {
				responses[i].VenueNames = vn
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events
// ─────────────────────────────────────────────────────────────────────────────

// createEventRequest carries the event-own fields only. Dates and venue
// belong to sessions (AB-36/AB-37): event create no longer collects them —
// they are set when sessions are created (step 2 of the event wizard).
type createEventRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Visibility   string `json:"visibility"`
	ImageURL     string `json:"image_url"`
	Translations map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"translations"`
}

func (h *Handler) HandleCreateEvent(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req createEventRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Status = strings.TrimSpace(req.Status)
	req.Visibility = strings.TrimSpace(req.Visibility)
	req.ImageURL = strings.TrimSpace(req.ImageURL)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	if req.Status != "" && req.Status != "draft" && req.Status != "published" &&
		req.Status != "cancelled" && req.Status != "archived" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_status", "status must be one of: draft, published, cancelled, archived", r,
			map[string]any{"field": "status"},
		))
		return
	}

	if req.Visibility != "" && req.Visibility != "public" && req.Visibility != "private" && req.Visibility != "unlisted" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted", r,
			map[string]any{"field": "visibility"},
		))
		return
	}

	var description *string
	if req.Description != "" {
		desc := req.Description
		description = &desc
	}

	var imageURL *string
	if req.ImageURL != "" {
		iu := req.ImageURL
		imageURL = &iu
	}

	e, err := h.eventQueries.InsertEvent(ctx, orgID, req.Name, description, req.Status, req.Visibility, imageURL)
	if err != nil {
		h.logger.Error("event: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.insert_failed", "failed to create event", r,
		))
		return
	}

	eventIDStr := e.ID.String()
	for locale, trans := range req.Translations {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		if name := strings.TrimSpace(trans.Name); name != "" {
			if err := h.eventQueries.UpsertEventI18nName(ctx, eventIDStr, locale, name); err != nil {
				h.logger.Warn("event: upsert i18n name failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
		if desc := strings.TrimSpace(trans.Description); desc != "" {
			if err := h.eventQueries.UpsertEventI18nDescription(ctx, eventIDStr, locale, desc); err != nil {
				h.logger.Warn("event: upsert i18n description failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"event": eventFromRow(e),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := NegotiateLocale(r)

	visibilityFilter := r.URL.Query().Get("visibility")
	if visibilityFilter == "" {
		visibilityFilter = "public"
	} else if visibilityFilter != "public" && visibilityFilter != "private" && visibilityFilter != "unlisted" && visibilityFilter != "all" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted, all", r,
			map[string]any{"field": "visibility"},
		))
		return
	}
	if visibilityFilter == "all" {
		visibilityFilter = ""
	}

	rows, err := h.eventQueries.ListEvents(ctx, locale, visibilityFilter)
	if err != nil {
		h.logger.Error("event: list all failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.list_failed", "failed to list events", r,
		))
		return
	}

	result := make([]eventResponse, 0, len(rows))
	for _, e := range rows {
		result = append(result, eventFromRow(e))
	}
	h.hydrateVenueNames(ctx, result)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"events": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetEvent(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := NegotiateLocale(r)

	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	e, err := h.eventQueries.GetEventByID(ctx, eventID, locale)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.get_failed", "failed to get event", r,
		))
		return
	}

	single := []eventResponse{eventFromRow(e)}
	h.hydrateVenueNames(ctx, single)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event": single[0],
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListEventsByOrg(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := NegotiateLocale(r)

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}

	rows, err := h.eventQueries.ListEventsByOrg(ctx, orgID, locale)
	if err != nil {
		h.logger.Error("event: list by org failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.list_failed", "failed to list events", r,
		))
		return
	}

	result := make([]eventResponse, 0, len(rows))
	for _, e := range rows {
		result = append(result, eventFromRow(e))
	}
	h.hydrateVenueNames(ctx, result)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"events": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateEventRequest carries the event-own fields only; dates and venue
// live on the event's sessions (AB-36/AB-37). poster_media_id sets the
// event-level poster artwork (AB-47); clear_session_overrides=true also
// clears the session-level poster overrides so the event poster becomes
// effective for all sessions.
type updateEventRequest struct {
	Name                  string  `json:"name"`
	Description           *string `json:"description"`
	Visibility            string  `json:"visibility"`
	ImageURL              *string `json:"image_url"`
	PosterMediaID         *string `json:"poster_media_id"`
	ClearSessionOverrides bool    `json:"clear_session_overrides"`
	Translations          map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"translations"`
	// AB-45: content-management metadata fields (migration 0051)
	Slug             *string `json:"slug"`
	ShortDescription *string `json:"short_description"`
	Genre            *string `json:"genre"`
	AgeRating        *string `json:"age_rating"`
	DurationMinutes  *int32  `json:"duration_minutes"`
	TeaserURL        *string `json:"teaser_url"`
	TrailerURL       *string `json:"trailer_url"`
	MetaDescription  *string `json:"meta_description"`
	MetaKeywords     *string `json:"meta_keywords"`
}

func (h *Handler) HandleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req updateEventRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Visibility = strings.TrimSpace(req.Visibility)

	if req.Visibility != "" && req.Visibility != "public" && req.Visibility != "private" && req.Visibility != "unlisted" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted", r,
			map[string]any{"field": "visibility"},
		))
		return
	}

	var description *string
	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		description = &trimmed
	}

	var imageURL *string
	if req.ImageURL != nil {
		trimmed := strings.TrimSpace(*req.ImageURL)
		imageURL = &trimmed
	}

	// poster_media_id (AB-47): optional event-level poster artwork.
	var posterMediaID *uuid.UUID
	if req.PosterMediaID != nil && strings.TrimSpace(*req.PosterMediaID) != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(*req.PosterMediaID))
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_poster_media_id", "poster_media_id must be a valid UUID", r,
				map[string]any{"field": "poster_media_id"},
			))
			return
		}
		posterMediaID = &parsed
	}

	updated, err := h.eventQueries.UpdateEvent(ctx, eventID, orgID, req.Name, description, req.Visibility, imageURL, posterMediaID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.update_failed", "failed to update event", r,
		))
		return
	}

	// clear_session_overrides (AB-47): when requested, null out the session-level
	// poster overrides so the event-level poster becomes effective for all sessions.
	if req.ClearSessionOverrides && h.eventQueries != nil {
		if err := h.eventQueries.ClearSessionPosterOverrides(ctx, eventID); err != nil {
			h.logger.Warn("event: clear session poster overrides failed (non-fatal)",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	eventIDStr := updated.ID.String()
	for locale, trans := range req.Translations {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		if name := strings.TrimSpace(trans.Name); name != "" {
			if err := h.eventQueries.UpsertEventI18nName(ctx, eventIDStr, locale, name); err != nil {
				h.logger.Warn("event: upsert i18n name failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
		if desc := strings.TrimSpace(trans.Description); desc != "" {
			if err := h.eventQueries.UpsertEventI18nDescription(ctx, eventIDStr, locale, desc); err != nil {
				h.logger.Warn("event: upsert i18n description failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	// AB-45: update metadata fields if any were provided
	if req.Slug != nil || req.ShortDescription != nil || req.Genre != nil || req.AgeRating != nil ||
		req.DurationMinutes != nil || req.TeaserURL != nil || req.TrailerURL != nil ||
		req.MetaDescription != nil || req.MetaKeywords != nil {
		meta, metaErr := h.eventQueries.UpdateEventMetadata(ctx, eventID, orgID,
			req.Slug, req.ShortDescription, req.Genre, req.AgeRating,
			req.DurationMinutes, req.TeaserURL, req.TrailerURL, req.MetaDescription, req.MetaKeywords,
		)
		if metaErr != nil {
			h.logger.Error("event: metadata update failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", metaErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"event.metadata_update_failed", "failed to update event metadata", r,
			))
			return
		}
		updated = meta

		if h.audit != nil {
			actor, _ := auth.ActorFromContext(r.Context())
			if auditErr := h.audit.Write(r.Context(), audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    "user",
				ActorID:      actor.ID,
				Action:       "v1.event.update_metadata",
				ResourceType: "event",
				ResourceID:   eventID.String(),
				RequestID:    logging.RequestID(r.Context()),
				TraceID:      logging.TraceID(r.Context()),
				IP:           httputil.ExtractClientIP(r),
				Metadata:     map[string]any{"org_id": orgID.String()},
			}); auditErr != nil {
				h.logger.Warn("event: audit write failed", "error", auditErr.Error())
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{id}/status
// ─────────────────────────────────────────────────────────────────────────────

type updateEventStatusRequest struct {
	Status string `json:"status"`
}

func (h *Handler) HandleUpdateEventStatus(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req updateEventStatusRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.missing_status", "status is required", r,
			map[string]any{"field": "status"},
		))
		return
	}

	if req.Status != "draft" && req.Status != "published" && req.Status != "cancelled" && req.Status != "archived" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_status", "status must be one of: draft, published, cancelled, archived", r,
			map[string]any{"field": "status"},
		))
		return
	}

	current, err := h.eventQueries.GetEventRaw(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: get for status transition failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.get_failed", "failed to get event", r,
		))
		return
	}

	if current.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
		return
	}

	if current.Status == req.Status {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"event": eventFromRow(current),
		})
		return
	}

	if !IsValidEventTransition(current.Status, req.Status) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_transition",
			"status transition from '"+current.Status+"' to '"+req.Status+"' is not allowed",
			r,
			map[string]any{
				"current_status": current.Status,
				"target_status":  req.Status,
			},
		))
		return
	}

	// AB-42 publish gate: an event may only be published once it is sellable.
	// It must have at least one session, and every session must carry at
	// least one ticket tier. Half-finished events stay resumable in draft.
	if req.Status == "published" && h.sessionQueries != nil && h.tierQueries != nil {
		sessions, sErr := h.sessionQueries.ListSessionsByEvent(ctx, eventID)
		if sErr != nil {
			h.logger.Error("event: publish gate list sessions failed", slog.String("error", sErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"event.publish_gate_failed", "failed to inspect sessions for publish gate", r,
			))
			return
		}
		if len(sessions) == 0 {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"event.publish_requires_session",
				"cannot publish an event that has no session",
				r,
				map[string]any{"missing": "session"},
			))
			return
		}
		var untieredSessionIDs []string
		for _, sess := range sessions {
			tiers, tErr := h.tierQueries.ListTicketTiersBySession(ctx, sess.ID)
			if tErr != nil {
				h.logger.Error("event: publish gate list tiers failed", slog.String("error", tErr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"event.publish_gate_failed", "failed to inspect tiers for publish gate", r,
				))
				return
			}
			if len(tiers) == 0 {
				untieredSessionIDs = append(untieredSessionIDs, sess.ID.String())
			}
		}
		if len(untieredSessionIDs) > 0 {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"event.publish_requires_priced_tier",
				"cannot publish: every session must have at least one priced tier",
				r,
				map[string]any{
					"missing":              "priced_tier",
					"untiered_session_ids": untieredSessionIDs,
				},
			))
			return
		}
	}

	updated, err := h.eventQueries.UpdateEventStatus(ctx, eventID, orgID, req.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: update status failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.update_status_failed", "failed to update event status", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
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

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.eventQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteEvent(ctx, eventID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.delete_failed", "failed to delete event", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.event.delete",
			ResourceType: "event",
			ResourceID:   eventID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"event_name": deleted.Name,
				"org_id":     orgID.String(),
				"status":     deleted.Status,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("event: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"event.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event":   eventFromRow(deleted),
		"deleted": true,
	})
}
