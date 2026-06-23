// scaffold_echo.go implements the example transactional command
// POST /v1/scaffold/echo (feature #105).
//
// This endpoint is a SCAFFOLDING EXAMPLE that demonstrates the full
// cross-cutting boundary stack in a single PostgreSQL transaction:
//
//	auth → permission('scaffold.echo.create') → idempotency → BEGIN tx →
//	InsertScaffoldEcho (sqlc) → audit.Write → outbox.Append → COMMIT →
//	cache response in idempotency store → 201 Created
//
// It is intentionally free of any domain logic. The endpoint will be
// REMOVED when real domain command endpoints exist.
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/jackc/pgx/v5"
)

// scaffoldEchoAuditAction is the stable audit action for POST /v1/scaffold/echo.
const scaffoldEchoAuditAction = "v1.scaffold_echo.create"

// scaffoldEchoOutboxEventType is the stable event type for the outbox row.
const scaffoldEchoOutboxEventType = "v1.scaffold_echo.created"

// scaffoldEchoOutboxAggregateType is the aggregate type for the outbox row.
const scaffoldEchoOutboxAggregateType = "scaffold_echo"

// handleScaffoldEcho serves POST /v1/scaffold/echo.
//
// Pre-conditions enforced by middleware (already on chain before we get here):
//
//   - auth.Middleware            → actor present in ctx; otherwise we never run.
//   - permissions.RequirePermission → actor holds 'scaffold.echo.create'; 403 otherwise.
//   - idempotency.Middleware     → either replayed a stored response (we never
//     run) OR placed key/scope/hash on the context.
func (s *Server) handleScaffoldEcho(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// Defence-in-depth: middleware should have blocked unauthenticated access.
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.required", "authentication required", r))
		return
	}

	// Body decode with size cap.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxEchoMessageBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) > MaxEchoMessageBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorEnvelope("http.body_too_large",
			fmt.Sprintf("request body exceeds %d bytes", MaxEchoMessageBytes), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.empty_body", "request body is required and must be a JSON object", r))
		return
	}

	var req struct{ Message string `json:"message"` }
	if err := json.Unmarshal(body, &req); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"validation.field_type",
				"field 'message' must be a string",
				r,
				map[string]any{"field": "message", "expected": "string", "actual": typeErr.Value},
			))
			return
		}
		msg := "request body is not valid JSON"
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			msg = fmt.Sprintf("request body is not valid JSON: parse error near byte offset %d", syntaxErr.Offset)
		}
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_json", msg, r))
		return
	}

	// Validate: reject unknown fields, require non-empty message.
	{
		var rawMap map[string]json.RawMessage
		if err2 := json.Unmarshal(body, &rawMap); err2 == nil {
			for key := range rawMap {
				if key != "message" {
					writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
						"validation.unknown_field",
						"request body contains unknown field '"+key+"'",
						r,
						map[string]any{"field": key},
					))
					return
				}
			}
			msgRaw, exists := rawMap["message"]
			if !exists || string(msgRaw) == "null" {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"validation.field_required",
					"field 'message' is required",
					r,
					map[string]any{"field": "message"},
				))
				return
			}
			if strings.TrimSpace(req.Message) == "" {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"validation.field_empty",
					"field 'message' must not be empty",
					r,
					map[string]any{"field": "message"},
				))
				return
			}
		}
	}

	// Guard: all required dependencies must be wired.
	if s.audit == nil || s.pool == nil || s.outboxWriter == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("not_ready", "scaffold echo dependencies not wired", r))
		return
	}

	requestID := logging.RequestID(ctx)
	traceID := logging.TraceID(ctx)
	clientIP := extractClientIP(r)
	idemKey, _, _, _, _ := idempotency.FromContext(ctx)

	// -------------------------------------------------------------------------
	// Begin transaction. scaffold_echo + audit_events + outbox + idempotency_keys
	// all commit together (or roll back together on any error).
	// -------------------------------------------------------------------------
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("scaffold_echo: begin tx", "error", err.Error())
		w.Header().Set("Retry-After", dbRetryAfterSeconds)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable",
			"database is unavailable: "+err.Error(), r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Insert into scaffold_echo via sqlc-generated query.
	queries := gen.New(tx)
	row, err := queries.InsertScaffoldEcho(ctx, actor.ID, req.Message)
	if err != nil {
		logger.Error("scaffold_echo: insert", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("scaffold_echo.insert_failed",
			"scaffold_echo insert failed: "+err.Error(), r))
		return
	}

	// 2. Audit row — written in the same transaction.
	auditEv := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    string(actor.Type),
		ActorID:      actor.ID,
		Action:       scaffoldEchoAuditAction,
		ResourceType: "scaffold_echo",
		ResourceID:   row.ID.String(),
		RequestID:    requestID,
		TraceID:      traceID,
		IP:           clientIP,
		Metadata: map[string]any{
			"message_length":   len(req.Message),
			"scaffold_echo_id": row.ID.String(),
			"issuer":           actor.Issuer,
		},
	}
	if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
		logger.Error("scaffold_echo: write audit", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("audit_failed",
			"audit write failed: "+err.Error(), r))
		return
	}

	// 3. Outbox row — domain event written via outbox.Writer in the same TX.
	outboxEv := outbox.Event{
		AggregateType: scaffoldEchoOutboxAggregateType,
		AggregateID:   row.ID.String(),
		EventType:     scaffoldEchoOutboxEventType,
		Payload: map[string]any{
			"scaffold_echo_id": row.ID.String(),
			"actor_id":         actor.ID,
			"message":          req.Message,
			"request_id":       requestID,
			"trace_id":         traceID,
		},
		OccurredAt: time.Now().UTC(),
	}
	if err := s.outboxWriter.Append(ctx, tx, outboxEv); err != nil {
		logger.Error("scaffold_echo: append outbox", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("outbox_failed",
			"outbox append failed: "+err.Error(), r))
		return
	}

	// 4. Build response body BEFORE saving to idempotency store so replay
	// returns byte-identical JSON.
	resp := struct {
		ID        string    `json:"id"`
		Message   string    `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}{
		ID:        row.ID.String(),
		Message:   row.Message,
		CreatedAt: row.CreatedAt.UTC(),
	}
	respBody, err := json.Marshal(resp)
	if err != nil {
		logger.Error("scaffold_echo: marshal response", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"marshal response: "+err.Error(), r))
		return
	}

	// 5. Persist idempotency row inside the same transaction.
	if err := idempotency.SaveTx(ctx, tx, w, idempotency.StoredResponse{
		Status:      http.StatusCreated,
		ContentType: "application/json; charset=utf-8",
		Body:        respBody,
	}); err != nil {
		logger.Error("scaffold_echo: save idempotency", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("idempotency_failed",
			"idempotency save failed: "+err.Error(), r))
		return
	}

	// 6. Commit — all four rows (scaffold_echo, audit_events, outbox, idempotency_keys)
	// are durable after this point or none are.
	if err := tx.Commit(ctx); err != nil {
		logger.Error("scaffold_echo: commit", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("commit_failed",
			"commit transaction: "+err.Error(), r))
		return
	}

	// 7. Write 201 Created response with the same bytes saved under the
	// idempotency key so replays are byte-identical.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(respBody)

	logger.Info("scaffold_echo: created",
		"actor_id", actor.ID,
		"scaffold_echo_id", row.ID.String(),
		"idempotency_key", idemKey,
		"message_length", len(req.Message),
	)
}
