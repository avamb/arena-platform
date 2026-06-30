// admin_ticket_delivery.go — support-console ticket delivery endpoints
// (feature #291, T-4).
//
// These two endpoints back the "Delivery" section that lives inside the
// admin tickets drawer (apps/admin-web/src/routes/tickets.tsx). They are
// surfaced under /v1/admin so they share the existing X-Admin-Reason +
// audit-log + cross-tenant gate already used by GET /v1/admin/tickets.
//
//	GET  /v1/admin/tickets/{id}/delivery        — last delivery attempt
//	POST /v1/admin/tickets/{id}/delivery/resend — enqueue a new attempt
//
// RBAC (spec): `ticket.update` OR `support.act`. The route mounts pick
// `ticket.update` as the canonical permission; the `support.act`
// fallback is enforced via the application Checker (a future RBAC
// engine can grant either permission; AllowAll() passes both today).
//
// The resend endpoint inserts a new `delivery_jobs` row (status='pending')
// and a companion `worker_jobs` row of type "ticket.deliver" so the
// existing worker handler picks the job up on its normal poll cycle.
// Both writes are best-effort and individually logged so a partial
// failure is observable but does not panic the request.
package htickets

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/tickets/{id}/delivery
// ─────────────────────────────────────────────────────────────────────────────

// HandleAdminGetTicketDelivery returns the most recent delivery_jobs row for
// the given ticket id. Returns 404 when no delivery has ever been attempted.
//
// Requires JWT + ticket.update + X-Admin-Reason (mounted via applyAuth).
func (h *Handler) HandleAdminGetTicketDelivery(w http.ResponseWriter, r *http.Request) {
	if h.deliveryJobQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "delivery store is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	ticketID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	dj, err := h.deliveryJobQueries.GetDeliveryJobByTicketID(r.Context(), ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logTicketDeliveryAudit(r, "v1.admin.ticket.delivery.read",
				ticketID.String(), reason, map[string]any{"found": false})
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"ticket_delivery.not_found",
				"no delivery has been attempted for this ticket",
				r,
			))
			return
		}
		h.logger.Error("admin_ticket_delivery: query failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket_delivery.internal", "failed to load delivery status", r,
		))
		return
	}

	h.logTicketDeliveryAudit(r, "v1.admin.ticket.delivery.read",
		ticketID.String(), reason, map[string]any{
			"found":           true,
			"delivery_status": dj.Status,
			"attempts":        dj.Attempts,
		})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"delivery": deliveryJobToMap(dj),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/tickets/{id}/delivery/resend
// ─────────────────────────────────────────────────────────────────────────────

// HandleAdminResendTicketDelivery enqueues a new delivery attempt for the
// given ticket id. Inserts a fresh `delivery_jobs` row (status='pending')
// and a companion `worker_jobs` row that the existing ticket.deliver worker
// will pick up. Returns the newly inserted delivery_jobs row.
//
// Requires JWT + ticket.update + X-Admin-Reason (mounted via applyAuth).
func (h *Handler) HandleAdminResendTicketDelivery(w http.ResponseWriter, r *http.Request) {
	if h.deliveryJobQueries == nil || h.workerPool == nil || h.ticketQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "delivery store is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	ticketID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()

	// Confirm the ticket exists before enqueuing a job so we return a
	// clean 404 rather than a deferred worker-side failure.
	t, err := h.ticketQueries.GetTicketByID(ctx, ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"ticket_delivery.ticket_not_found",
				"ticket not found",
				r,
			))
			return
		}
		h.logger.Error("admin_ticket_delivery: load ticket failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket_delivery.internal", "failed to load ticket", r,
		))
		return
	}

	// Insert a fresh delivery_jobs row with the latest known recipient email.
	dj, err := h.deliveryJobQueries.InsertDeliveryJob(ctx, ticketID, t.HolderEmail)
	if err != nil {
		h.logger.Error("admin_ticket_delivery: insert delivery_job failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket_delivery.enqueue_failed", "failed to enqueue delivery job", r,
		))
		return
	}

	// Build the worker job payload and enqueue it via the same SQL used by
	// the post-issuance enqueuer so semantics line up exactly.
	p := delivery.Payload{TicketID: ticketID.String()}
	body, jsonErr := json.Marshal(p)
	if jsonErr != nil {
		h.logger.Error("admin_ticket_delivery: marshal payload failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", jsonErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket_delivery.enqueue_failed", "failed to build worker payload", r,
		))
		return
	}
	const insertJobSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now())
		RETURNING id::text`
	var workerJobID string
	if qErr := h.workerPool.QueryRow(ctx, insertJobSQL,
		delivery.JobType, body, 5,
	).Scan(&workerJobID); qErr != nil {
		h.logger.Error("admin_ticket_delivery: enqueue worker_job failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("delivery_job_id", dj.ID.String()),
			slog.String("error", qErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket_delivery.enqueue_failed", "failed to enqueue worker job", r,
		))
		return
	}

	h.logger.Info("admin_ticket_delivery: resend enqueued",
		slog.String("ticket_id", ticketID.String()),
		slog.String("delivery_job_id", dj.ID.String()),
		slog.String("worker_job_id", workerJobID),
	)
	h.logTicketDeliveryAudit(r, "v1.admin.ticket.delivery.resend",
		ticketID.String(), reason, map[string]any{
			"delivery_job_id": dj.ID.String(),
			"worker_job_id":   workerJobID,
		})

	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"delivery":      deliveryJobToMap(dj),
		"worker_job_id": workerJobID,
	})
}

// deliveryJobToMap renders a DeliveryJobRow as the JSON object expected by
// the admin UI. nil time fields and nullable strings become explicit JSON nulls.
func deliveryJobToMap(dj gen.DeliveryJobRow) map[string]any {
	m := map[string]any{
		"id":         dj.ID.String(),
		"ticket_id":  dj.TicketID.String(),
		"status":     dj.Status,
		"attempts":   dj.Attempts,
		"queued_at":  dj.QueuedAt.UTC().Format(time.RFC3339),
		"created_at": dj.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": dj.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if dj.RecipientEmail != nil {
		m["recipient_email"] = *dj.RecipientEmail
	} else {
		m["recipient_email"] = nil
	}
	if dj.LastError != nil {
		m["last_error"] = *dj.LastError
	} else {
		m["last_error"] = nil
	}
	if dj.SentAt != nil {
		m["sent_at"] = dj.SentAt.UTC().Format(time.RFC3339)
	} else {
		m["sent_at"] = nil
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// logTicketDeliveryAudit emits a fire-and-forget audit event for the
// support-console delivery endpoints. Failures are logged but do not abort
// the response.
func (h *Handler) logTicketDeliveryAudit(
	r *http.Request,
	action, ticketID, reason string,
	extra map[string]any,
) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	metadata := map[string]any{"reason": reason}
	for k, v := range extra {
		metadata[k] = v
	}
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "ticket",
		ResourceID:   ticketID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("admin_ticket_delivery: audit write failed",
			slog.String("action", action),
			slog.Any("error", err),
		)
	}
}
