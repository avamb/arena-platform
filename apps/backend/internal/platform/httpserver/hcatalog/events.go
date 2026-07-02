// events.go implements the event CRUD API endpoints (feature #125).
package hcatalog

import (
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
type EventResponse struct {
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

func eventFromRow(e gen.EventRow) eventResponse {
	return EventFromRow(e)
}

// EventFromRow is the exported form of eventFromRow, for use by the httpserver
// shim layer (events_test.go calls eventFromRow from package httpserver via a
// forwarder in catalog_shims.go).
func EventFromRow(e gen.EventRow) EventResponse {
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

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events
// ─────────────────────────────────────────────────────────────────────────────

type createEventRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	VenueID      string `json:"venue_id"`
	Status       string `json:"status"`
	StartAt      string `json:"start_at"`
	EndAt        string `json:"end_at"`
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

	if req.StartAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.missing_start_at", "start_at is required", r,
			map[string]any{"field": "start_at"},
		))
		return
	}
	startAt, parseErr := time.Parse(time.RFC3339, req.StartAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "start_at"},
		))
		return
	}

	if req.EndAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.missing_end_at", "end_at is required", r,
			map[string]any{"field": "end_at"},
		))
		return
	}
	endAt, parseErr := time.Parse(time.RFC3339, req.EndAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	if !endAt.After(startAt) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	var venueID *uuid.UUID
	if req.VenueID != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(req.VenueID))
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_venue_id", "venue_id must be a valid UUID", r,
				map[string]any{"field": "venue_id"},
			))
			return
		}
		venueID = &parsed
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

	e, err := h.eventQueries.InsertEvent(ctx, orgID, venueID, req.Name, description, req.Status, startAt, endAt, req.Visibility, imageURL)
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event": eventFromRow(e),
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
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"events": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{id}
// ─────────────────────────────────────────────────────────────────────────────

type updateEventRequest struct {
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	VenueID      *string `json:"venue_id"`
	StartAt      *string `json:"start_at"`
	EndAt        *string `json:"end_at"`
	Visibility   string  `json:"visibility"`
	ImageURL     *string `json:"image_url"`
	Translations map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"translations"`
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

	var venueID *uuid.UUID
	if req.VenueID != nil {
		trimmed := strings.TrimSpace(*req.VenueID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"event.invalid_venue_id", "venue_id must be a valid UUID", r,
					map[string]any{"field": "venue_id"},
				))
				return
			}
			venueID = &parsed
		}
	}

	var startAt *time.Time
	if req.StartAt != nil {
		trimmed := strings.TrimSpace(*req.StartAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"event.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "start_at"},
				))
				return
			}
			startAt = &t
		}
	}

	var endAt *time.Time
	if req.EndAt != nil {
		trimmed := strings.TrimSpace(*req.EndAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"event.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "end_at"},
				))
				return
			}
			endAt = &t
		}
	}

	if startAt != nil && endAt != nil && !endAt.After(*startAt) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
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

	updated, err := h.eventQueries.UpdateEvent(ctx, eventID, orgID, venueID, req.Name, description, startAt, endAt, req.Visibility, imageURL)
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
