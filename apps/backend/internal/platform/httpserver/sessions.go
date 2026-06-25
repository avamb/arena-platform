// sessions.go implements the session CRUD API endpoints (feature #126).
//
// A Session is a specific time slot for an Event. Each session has independent
// inventory: capacity_total tracks the total seats available for that slot.
// Multiple sessions per event are allowed; overlapping sessions are permitted
// but flagged in the response (has_overlapping_sessions).
//
// Status lifecycle: draft → scheduled → completed|cancelled.
// Date invariant: end_at must be strictly after start_at (table CHECK + handler).
//
// Capacity propagation hook: whenever capacity_total changes, the handler
// calls the capacity hook to notify the inventory module. In this milestone the
// hook is a no-op log statement; the real inventory integration is out of scope.
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/events/{event_id}/sessions        — create (session.create)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions        — list   (session.read)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — get    (session.read)
//	PATCH  /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — update (session.update)
//	DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — delete (session.delete)
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Valid session statuses and transitions (forwarders to internal/domain/catalog).
//
// The state-machine has moved to the pure-domain layer (feature #183). The
// local names are preserved as thin forwarders so the handlers below and the
// in-package tests (sessions_test.go) compile unchanged.
// ─────────────────────────────────────────────────────────────────────────────

// validSessionStatuses lists the allowed status values for sessions. Backed
// by the catalog domain layer.
var validSessionStatuses = map[string]bool{
	string(catalogdomain.SessionStatusDraft):     true,
	string(catalogdomain.SessionStatusScheduled): true,
	string(catalogdomain.SessionStatusCancelled): true,
	string(catalogdomain.SessionStatusCompleted): true,
}

// isValidSessionTransition returns true when the transition from → to is
// allowed by the Session state machine. Forwards to internal/domain/catalog
// so the rule lives in exactly one place.
func isValidSessionTransition(from, to string) bool {
	return catalogdomain.IsValidSessionTransition(from, to)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// sessionResponse is the JSON representation of a single session.
type sessionResponse struct {
	ID                     string `json:"id"`
	EventID                string `json:"event_id"`
	StartAt                string `json:"start_at"`
	EndAt                  string `json:"end_at"`
	CapacityTotal          int32  `json:"capacity_total"`
	Status                 string `json:"status"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	HasOverlappingSessions bool   `json:"has_overlapping_sessions"`
}

// sessionFromRow converts a SessionRow to a sessionResponse.
// hasOverlap is the result of the overlap detection check.
func sessionFromRow(s gen.SessionRow, hasOverlap bool) sessionResponse {
	return sessionResponse{
		ID:                     s.ID.String(),
		EventID:                s.EventID.String(),
		StartAt:                s.StartAt.UTC().Format(time.RFC3339),
		EndAt:                  s.EndAt.UTC().Format(time.RFC3339),
		CapacityTotal:          s.CapacityTotal,
		Status:                 s.Status,
		CreatedAt:              s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              s.UpdatedAt.UTC().Format(time.RFC3339),
		HasOverlappingSessions: hasOverlap,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Overlap detection helpers
// ─────────────────────────────────────────────────────────────────────────────

// detectSessionOverlaps returns true when any two sessions in the list overlap.
// Two sessions overlap when a.start_at < b.end_at AND a.end_at > b.start_at.
// This is an O(n²) check applied at the application layer per the feature spec.
//
// The pure-domain implementation lives in internal/domain/catalog as
// DetectOverlaps over the adapter-free SessionInterval value type; this
// forwarder projects the gen.SessionRow slice into that value type so the
// domain layer never imports the adapters/postgres/gen package (feature #183).
func detectSessionOverlaps(sessions []gen.SessionRow) bool {
	intervals := make([]catalogdomain.SessionInterval, len(sessions))
	for i, s := range sessions {
		intervals[i] = catalogdomain.SessionInterval{StartAt: s.StartAt, EndAt: s.EndAt}
	}
	return catalogdomain.DetectOverlaps(intervals)
}

// ─────────────────────────────────────────────────────────────────────────────
// Capacity propagation hook (foundation placeholder)
// ─────────────────────────────────────────────────────────────────────────────

// onCapacityChange is called whenever a session's capacity_total is updated.
// It propagates the new capacity to the inventory ledger (feature #130).
func (s *Server) onCapacityChange(ctx context.Context, sessionID uuid.UUID, oldCapacity, newCapacity int32) {
	s.logger.Info("session: capacity changed — propagating to inventory ledger",
		slog.String("session_id", sessionID.String()),
		slog.Int("old_capacity", int(oldCapacity)),
		slog.Int("new_capacity", int(newCapacity)),
	)
	if s.inventoryQueries != nil {
		newTotalPtr := newCapacity
		if _, err := s.inventoryQueries.UpdateCapacityTotal(ctx, sessionID, nil, &newTotalPtr); err != nil {
			s.logger.Error("session: inventory capacity propagation failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
			// Non-fatal: the inventory ledger may not exist yet (if it hasn't been initialized).
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions
// ─────────────────────────────────────────────────────────────────────────────

// createSessionRequest is the request body for POST .../sessions.
type createSessionRequest struct {
	StartAt       string `json:"start_at"`
	EndAt         string `json:"end_at"`
	CapacityTotal int32  `json:"capacity_total"`
	Status        string `json:"status"`
}

// handleCreateSession serves POST /v1/organizations/{org_id}/events/{event_id}/sessions.
// Requires JWT + "session.create" permission.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req createSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_status", "status must be one of: draft, scheduled, cancelled, completed", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Parse start_at.
	if req.StartAt == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.missing_start_at", "start_at is required", r,
			map[string]any{"field": "start_at"},
		))
		return
	}
	startAt, parseErr := time.Parse(time.RFC3339, req.StartAt)
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "start_at"},
		))
		return
	}

	// Parse end_at.
	if req.EndAt == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.missing_end_at", "end_at is required", r,
			map[string]any{"field": "end_at"},
		))
		return
	}
	endAt, parseErr := time.Parse(time.RFC3339, req.EndAt)
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Date invariant: end_at must be after start_at.
	if !endAt.After(startAt) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// capacity_total must be positive.
	if req.CapacityTotal <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_capacity", "capacity_total must be greater than 0", r,
			map[string]any{"field": "capacity_total"},
		))
		return
	}

	sess, err := s.sessionQueries.InsertSession(ctx, eventID, startAt, endAt, req.CapacityTotal, req.Status)
	if err != nil {
		s.logger.Error("session: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.insert_failed", "failed to create session", r,
		))
		return
	}

	// Initialize the inventory ledger for the new session (feature #130, non-fatal).
	if s.inventoryQueries != nil {
		capTotal := sess.CapacityTotal
		if _, err := s.inventoryQueries.InsertInventoryLedger(ctx, sess.ID, nil, &capTotal); err != nil {
			s.logger.Warn("session: inventory initialization failed (non-fatal)",
				slog.String("session_id", sess.ID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	// Check for overlapping sessions (allowed but flagged).
	overlapCount, overlapErr := s.sessionQueries.CountOverlappingSessions(ctx, eventID, uuid.Nil, startAt, endAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	if hasOverlap {
		s.logger.Warn("session: overlapping sessions detected",
			slog.String("session_id", sess.ID.String()),
			slog.String("event_id", eventID.String()),
			slog.Int("overlap_count", int(overlapCount)),
		)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session": sessionFromRow(sess, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions
// ─────────────────────────────────────────────────────────────────────────────

// handleListSessions serves GET /v1/organizations/{org_id}/events/{event_id}/sessions.
// Returns all active sessions for the specified event.
// Requires JWT + "session.read" permission.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}

	rows, err := s.sessionQueries.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		s.logger.Error("session: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.list_failed", "failed to list sessions", r,
		))
		return
	}

	// Detect overlaps across the returned list at the application layer.
	hasOverlap := detectSessionOverlaps(rows)

	result := make([]sessionResponse, 0, len(rows))
	for _, sess := range rows {
		result = append(result, sessionFromRow(sess, hasOverlap))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":                 result,
		"has_overlapping_sessions": hasOverlap,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetSession serves GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.read" permission.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	sess, err := s.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("session.not_found", "session not found", r))
			return
		}
		s.logger.Error("session: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	// Check overlap with siblings.
	overlapCount, overlapErr := s.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, sess.StartAt, sess.EndAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	writeJSON(w, http.StatusOK, map[string]any{
		"session": sessionFromRow(sess, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateSessionRequest is the request body for PATCH .../sessions/{id}.
// All fields are optional; nil/empty values leave the existing value unchanged.
type updateSessionRequest struct {
	StartAt       *string `json:"start_at"`
	EndAt         *string `json:"end_at"`
	CapacityTotal *int32  `json:"capacity_total"`
	Status        string  `json:"status"`
}

// handleUpdateSession serves PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.update" permission.
// Triggers the capacity propagation hook when capacity_total changes.
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req updateSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_status", "status must be one of: draft, scheduled, cancelled, completed", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Parse optional start_at.
	var startAt *time.Time
	if req.StartAt != nil {
		trimmed := strings.TrimSpace(*req.StartAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
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
					"session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
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
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Validate capacity if provided.
	if req.CapacityTotal != nil && *req.CapacityTotal <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"session.invalid_capacity", "capacity_total must be greater than 0", r,
			map[string]any{"field": "capacity_total"},
		))
		return
	}

	// Fetch the current session to detect capacity changes and validate status transitions.
	current, err := s.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("session.not_found", "session not found", r))
			return
		}
		s.logger.Error("session: get for update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	// Validate status transition when status is being changed.
	if req.Status != "" && req.Status != current.Status {
		if !isValidSessionTransition(current.Status, req.Status) {
			writeJSON(w, http.StatusUnprocessableEntity, errorEnvelopeWithDetails(
				"session.invalid_transition",
				"status transition from '"+current.Status+"' to '"+req.Status+"' is not allowed",
				r,
				map[string]any{
					"current_status": current.Status,
					"target_status":  req.Status,
				},
			))
			return
		}
	}

	updated, err := s.sessionQueries.UpdateSession(ctx, sessionID, eventID, startAt, endAt, req.CapacityTotal, req.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("session.not_found", "session not found", r))
			return
		}
		s.logger.Error("session: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.update_failed", "failed to update session", r,
		))
		return
	}

	// Capacity propagation hook: fire when capacity_total changed.
	if req.CapacityTotal != nil && *req.CapacityTotal != current.CapacityTotal {
		s.onCapacityChange(ctx, updated.ID, current.CapacityTotal, updated.CapacityTotal)
	}

	// Check overlap with siblings (excluding this session itself).
	effectiveStart := updated.StartAt
	effectiveEnd := updated.EndAt
	overlapCount, overlapErr := s.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, effectiveStart, effectiveEnd)
	hasOverlap := overlapErr == nil && overlapCount > 0

	writeJSON(w, http.StatusOK, map[string]any{
		"session": sessionFromRow(updated, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleDeleteSession serves DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "session.delete" permission.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.sessionQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "id")
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

	qtx := s.sessionQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteSession(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("session.not_found", "session not found", r))
			return
		}
		s.logger.Error("session: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.delete_failed", "failed to delete session", r,
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
			Action:       "v1.session.delete",
			ResourceType: "session",
			ResourceID:   sessionID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"event_id": eventID.String(),
				"status":   deleted.Status,
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("session: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"session.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"session.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session": sessionFromRow(deleted, false),
		"deleted": true,
	})
}
