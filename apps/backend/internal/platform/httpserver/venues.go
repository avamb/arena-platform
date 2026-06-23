// venues.go implements the venue CRUD API endpoints (feature #124).
//
// Venues are physical event locations owned by one organization. Any org can
// read venue data (GET endpoints are shared across orgs), but only the owning
// org can create, update, or soft-delete a venue (owner-gated mutations).
//
// Endpoints:
//
//   POST   /v1/organizations/{org_id}/venues        — create a venue (venue.create, owner only)
//   GET    /v1/venues                               — list all venues (venue.read, shared)
//   GET    /v1/venues/{id}                          — get a venue by ID (venue.read, shared)
//   GET    /v1/organizations/{org_id}/venues        — list venues for org (venue.read)
//   PATCH  /v1/organizations/{org_id}/venues/{id}   — update a venue (venue.update, owner only)
//   DELETE /v1/organizations/{org_id}/venues/{id}   — soft-delete a venue (venue.delete, owner only)
//
// Owner-gating is enforced by including org_id in the WHERE clause of every
// write query (UpdateVenue, SoftDeleteVenue, InsertVenue). A non-owning org
// cannot satisfy the WHERE clause and gets pgx.ErrNoRows → 404.
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// venueResponse is the JSON representation of a single venue.
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

// venueFromRow converts a VenueRow to a venueResponse.
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

// createVenueRequest is the request body for POST /v1/organizations/{org_id}/venues.
type createVenueRequest struct {
	Name            string `json:"name"`
	CityID          string `json:"city_id"`
	Address         string `json:"address"`
	CapacityDefault *int32 `json:"capacity_default"`
}

// handleCreateVenue serves POST /v1/organizations/{org_id}/venues.
// Requires JWT + "venue.create" permission.
// The venue is owned by the org identified in the path.
func (s *Server) handleCreateVenue(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil || s.pool == nil {
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
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req createVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.CityID = strings.TrimSpace(req.CityID)
	req.Address = strings.TrimSpace(req.Address)

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"venue.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	// Optional city_id.
	var cityID *uuid.UUID
	if req.CityID != "" {
		parsed, parseErr := uuid.Parse(req.CityID)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"venue.invalid_city_id", "city_id must be a valid UUID", r,
				map[string]any{"field": "city_id"},
			))
			return
		}
		cityID = &parsed
	}

	// Optional address.
	var address *string
	if req.Address != "" {
		s := req.Address
		address = &s
	}

	v, err := s.venueQueries.InsertVenue(ctx, orgID, cityID, req.Name, address, req.CapacityDefault)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		s.logger.Error("venue: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.insert_failed", "failed to create venue", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues
// ─────────────────────────────────────────────────────────────────────────────

// handleListVenues serves GET /v1/venues.
// Returns all active venues across all organizations (shared read-only).
// Requires JWT + "venue.read" permission.
func (s *Server) handleListVenues(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	rows, err := s.venueQueries.ListVenues(ctx)
	if err != nil {
		s.logger.Error("venue: list all failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetVenue serves GET /v1/venues/{id}.
// Shared read-only — any authenticated org may read any active venue.
// Requires JWT + "venue.read" permission.
func (s *Server) handleGetVenue(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	venueID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	v, err := s.venueQueries.GetVenueByID(ctx, venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		s.logger.Error("venue: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.get_failed", "failed to get venue", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/venues
// ─────────────────────────────────────────────────────────────────────────────

// handleListVenuesByOrg serves GET /v1/organizations/{org_id}/venues.
// Returns only the venues owned by the specified organization.
// Requires JWT + "venue.read" permission.
func (s *Server) handleListVenuesByOrg(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil {
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

	rows, err := s.venueQueries.ListVenuesByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("venue: list by org failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateVenueRequest is the request body for PATCH /v1/organizations/{org_id}/venues/{id}.
// All fields are optional; empty/nil values leave the existing value unchanged.
type updateVenueRequest struct {
	Name            string  `json:"name"`
	CityID          *string `json:"city_id"`
	Address         *string `json:"address"`
	CapacityDefault *int32  `json:"capacity_default"`
}

// handleUpdateVenue serves PATCH /v1/organizations/{org_id}/venues/{id}.
// Requires JWT + "venue.update" permission.
// Owner-gated: org_id in the path must match the venue's owning org.
func (s *Server) handleUpdateVenue(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil || s.pool == nil {
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
	venueID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req updateVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)

	// Parse optional city_id.
	var cityID *uuid.UUID
	if req.CityID != nil {
		trimmed := strings.TrimSpace(*req.CityID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"venue.invalid_city_id", "city_id must be a valid UUID", r,
					map[string]any{"field": "city_id"},
				))
				return
			}
			cityID = &parsed
		}
	}

	// Trim address if provided.
	var address *string
	if req.Address != nil {
		trimmed := strings.TrimSpace(*req.Address)
		address = &trimmed
	}

	updated, err := s.venueQueries.UpdateVenue(ctx, venueID, orgID, cityID, req.Name, address, req.CapacityDefault)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		s.logger.Error("venue: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.update_failed", "failed to update venue", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleDeleteVenue serves DELETE /v1/organizations/{org_id}/venues/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "venue.delete" permission.
// Owner-gated: org_id in the path must match the venue's owning org.
func (s *Server) handleDeleteVenue(w http.ResponseWriter, r *http.Request) {
	if s.venueQueries == nil || s.pool == nil {
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
	venueID, ok := uuidPathParam(w, r, "id")
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

	qtx := s.venueQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteVenue(ctx, venueID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		s.logger.Error("venue: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.delete_failed", "failed to delete venue", r,
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
			Action:       "v1.venue.delete",
			ResourceType: "venue",
			ResourceID:   venueID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"venue_name": deleted.Name,
				"org_id":     orgID.String(),
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("venue: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"venue.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"venue":   venueFromRow(deleted),
		"deleted": true,
	})
}
