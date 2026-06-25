// gdpr.go implements GDPR data subject request endpoints (feature #164).
//
// Routes:
//
//	POST /v1/me/data-export   — queue an export request (returns 202 Accepted)
//	POST /v1/me/data-delete   — queue a deletion/anonymization request (returns 202 Accepted)
//	GET  /v1/me/data-requests — list the authenticated user's own GDPR requests
//
// All endpoints require JWT authentication. The authenticated user's ActorID
// is used as the user_id for all DB operations — a user can only access their
// own data.
//
// Processing is asynchronous: the HTTP endpoints create a pending
// data_subject_requests row and return 202 immediately. The arena-worker
// background process picks up pending rows (FOR UPDATE SKIP LOCKED) and
// calls the GDPRProcessor to generate the export JSON or anonymize the user.
//
// Anonymization retention policy (GDPR Article 17 + Russian accounting law):
//   - users.email, users.password_hash → replaced with placeholder values
//   - users.anonymized_at → set to now()
//   - Financial records (orders, payments) that reference user_id → RETAINED
//     (accounting law requires keeping financial records for 5+ years)
//   - Memberships, sessions, and other operational data → may be deleted
//     by the anonymization worker in a later milestone as more tables land
package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────────

// dataSubjectRequestResponse converts a DataSubjectRequestRow to the API response shape.
func dataSubjectRequestResponse(r gen.DataSubjectRequestRow) map[string]any {
	resp := map[string]any{
		"id":           r.ID.String(),
		"user_id":      r.UserID.String(),
		"request_type": r.RequestType,
		"status":       r.Status,
		"created_at":   r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.PayloadURL != nil {
		resp["payload_url"] = *r.PayloadURL
	}
	if r.ErrorMsg != nil {
		resp["error_msg"] = *r.ErrorMsg
	}
	if r.CompletedAt != nil {
		resp["completed_at"] = r.CompletedAt.UTC().Format(time.RFC3339)
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/me/data-export
// ─────────────────────────────────────────────────────────────────────────────

// handleDataExportRequest handles POST /v1/me/data-export.
//
// Creates a pending 'export' data_subject_request for the authenticated user
// and returns 202 Accepted. The arena-worker will pick up the request and
// generate a JSON dump of all user data.
//
// Body (optional JSON):
//
//	{} — no fields required; reserved for future options
//
// Response 202:
//
//	{"id": "...", "request_type": "export", "status": "pending", ...}
func (s *Server) handleDataExportRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- Identify the authenticated user ---
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthorized", "authentication required", r))
		return
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_actor_id", "invalid actor ID in token", r))
		return
	}

	// --- Validate Content-Type (allow missing body) ---
	ct := r.Header.Get("Content-Type")
	if ct != "" && ct != "application/json" && ct != "application/json; charset=utf-8" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorEnvelope("http.unsupported_media_type", "Content-Type must be application/json", r))
		return
	}

	// --- Drain body (optional; we don't use it yet) ---
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4*1024))

	// --- Create the pending request ---
	if s.gdprQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("gdpr.data_export: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)
	req, err := q.InsertDataSubjectRequest(ctx, userID, "export")
	if err != nil {
		logger.Error("gdpr.data_export: insert request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.request_insert_failed", "failed to create export request", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("gdpr.data_export: commit failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "failed to save request", r))
		return
	}

	logger.Info("gdpr.data_export: export request created",
		slog.String("request_id", req.ID.String()),
		slog.String("user_id", userID.String()),
	)

	writeJSON(w, http.StatusAccepted, dataSubjectRequestResponse(req))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/me/data-delete
// ─────────────────────────────────────────────────────────────────────────────

// handleDataDeleteRequest handles POST /v1/me/data-delete.
//
// Creates a pending 'delete' data_subject_request for the authenticated user
// and returns 202 Accepted. The arena-worker will pick up the request and
// anonymize the user's PII while retaining financial records.
//
// Body (optional JSON):
//
//	{} — no fields required; reserved for future confirmation token
//
// Response 202:
//
//	{"id": "...", "request_type": "delete", "status": "pending", ...}
func (s *Server) handleDataDeleteRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- Identify the authenticated user ---
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthorized", "authentication required", r))
		return
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_actor_id", "invalid actor ID in token", r))
		return
	}

	// --- Validate Content-Type (allow missing body) ---
	ct := r.Header.Get("Content-Type")
	if ct != "" && ct != "application/json" && ct != "application/json; charset=utf-8" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorEnvelope("http.unsupported_media_type", "Content-Type must be application/json", r))
		return
	}

	// --- Drain body ---
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4*1024))

	// --- Create the pending request ---
	if s.gdprQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("gdpr.data_delete: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)
	req, err := q.InsertDataSubjectRequest(ctx, userID, "delete")
	if err != nil {
		logger.Error("gdpr.data_delete: insert request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.request_insert_failed", "failed to create delete request", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("gdpr.data_delete: commit failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "failed to save request", r))
		return
	}

	logger.Info("gdpr.data_delete: deletion request created",
		slog.String("request_id", req.ID.String()),
		slog.String("user_id", userID.String()),
	)

	writeJSON(w, http.StatusAccepted, dataSubjectRequestResponse(req))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/me/data-requests
// ─────────────────────────────────────────────────────────────────────────────

// handleListDataRequests handles GET /v1/me/data-requests.
//
// Lists all GDPR requests submitted by the authenticated user, newest first.
//
// Response 200:
//
//	{"requests": [ { "id": "...", "request_type": "export", ... }, ... ]}
func (s *Server) handleListDataRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- Identify the authenticated user ---
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthorized", "authentication required", r))
		return
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_actor_id", "invalid actor ID in token", r))
		return
	}

	if s.gdprQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	rows, err := s.gdprQueries.ListDataSubjectRequestsByUser(ctx, userID)
	if err != nil {
		logger.Error("gdpr.list_requests: query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.query_failed", "failed to list requests", r))
		return
	}

	var items []map[string]any
	for _, row := range rows {
		items = append(items, dataSubjectRequestResponse(row))
	}
	if items == nil {
		items = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"requests": items})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/me/consent
// ─────────────────────────────────────────────────────────────────────────────

// handleRecordConsent handles POST /v1/me/consent.
//
// Records the authenticated user's consent. Can be called at registration time
// or any time the user updates their consent preferences.
//
// Body:
//
//	{"marketing_consent": true|false}
//
// Response 200:
//
//	{"message": "consent recorded"}
func (s *Server) handleRecordConsent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- Identify the authenticated user ---
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthorized", "authentication required", r))
		return
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_actor_id", "invalid actor ID in token", r))
		return
	}

	// --- Parse body ---
	ct := r.Header.Get("Content-Type")
	if ct != "" && ct != "application/json" && ct != "application/json; charset=utf-8" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorEnvelope("http.unsupported_media_type", "Content-Type must be application/json", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_body", "cannot read request body", r))
		return
	}

	var req struct {
		MarketingConsent bool `json:"marketing_consent"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	if s.gdprQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	if err := s.gdprQueries.RecordUserConsent(ctx, userID, req.MarketingConsent); err != nil {
		logger.Error("gdpr.consent: record consent failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.consent_failed", "failed to record consent", r))
		return
	}

	logger.Info("gdpr.consent: consent recorded",
		slog.String("user_id", userID.String()),
		slog.Bool("marketing_consent", req.MarketingConsent),
	)

	writeJSON(w, http.StatusOK, map[string]any{"message": "consent recorded"})
}
