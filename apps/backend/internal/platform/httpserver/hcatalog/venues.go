// venues.go implements the venue CRUD API endpoints (feature #124).
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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type venueResponse struct {
	ID              string  `json:"id"`
	OrgID           string  `json:"org_id"`
	CityID          *string `json:"city_id"`
	Name            string  `json:"name"`
	Address         *string `json:"address"`
	CapacityDefault *int32  `json:"capacity_default"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// VenueResponse is the exported alias of venueResponse for use by the httpserver
// shim layer (venues_test.go in package httpserver references venueFromRow via
// catalog_shims.go and reads response fields directly).
type VenueResponse = venueResponse

// VenueFromRow is the exported alias of venueFromRow for use by the httpserver
// shim layer (venues_test.go calls venueFromRow via catalog_shims.go).
func VenueFromRow(v gen.VenueRow) VenueResponse { return venueFromRow(v) }

func venueFromRow(v gen.VenueRow) venueResponse {
	resp := venueResponse{
		ID:              v.ID.String(),
		OrgID:           v.OrgID.String(),
		Name:            v.Name,
		Address:         v.Address,
		CapacityDefault: v.CapacityDefault,
		CreatedAt:       v.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       v.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if v.CityID != nil {
		s := v.CityID.String()
		resp.CityID = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/venues
// ─────────────────────────────────────────────────────────────────────────────

type createVenueRequest struct {
	Name            string `json:"name"`
	CityID          string `json:"city_id"`
	Address         string `json:"address"`
	CapacityDefault *int32 `json:"capacity_default"`
}

func (h *Handler) HandleCreateVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil || h.pool == nil {
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
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req createVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.CityID = strings.TrimSpace(req.CityID)
	req.Address = strings.TrimSpace(req.Address)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	var cityID *uuid.UUID
	if req.CityID != "" {
		parsed, parseErr := uuid.Parse(req.CityID)
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_city_id", "city_id must be a valid UUID", r,
				map[string]any{"field": "city_id"},
			))
			return
		}
		cityID = &parsed
	}

	var address *string
	if req.Address != "" {
		a := req.Address
		address = &a
	}

	v, err := h.venueQueries.InsertVenue(ctx, orgID, cityID, req.Name, address, req.CapacityDefault)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("venue: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.insert_failed", "failed to create venue", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListVenues(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	rows, err := h.venueQueries.ListVenues(ctx)
	if err != nil {
		h.logger.Error("venue: list all failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	v, err := h.venueQueries.GetVenueByID(ctx, venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.get_failed", "failed to get venue", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/venues
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListVenuesByOrg(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
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

	rows, err := h.venueQueries.ListVenuesByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("venue: list by org failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

type updateVenueRequest struct {
	Name            string  `json:"name"`
	CityID          *string `json:"city_id"`
	Address         *string `json:"address"`
	CapacityDefault *int32  `json:"capacity_default"`
}

func (h *Handler) HandleUpdateVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil || h.pool == nil {
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
	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req updateVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)

	var cityID *uuid.UUID
	if req.CityID != nil {
		trimmed := strings.TrimSpace(*req.CityID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"venue.invalid_city_id", "city_id must be a valid UUID", r,
					map[string]any{"field": "city_id"},
				))
				return
			}
			cityID = &parsed
		}
	}

	var address *string
	if req.Address != nil {
		trimmed := strings.TrimSpace(*req.Address)
		address = &trimmed
	}

	updated, err := h.venueQueries.UpdateVenue(ctx, venueID, orgID, cityID, req.Name, address, req.CapacityDefault)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("venue: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.update_failed", "failed to update venue", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil || h.pool == nil {
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
	venueID, ok := httputil.UUIDPathParam(w, r, "id")
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

	qtx := h.venueQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteVenue(ctx, venueID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.delete_failed", "failed to delete venue", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.venue.delete",
			ResourceType: "venue",
			ResourceID:   venueID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"venue_name": deleted.Name,
				"org_id":     orgID.String(),
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("venue: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"venue.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue":   venueFromRow(deleted),
		"deleted": true,
	})
}
