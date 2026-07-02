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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Valid session statuses and transitions (forwarders to internal/domain/catalog).
//
// The state-machine has moved to the pure-domain layer (feature #183). The
// local names are preserved as thin forwarders so the handlers below and the
// in-package tests (sessions_test.go via catalog_shims.go) compile unchanged.
// ─────────────────────────────────────────────────────────────────────────────

// validSessionStatuses lists the allowed status values for sessions. Backed
// by the catalog domain layer.
var validSessionStatuses = map[string]bool{
	string(catalogdomain.SessionStatusDraft):     true,
	string(catalogdomain.SessionStatusScheduled): true,
	string(catalogdomain.SessionStatusCancelled): true,
	string(catalogdomain.SessionStatusCompleted): true,
}

// IsValidSessionTransition returns true when the transition from → to is
// allowed by the Session state machine. Forwards to internal/domain/catalog
// so the rule lives in exactly one place.
func IsValidSessionTransition(from, to string) bool {
	return catalogdomain.IsValidSessionTransition(from, to)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// SessionResponse is the JSON representation of a single session.
type SessionResponse struct {
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

// SessionFromRow converts a SessionRow to a SessionResponse.
// hasOverlap is the result of the overlap detection check.
func SessionFromRow(s gen.SessionRow, hasOverlap bool) SessionResponse {
	return SessionResponse{
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

// DetectSessionOverlaps returns true when any two sessions in the list overlap.
// Two sessions overlap when a.start_at < b.end_at AND a.end_at > b.start_at.
// This is an O(n²) check applied at the application layer per the feature spec.
//
// The pure-domain implementation lives in internal/domain/catalog as
// DetectOverlaps over the adapter-free SessionInterval value type; this
// forwarder projects the gen.SessionRow slice into that value type so the
// domain layer never imports the adapters/postgres/gen package (feature #183).
func DetectSessionOverlaps(sessions []gen.SessionRow) bool {
	intervals := make([]catalogdomain.SessionInterval, len(sessions))
	for i, s := range sessions {
		intervals[i] = catalogdomain.SessionInterval{StartAt: s.StartAt, EndAt: s.EndAt}
	}
	return catalogdomain.DetectOverlaps(intervals)
}

// ─────────────────────────────────────────────────────────────────────────────
// Capacity propagation hook (foundation placeholder)
// ─────────────────────────────────────────────────────────────────────────────

// OnCapacityChange is called whenever a session's capacity_total is updated.
// It propagates the new capacity to the inventory ledger (feature #130).
// Exported so catalog_shims.go can keep the original *Server onCapacityChange
// delegate alive for sessions_test.go.
func (h *Handler) OnCapacityChange(ctx context.Context, sessionID uuid.UUID, oldCapacity, newCapacity int32) {
	h.logger.Info("session: capacity changed — propagating to inventory ledger",
		slog.String("session_id", sessionID.String()),
		slog.Int("old_capacity", int(oldCapacity)),
		slog.Int("new_capacity", int(newCapacity)),
	)
	if h.inventoryQueries != nil {
		newTotalPtr := newCapacity
		if _, err := h.inventoryQueries.UpdateCapacityTotal(ctx, sessionID, nil, &newTotalPtr); err != nil {
			h.logger.Error("session: inventory capacity propagation failed",
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

// HandleCreateSession serves POST /v1/organizations/{org_id}/events/{event_id}/sessions.
// Requires JWT + "session.create" permission.
func (h *Handler) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req createSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_status", "status must be one of: draft, scheduled, cancelled, completed", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Parse start_at.
	if req.StartAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.missing_start_at", "start_at is required", r,
			map[string]any{"field": "start_at"},
		))
		return
	}
	startAt, parseErr := time.Parse(time.RFC3339, req.StartAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "start_at"},
		))
		return
	}

	// Parse end_at.
	if req.EndAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.missing_end_at", "end_at is required", r,
			map[string]any{"field": "end_at"},
		))
		return
	}
	endAt, parseErr := time.Parse(time.RFC3339, req.EndAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Date invariant: end_at must be after start_at.
	if !endAt.After(startAt) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// capacity_total must be positive.
	if req.CapacityTotal <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_capacity", "capacity_total must be greater than 0", r,
			map[string]any{"field": "capacity_total"},
		))
		return
	}

	sess, err := h.sessionQueries.InsertSession(ctx, eventID, startAt, endAt, req.CapacityTotal, req.Status)
	if err != nil {
		h.logger.Error("session: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.insert_failed", "failed to create session", r,
		))
		return
	}

	// Initialize the inventory ledger for the new session (feature #130, non-fatal).
	if h.inventoryQueries != nil {
		capTotal := sess.CapacityTotal
		if _, err := h.inventoryQueries.InsertInventoryLedger(ctx, sess.ID, nil, &capTotal); err != nil {
			h.logger.Warn("session: inventory initialization failed (non-fatal)",
				slog.String("session_id", sess.ID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	// Check for overlapping sessions (allowed but flagged).
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, uuid.Nil, startAt, endAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	if hasOverlap {
		h.logger.Warn("session: overlapping sessions detected",
			slog.String("session_id", sess.ID.String()),
			slog.String("event_id", eventID.String()),
			slog.Int("overlap_count", int(overlapCount)),
		)
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"session": SessionFromRow(sess, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions
// ─────────────────────────────────────────────────────────────────────────────

// HandleListSessions serves GET /v1/organizations/{org_id}/events/{event_id}/sessions.
// Returns all active sessions for the specified event.
// Requires JWT + "session.read" permission.
func (h *Handler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	rows, err := h.sessionQueries.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		h.logger.Error("session: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.list_failed", "failed to list sessions", r,
		))
		return
	}

	// Detect overlaps across the returned list at the application layer.
	hasOverlap := DetectSessionOverlaps(rows)

	result := make([]SessionResponse, 0, len(rows))
	for _, sess := range rows {
		result = append(result, SessionFromRow(sess, hasOverlap))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"sessions":                 result,
		"has_overlapping_sessions": hasOverlap,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetSession serves GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.read" permission.
func (h *Handler) HandleGetSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	sess, err := h.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	// Check overlap with siblings.
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, sess.StartAt, sess.EndAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(sess, hasOverlap),
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

// HandleUpdateSession serves PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.update" permission.
// Triggers the capacity propagation hook when capacity_total changes.
func (h *Handler) HandleUpdateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req updateSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Validate capacity if provided.
	if req.CapacityTotal != nil && *req.CapacityTotal <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_capacity", "capacity_total must be greater than 0", r,
			map[string]any{"field": "capacity_total"},
		))
		return
	}

	// Fetch the current session to detect capacity changes and validate status transitions.
	current, err := h.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: get for update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	// Validate status transition when status is being changed.
	if req.Status != "" && req.Status != current.Status {
		if !IsValidSessionTransition(current.Status, req.Status) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
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

	updated, err := h.sessionQueries.UpdateSession(ctx, sessionID, eventID, startAt, endAt, req.CapacityTotal, req.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.update_failed", "failed to update session", r,
		))
		return
	}

	// Capacity propagation hook: fire when capacity_total changed.
	if req.CapacityTotal != nil && *req.CapacityTotal != current.CapacityTotal {
		h.OnCapacityChange(ctx, updated.ID, current.CapacityTotal, updated.CapacityTotal)
	}

	// Webhook event catalog (feature S-1): emit v1.session.cancelled exactly
	// once when the status transitions into "cancelled".  Best-effort: errors
	// are logged inside publishScannerEvent. The publisher is injected as a
	// callback by catalog_shims.go because its implementation lives in the
	// hscanner sub-package.
	if req.Status == "cancelled" && current.Status != "cancelled" && updated.Status == "cancelled" {
		if h.publishSessionCancelled != nil {
			h.publishSessionCancelled(ctx, updated.ID.String(), eventID.String(), current.Status)
		}
	}

	// Check overlap with siblings (excluding this session itself).
	effectiveStart := updated.StartAt
	effectiveEnd := updated.EndAt
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, effectiveStart, effectiveEnd)
	hasOverlap := overlapErr == nil && overlapCount > 0

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(updated, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteSession serves DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "session.delete" permission.
func (h *Handler) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: soft-delete + audit in one atomic write.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.sessionQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteSession(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.delete_failed", "failed to delete session", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if h.audit != nil {
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
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"event_id": eventID.String(),
				"status":   deleted.Status,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("session: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"session.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(deleted, false),
		"deleted": true,
	})
}
