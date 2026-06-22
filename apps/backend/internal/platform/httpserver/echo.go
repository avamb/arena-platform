// echo.go wires the example transactional command POST /v1/echo described
// in app_spec.txt §api_endpoints_summary. The endpoint is the smallest
// possible exercise of the full cross-cutting boundary stack:
//
//   - Auth          (Authorization: Bearer JWT, verified via auth.Provider)
//   - Idempotency   (Idempotency-Key header, verified+stored via idempotency.Store)
//   - Audit         (audit_events row written in the same TX as the response cache)
//   - Outbox        (outbox_events row written in the same TX — placeholder event
//                    that the OutboxDispatcher worker will publish in a later wave)
//
// Persisting all four rows in one pgx.Tx is the foundation guarantee that
// makes feature #5 ("Backend API queries real database") observable: a
// single /v1/echo call produces BEGIN, INSERT audit_events, INSERT
// outbox_events, INSERT idempotency_keys, and COMMIT in the SQL query log.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/jackc/pgx/v5"
)

// MaxEchoMessageBytes caps the size of the echoed message so a malicious
// client cannot DoS the database with multi-megabyte audit rows.
const MaxEchoMessageBytes = 8 * 1024

// echoAuditAction is the stable audit action identifier written to
// audit_events.action for every successful POST /v1/echo request.
// Using a dotted, hierarchical naming convention (resource.verb) rather
// than a bare verb keeps the audit log queryable and human-readable.
const echoAuditAction = "v1.echo.create"

// echoOutboxEventType is the stable event_type identifier written to
// outbox_events.event_type for every successful POST /v1/echo request.
// Follows the "v1.<resource>.<verb>" convention used by the audit action.
const echoOutboxEventType = "v1.echo.created"

// echoOutboxAggregateType is the aggregate_type written to outbox_events.
// It identifies the domain aggregate the event belongs to — "echo" for
// the placeholder echo resource used in this foundational milestone.
const echoOutboxAggregateType = "echo"

// echoRequest is the request body schema for POST /v1/echo.
type echoRequest struct {
	Message string `json:"message"`
}

// echoResponse is the response body. snake_case per design system spec.
type echoResponse struct {
	Message       string `json:"message"`
	ActorID       string `json:"actor_id"`
	RequestID     string `json:"request_id"`
	TraceID       string `json:"trace_id"`
	EchoEventID   string `json:"echo_event_id"`
	IdempotentKey string `json:"idempotent_key"`
	IssuedAt      string `json:"issued_at"`
}

// handleEcho serves POST /v1/echo.
//
// Pre-conditions enforced by middleware (already on chain before we get here):
//
//   - auth.Middleware       → actor present in ctx; otherwise we never run.
//   - idempotency.Middleware → either replayed a stored response (we never
//                              run) OR placed key/scope/hash on the context.
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		// Middleware should have stopped this, but defence-in-depth.
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth_required", "authentication required", r))
		return
	}

	// Body decode with size cap.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxEchoMessageBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) > MaxEchoMessageBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorEnvelope("body_too_large",
			fmt.Sprintf("request body exceeds %d bytes", MaxEchoMessageBytes), r))
		return
	}
	var req echoRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("invalid_json", "request body is not valid JSON: "+err.Error(), r))
			return
		}
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("missing_field", "field 'message' is required and must be non-empty", r))
		return
	}

	if s.audit == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("not_ready", "echo dependencies not wired", r))
		return
	}

	// Read the request-id from context (set by the requestContext middleware in
	// the adapter chain). This is the same value written to the X-Request-Id
	// response header so audit rows and response headers always match.
	requestID := logging.RequestID(ctx)
	traceID := logging.TraceID(ctx)
	clientIP := extractClientIP(r)
	idemKey, _, _, _, _ := idempotency.FromContext(ctx)

	// -------------------------------------------------------------------------
	// Begin transaction. audit_events + outbox_events + idempotency_keys all
	// commit together (or roll back together on any error).
	// -------------------------------------------------------------------------
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("echo: begin tx", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("db_unavailable", "begin transaction: "+err.Error(), r))
		return
	}
	// Defer rollback. Commit at the bottom shadows this; if we never reach
	// commit (return early on error) the rollback restores a clean state.
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// 1. Audit row
	auditEv := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    string(actor.Type),
		ActorID:      actor.ID,
		Action:       echoAuditAction,
		ResourceType: "echo_message",
		ResourceID:   idemKey, // resource is keyed by the idempotency key
		RequestID:    requestID,
		TraceID:      traceID,
		IP:           clientIP,
		Metadata: map[string]any{
			"message_length": len(req.Message),
			"issuer":         actor.Issuer,
		},
	}
	if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
		logger.Error("echo: write audit", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("audit_failed", "audit write failed: "+err.Error(), r))
		return
	}

	// 2. Outbox row — placeholder event the OutboxDispatcher worker will pick
	// up in a later wave. The event payload mirrors the echo response.
	echoEventID, err := insertOutboxEcho(ctx, tx, actor.ID, req.Message, requestID, traceID)
	if err != nil {
		logger.Error("echo: insert outbox", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("outbox_failed", "outbox write failed: "+err.Error(), r))
		return
	}

	// 3. Build the response body. We need to settle on the body BEFORE we
	// persist the idempotency row so the replay path returns byte-identical
	// JSON on retries.
	resp := echoResponse{
		Message:       req.Message,
		ActorID:       actor.ID,
		RequestID:     requestID,
		TraceID:       traceID,
		EchoEventID:   echoEventID,
		IdempotentKey: idemKey,
		IssuedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	respBody, err := json.Marshal(resp)
	if err != nil {
		logger.Error("echo: marshal response", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("marshal_failed", "marshal response: "+err.Error(), r))
		return
	}

	// 4. Idempotency row — saved in the same transaction so a crash between
	// the audit row and the idempotency row cannot leave the system in a
	// state where an audit event exists for a request that the client will
	// happily retry.
	if err := idempotency.SaveTx(ctx, tx, w, idempotency.StoredResponse{
		Status:      http.StatusOK,
		ContentType: "application/json; charset=utf-8",
		Body:        respBody,
	}); err != nil {
		// SaveTx will return an error if the middleware state is missing
		// (i.e. the route was misconfigured) — that's a programmer bug, not
		// a runtime fault. Fall through to a 500.
		logger.Error("echo: save idempotency", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("idempotency_failed", "idempotency save failed: "+err.Error(), r))
		return
	}

	// 5. Commit.
	if err := tx.Commit(ctx); err != nil {
		logger.Error("echo: commit", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("commit_failed", "commit transaction: "+err.Error(), r))
		return
	}

	// 6. Flush response. We deliberately write the same bytes that were
	// stored under the idempotency row so any replay (whether by us on a
	// retry, or by the middleware on a re-issue) returns identical bytes.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)

	logger.Info("echo: ok",
		"actor_id", actor.ID,
		"idempotency_key", idemKey,
		"echo_event_id", echoEventID,
		"message_length", len(req.Message),
	)
}

// insertOutboxEcho writes the v1.echo.created event into outbox_events.
// Returns the generated id (UUID) so the response can echo it.
// The row is picked up by the OutboxDispatcher worker in a later milestone.
func insertOutboxEcho(ctx context.Context, tx pgx.Tx, actorID, message, requestID, traceID string) (string, error) {
	const q = `
		INSERT INTO outbox_events
		    (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES
		    ('echo', $1, 'v1.echo.created', $2::jsonb, now())
		RETURNING id::text
	`
	payload, err := json.Marshal(map[string]any{
		"actor_id":   actorID,
		"message":    message,
		"request_id": requestID,
		"trace_id":   traceID,
	})
	if err != nil {
		return "", fmt.Errorf("marshal outbox payload: %w", err)
	}
	var id string
	// aggregate_id is the actor id for now; later modules will key on a real
	// domain aggregate identifier.
	if err := tx.QueryRow(ctx, q, actorID, string(payload)).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// extractClientIP returns the best-guess client IP for audit purposes. Order:
//   - first entry in X-Forwarded-For (already normalised by chi RealIP)
//   - X-Real-IP
//   - r.RemoteAddr host portion
//
// Returns "" if nothing parseable was found — the audit row's IP column will
// then be NULL rather than failing the INSERT.
func extractClientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if idx := strings.Index(xff, ","); idx > 0 {
			xff = xff[:idx]
		}
		if ip := net.ParseIP(strings.TrimSpace(xff)); ip != nil {
			return ip.String()
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}
