// event_status.go — the write half of POST .../events/{id}/status (EC-10).
//
// Moving an event between statuses is audited: every real transition writes
// `v1.event.status_update` (from, to) in the SAME transaction as the UPDATE, so
// there is never a status change without its row, nor a row for a change that
// rolled back. Taking an event off sale (published -> draft) touches nothing
// else: the sessions, the orders and the tickets stay as they are, and the
// event's publication rows are kept. They are what ties the event to its
// channels, and they are inert while the status is not `published` (every
// public surface filters on it), so publishing the event again restores
// exactly the channels it was sold in. Dropping them would also silence the
// `event.changed` webhook that tells a site to hide the event, because the
// worker finds the sites through those rows.
package hcatalog

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// applyEventStatus writes the new status and its audit row in one transaction.
// The caller tells the sites afterwards. It answers the error itself and
// reports false.
func (h *Handler) applyEventStatus(w http.ResponseWriter, r *http.Request, current gen.EventRow, orgID uuid.UUID, to string) (gen.EventRow, bool) {
	ctx := r.Context()
	fail := func(err error, code, msg string) (gen.EventRow, bool) {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return gen.EventRow{}, false
		}
		h.logger.Error("event: update status failed", slog.String("code", code), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(code, msg, r))
		return gen.EventRow{}, false
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return gen.EventRow{}, false
	}
	defer func() { _ = tx.Rollback(ctx) }()

	updated, err := h.eventQueries.WithTx(tx).UpdateEventStatus(ctx, current.ID, orgID, to)
	if err != nil {
		return fail(err, "event.update_status_failed", "failed to update event status")
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.event.status_update",
			ResourceType: "event",
			ResourceID:   current.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"event_name":  updated.Name,
				"org_id":      orgID.String(),
				"from_status": current.Status,
				"to_status":   to,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			return fail(err, "event.audit_failed", "failed to write audit event")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fail(err, "event.commit_failed", "failed to commit transaction")
	}

	return updated, true
}
