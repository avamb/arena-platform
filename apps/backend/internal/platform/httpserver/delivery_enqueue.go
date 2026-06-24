// delivery_enqueue.go — post-issuance delivery job enqueueing (feature #141).
//
// enqueueDeliveryJobs is called after issueTicketsForCheckout returns a
// non-empty ticket slice. It inserts a delivery_jobs row and a worker_jobs
// row (type "ticket.deliver") for each issued ticket that has not yet been
// enqueued. The method is best-effort: individual errors are logged and
// skipped so delivery infrastructure issues never block ticket issuance.
//
// Dependencies (no-op when absent):
//   - s.deliveryJobQueries — delivery_jobs DB access
//   - s.workerPool         — worker_jobs INSERT
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// enqueueDeliveryJobs creates one delivery_jobs row and one ticket.deliver
// worker_jobs row for each ticket in the slice.
//
// Idempotent per ticket: the delivery_jobs INSERT will be skipped or
// produce a duplicate row if the caller retries — the idempotency of the
// delivery pipeline is enforced at the worker handler level
// (see delivery.NewHandler).
func (s *Server) enqueueDeliveryJobs(ctx context.Context, tickets []gen.TicketRow) {
	if s.deliveryJobQueries == nil || s.workerPool == nil {
		return
	}

	for _, t := range tickets {
		ticketID := t.ID

		// Insert delivery_jobs row (recipient_email = ticket.holder_email or nil).
		dj, err := s.deliveryJobQueries.InsertDeliveryJob(ctx, ticketID, t.HolderEmail)
		if err != nil {
			s.logger.Warn("delivery: insert delivery_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", err.Error()),
			)
			continue
		}

		// Build the worker job payload.
		p := delivery.Payload{TicketID: ticketID.String()}
		body, jsonErr := json.Marshal(p)
		if jsonErr != nil {
			s.logger.Warn("delivery: marshal payload failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", jsonErr.Error()),
			)
			continue
		}

		// Enqueue the ticket.deliver worker job.
		const insertJobSQL = `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, $2::jsonb, $3, 'pending', now())
			RETURNING id::text`
		var jobID string
		if qErr := s.workerPool.QueryRow(ctx, insertJobSQL,
			delivery.JobType, body, 5,
		).Scan(&jobID); qErr != nil {
			s.logger.Warn("delivery: enqueue worker_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", qErr.Error()),
			)
			continue
		}

		s.logger.Info("delivery: job enqueued",
			slog.String("ticket_id", ticketID.String()),
			slog.String("delivery_job_id", dj.ID.String()),
			slog.String("worker_job_id", jobID),
		)
	}
}
