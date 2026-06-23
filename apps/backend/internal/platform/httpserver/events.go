// events.go implements the event CRUD API endpoints (feature #125).
//
// Events are dated occurrences organized by one organization at an optional
// venue.  They follow a lifecycle: draft → published → cancelled|archived.
// Name and description can be translated via i18n_text entries.
//
// Date invariant: end_at must be strictly after start_at.  Validated both
// in the handler and enforced by a table CHECK constraint.
//
// Status transitions (allowed):
//
//	draft       → published
//	draft       → cancelled
//	published   → cancelled
//	published   → archived
//	cancelled   → archived
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/events        — create an event (event.create)
//	GET    /v1/events                               — list all public events (event.read, shared)
//	GET    /v1/events/{id}                          — get an event by ID (event.read, shared)
//	GET    /v1/organizations/{org_id}/events        — list events for org (event.read)
//	PATCH  /v1/organizations/{org_id}/events/{id}   — update an event (event.update, owner only)
//	POST   /v1/organizations/{org_id}/events/{id}/status — transition status (event.publish)
//	DELETE /v1/organizations/{org_id}/events/{id}   — soft-delete (event.delete, owner only)
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Status transition table
// ─────────────────────────────────────────────────────────────────────────────

// validEventTransitions defines the allowed status transitions for events.
// Only the entries listed here are permitted; all others return 422.
var validEventTransitions = map[string]map[string]bool{
	"draft": {
		"published": true,
		"cancelled": true,
	},
	"published": {
		"cancelled": true,
		"archived":  true,
	},
	"cancelled": {
		"archived": true,
	},
	"archived": {},
}

// isValidEventTransition returns true when the transition from → to is allowed.
func isValidEventTransition(from, to string) bool {
	allowed, ok := validEventTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// eventResponse is the JSON representation of a single event.
type eventResponse struct {
	ID          string  `json:"id"`
	OrgID       string  `json:"org_id"`
	VenueID     *string `json:"venue_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	StartAt     string  `json:"start_at"`
	EndAt       string  `json:"end_at"`
	Visibility  string  `json:"visibility"`
	ImageURL    *string `json:"image_url"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// eventFromRow converts an EventRow to an eventResponse.
func eventFromRow(e gen.EventRow) eventResponse {
	resp := eventResponse{
		ID:          e.ID.String(),
		OrgID:       e.OrgID.String(),
		Name:        e.Name,
		Description: e.Description,
		Status:      e.Status,
		StartAt:     e.StartAt.UTC().Format(time.RFC3339),
		EndAt:       e.EndAt.UTC().Format(time.RFC3339),
		Visibility:  e.Visibility,
		ImageURL:    e.ImageURL,
		CreatedAt:   e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   e.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if e.VenueID != nil {
		s := e.VenueID.String()
		resp.VenueID = &s
	}
	return resp
}

// negotiateLocale resolves the preferred locale from the request using
// Accept-Language header and ?lang= query parameter. Falls back to "en".
func negotiateLocale(r *http.Request) string {
	return i18n.NegotiateLocale(
		r.Header.Get("Accept-Language"),
		r.URL.Query().Get("lang"),
		"",  // preferred user locale (stub for this milestone)
		"en", // default locale
		[]string{"en", "ru"},
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events
// ─────────────────────────────────────────────────────────────────────────────

// createEventRequest is the request body for POST /v1/organizations/{org_id}/events.
type createEventRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	VenueID     string  `json:"venue_id"`
	Status      string  `json:"status"`
	StartAt     string  `json:"start_at"`
	EndAt       string  `json:"end_at"`
	Visibility  string  `json:"visibility"`
	ImageURL    string  `json:"image_url"`
	// i18n translations for name and description (optional).
	// Key: locale code (e.g. "ru"), Value: translated string.
	Translations map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"translations"`
}

// handleCreateEvent serves POST /v1/organizations/{org_id}/events.
// Requires JWT + "event.create" permission.
// The event is owned by the org identified in the path.
func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req createEventRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Status = strings.TrimSpace(req.Status)
	req.Visibility = strings.TrimSpace(req.Visibility)
	req.ImageURL = strings.TrimSpace(req.ImageURL)

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	// Validate status if provided.
	if req.Status != "" && req.Status != "draft" && req.Status != "published" &&
		req.Status != "cancelled" && req.Status != "archived" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_status", "status must be one of: draft, published, cancelled, archived", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Validate visibility if provided.
	if req.Visibility != "" && req.Visibility != "public" && req.Visibility != "private" && req.Visibility != "unlisted" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted", r,
			map[string]any{"field": "visibility"},
		))
		return
	}

	// Parse start_at.
	if req.StartAt == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.missing_start_at", "start_at is required", r,
			map[string]any{"field": "start_at"},
		))
		return
	}
	startAt, parseErr := time.Parse(time.RFC3339, req.StartAt)
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "start_at"},
		))
		return
	}

	// Parse end_at.
	if req.EndAt == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.missing_end_at", "end_at is required", r,
			map[string]any{"field": "end_at"},
		))
		return
	}
	endAt, parseErr := time.Parse(time.RFC3339, req.EndAt)
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Date invariant: end_at must be after start_at.
	if !endAt.After(startAt) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Optional venue_id.
	var venueID *uuid.UUID
	if req.VenueID != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(req.VenueID))
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"event.invalid_venue_id", "venue_id must be a valid UUID", r,
				map[string]any{"field": "venue_id"},
			))
			return
		}
		venueID = &parsed
	}

	// Optional description.
	var description *string
	if req.Description != "" {
		desc := req.Description
		description = &desc
	}

	// Optional image_url.
	var imageURL *string
	if req.ImageURL != "" {
		iu := req.ImageURL
		imageURL = &iu
	}

	e, err := s.eventQueries.InsertEvent(ctx, orgID, venueID, req.Name, description, req.Status, startAt, endAt, req.Visibility, imageURL)
	if err != nil {
		s.logger.Error("event: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.insert_failed", "failed to create event", r,
		))
		return
	}

	// Store i18n translations when provided.
	eventIDStr := e.ID.String()
	for locale, trans := range req.Translations {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		if name := strings.TrimSpace(trans.Name); name != "" {
			if err := s.eventQueries.UpsertEventI18nName(ctx, eventIDStr, locale, name); err != nil {
				s.logger.Warn("event: upsert i18n name failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
		if desc := strings.TrimSpace(trans.Description); desc != "" {
			if err := s.eventQueries.UpsertEventI18nDescription(ctx, eventIDStr, locale, desc); err != nil {
				s.logger.Warn("event: upsert i18n description failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"event": eventFromRow(e),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events
// ─────────────────────────────────────────────────────────────────────────────

// handleListEvents serves GET /v1/events.
// Returns all active events across all organizations (shared read-only).
// Optional ?visibility= filter; defaults to showing only 'public' events.
// Requires JWT + "event.read" permission.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := negotiateLocale(r)

	// Default to 'public' unless caller provides an explicit visibility filter.
	visibilityFilter := r.URL.Query().Get("visibility")
	if visibilityFilter == "" {
		visibilityFilter = "public"
	} else if visibilityFilter != "public" && visibilityFilter != "private" && visibilityFilter != "unlisted" && visibilityFilter != "all" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted, all", r,
			map[string]any{"field": "visibility"},
		))
		return
	}
	if visibilityFilter == "all" {
		visibilityFilter = "" // empty string → no filter in SQL
	}

	rows, err := s.eventQueries.ListEvents(ctx, locale, visibilityFilter)
	if err != nil {
		s.logger.Error("event: list all failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.list_failed", "failed to list events", r,
		))
		return
	}

	result := make([]eventResponse, 0, len(rows))
	for _, e := range rows {
		result = append(result, eventFromRow(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetEvent serves GET /v1/events/{id}.
// Shared read-only — any authenticated user may read any active event.
// Requires JWT + "event.read" permission.
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := negotiateLocale(r)

	eventID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	e, err := s.eventQueries.GetEventByID(ctx, eventID, locale)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
			return
		}
		s.logger.Error("event: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.get_failed", "failed to get event", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(e),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events
// ─────────────────────────────────────────────────────────────────────────────

// handleListEventsByOrg serves GET /v1/organizations/{org_id}/events.
// Returns only the events owned by the specified organization.
// Requires JWT + "event.read" permission.
func (s *Server) handleListEventsByOrg(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	locale := negotiateLocale(r)

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := s.eventQueries.ListEventsByOrg(ctx, orgID, locale)
	if err != nil {
		s.logger.Error("event: list by org failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.list_failed", "failed to list events", r,
		))
		return
	}

	result := make([]eventResponse, 0, len(rows))
	for _, e := range rows {
		result = append(result, eventFromRow(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateEventRequest is the request body for PATCH /v1/organizations/{org_id}/events/{id}.
// All fields are optional; nil/empty values leave the existing value unchanged.
type updateEventRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	VenueID     *string `json:"venue_id"`
	StartAt     *string `json:"start_at"`
	EndAt       *string `json:"end_at"`
	Visibility  string  `json:"visibility"`
	ImageURL    *string `json:"image_url"`
	// i18n translations for name and description (optional).
	Translations map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"translations"`
}

// handleUpdateEvent serves PATCH /v1/organizations/{org_id}/events/{id}.
// Requires JWT + "event.update" permission.
// Owner-gated: org_id in the path must match the event's owning org.
// Does not handle status transitions — use the /status endpoint instead.
func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req updateEventRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Visibility = strings.TrimSpace(req.Visibility)

	// Validate visibility if provided.
	if req.Visibility != "" && req.Visibility != "public" && req.Visibility != "private" && req.Visibility != "unlisted" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_visibility", "visibility must be one of: public, private, unlisted", r,
			map[string]any{"field": "visibility"},
		))
		return
	}

	// Parse optional venue_id.
	var venueID *uuid.UUID
	if req.VenueID != nil {
		trimmed := strings.TrimSpace(*req.VenueID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"event.invalid_venue_id", "venue_id must be a valid UUID", r,
					map[string]any{"field": "venue_id"},
				))
				return
			}
			venueID = &parsed
		}
	}

	// Parse optional start_at.
	var startAt *time.Time
	if req.StartAt != nil {
		trimmed := strings.TrimSpace(*req.StartAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"event.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "start_at"},
				))
				return
			}
			startAt = &t
		}
	}

	// Parse optional end_at.
	var endAt *time.Time
	if req.EndAt != nil {
		trimmed := strings.TrimSpace(*req.EndAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"event.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "end_at"},
				))
				return
			}
			endAt = &t
		}
	}

	// Date invariant: if both are provided, end_at must be after start_at.
	if startAt != nil && endAt != nil && !endAt.After(*startAt) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Trim description if provided.
	var description *string
	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		description = &trimmed
	}

	// Trim image_url if provided.
	var imageURL *string
	if req.ImageURL != nil {
		trimmed := strings.TrimSpace(*req.ImageURL)
		imageURL = &trimmed
	}

	updated, err := s.eventQueries.UpdateEvent(ctx, eventID, orgID, venueID, req.Name, description, startAt, endAt, req.Visibility, imageURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
			return
		}
		s.logger.Error("event: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.update_failed", "failed to update event", r,
		))
		return
	}

	// Store i18n translations when provided.
	eventIDStr := updated.ID.String()
	for locale, trans := range req.Translations {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		if name := strings.TrimSpace(trans.Name); name != "" {
			if err := s.eventQueries.UpsertEventI18nName(ctx, eventIDStr, locale, name); err != nil {
				s.logger.Warn("event: upsert i18n name failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
		if desc := strings.TrimSpace(trans.Description); desc != "" {
			if err := s.eventQueries.UpsertEventI18nDescription(ctx, eventIDStr, locale, desc); err != nil {
				s.logger.Warn("event: upsert i18n description failed",
					slog.String("event_id", eventIDStr),
					slog.String("locale", locale),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{id}/status
// ─────────────────────────────────────────────────────────────────────────────

// updateEventStatusRequest is the request body for the status transition endpoint.
type updateEventStatusRequest struct {
	Status string `json:"status"`
}

// handleUpdateEventStatus serves POST /v1/organizations/{org_id}/events/{id}/status.
// Validates and applies a status transition according to the event lifecycle:
//
//	draft       → published, cancelled
//	published   → cancelled, archived
//	cancelled   → archived
//
// Requires JWT + "event.publish" permission.
// Owner-gated: org_id in the path must match the event's owning org.
func (s *Server) handleUpdateEventStatus(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.empty_body", "request body is required", r))
		return
	}

	var req updateEventStatusRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("event.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.missing_status", "status is required", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Validate target status value.
	if req.Status != "draft" && req.Status != "published" && req.Status != "cancelled" && req.Status != "archived" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"event.invalid_status", "status must be one of: draft, published, cancelled, archived", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Fetch current status (without i18n overhead).
	current, err := s.eventQueries.GetEventRaw(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
			return
		}
		s.logger.Error("event: get for status transition failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.get_failed", "failed to get event", r,
		))
		return
	}

	// Validate that the caller owns this event.
	if current.OrgID != orgID {
		writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
		return
	}

	// Guard: no-op transition is allowed.
	if current.Status == req.Status {
		writeJSON(w, http.StatusOK, map[string]any{
			"event": eventFromRow(current),
		})
		return
	}

	// Validate the transition.
	if !isValidEventTransition(current.Status, req.Status) {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelopeWithDetails(
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

	updated, err := s.eventQueries.UpdateEventStatus(ctx, eventID, orgID, req.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
			return
		}
		s.logger.Error("event: update status failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.update_status_failed", "failed to update event status", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleDeleteEvent serves DELETE /v1/organizations/{org_id}/events/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "event.delete" permission.
// Owner-gated: org_id in the path must match the event's owning org.
func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	if s.eventQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: soft-delete + audit in one atomic write.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.eventQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteEvent(ctx, eventID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("event.not_found", "event not found", r))
			return
		}
		s.logger.Error("event: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.delete_failed", "failed to delete event", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if s.audit != nil {
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
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"event_name": deleted.Name,
				"org_id":     orgID.String(),
				"status":     deleted.Status,
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("event: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"event.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"event.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event":   eventFromRow(deleted),
		"deleted": true,
	})
}
