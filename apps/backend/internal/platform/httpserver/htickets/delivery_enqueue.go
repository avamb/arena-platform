// delivery_enqueue.go — post-issuance delivery job enqueueing (feature #141, #149).
//
// EnqueueDeliveryJobs is called after IssueTicketsForCheckout returns a
// non-empty ticket slice. It inserts a delivery_jobs row and a worker_jobs
// row (type "ticket.deliver") for each issued ticket that has not yet been
// enqueued. The method is best-effort: individual errors are logged and
// skipped so delivery infrastructure issues never block ticket issuance.
//
// EnqueueComplimentaryDeliveryJobs (feature #149) works the same way but for
// complimentary (invitation) tickets. It sets Template="invitation" in the
// worker job payload so the delivery handler uses the invitation email template.
//
// Dependencies (no-op when absent):
//   - h.deliveryJobQueries — delivery_jobs DB access
//   - h.workerPool         — worker_jobs INSERT
package htickets

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// EnqueueDeliveryJobs creates one delivery_jobs row and one ticket.deliver
// worker_jobs row for each ticket in the slice.
//
// Idempotent per ticket: the delivery_jobs INSERT will be skipped or
// produce a duplicate row if the caller retries — the idempotency of the
// delivery pipeline is enforced at the worker handler level
// (see delivery.NewHandler).
func (h *Handler) EnqueueDeliveryJobs(ctx context.Context, tickets []gen.TicketRow) {
	if h.deliveryJobQueries == nil || h.workerPool == nil {
		return
	}

	for _, t := range tickets {
		ticketID := t.ID

		// Insert delivery_jobs row (recipient_email = ticket.holder_email or nil).
		dj, err := h.deliveryJobQueries.InsertDeliveryJob(ctx, ticketID, t.HolderEmail)
		if err != nil {
			h.logger.Warn("delivery: insert delivery_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", err.Error()),
			)
			continue
		}

		// Build the worker job payload. SEAT-C3 (feature #311): copy the
		// denormalized seat coordinates into the payload so the delivery
		// worker can render Sector / Row / Seat into the PDF and email
		// without re-joining session_seats at delivery time.
		p := delivery.Payload{TicketID: ticketID.String()}
		if t.SeatSector != nil {
			p.SeatSector = *t.SeatSector
		}
		if t.SeatRow != nil {
			p.SeatRow = *t.SeatRow
		}
		if t.SeatNumber != nil {
			p.SeatNumber = *t.SeatNumber
		}
		body, jsonErr := json.Marshal(p)
		if jsonErr != nil {
			h.logger.Warn("delivery: marshal payload failed",
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
		if qErr := h.workerPool.QueryRow(ctx, insertJobSQL,
			delivery.JobType, body, 5,
		).Scan(&jobID); qErr != nil {
			h.logger.Warn("delivery: enqueue worker_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", qErr.Error()),
			)
			continue
		}

		h.logger.Info("delivery: job enqueued",
			slog.String("ticket_id", ticketID.String()),
			slog.String("delivery_job_id", dj.ID.String()),
			slog.String("worker_job_id", jobID),
		)
	}
}

// EnqueueComplimentaryDeliveryJobs creates one delivery_jobs row and one
// ticket.deliver worker_jobs row (with template="invitation") for each
// complimentary ticket in the slice. (feature #149)
//
// The invitation template flag causes the delivery handler to send an
// invitation-style email rather than the standard ticket delivery email.
//
// Best-effort: individual errors are logged and skipped so delivery issues
// never roll back a committed complimentary issuance.
func (h *Handler) EnqueueComplimentaryDeliveryJobs(ctx context.Context, tickets []gen.ComplimentaryTicketRow) {
	if h.deliveryJobQueries == nil || h.workerPool == nil {
		return
	}

	for _, t := range tickets {
		ticketID := t.ID

		// Insert delivery_jobs row.
		dj, err := h.deliveryJobQueries.InsertDeliveryJob(ctx, ticketID, t.HolderEmail)
		if err != nil {
			h.logger.Warn("complimentary delivery: insert delivery_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", err.Error()),
			)
			continue
		}

		// Build the worker job payload with invitation template flag.
		p := delivery.Payload{
			TicketID: ticketID.String(),
			Template: delivery.TemplateInvitation,
		}
		body, jsonErr := json.Marshal(p)
		if jsonErr != nil {
			h.logger.Warn("complimentary delivery: marshal payload failed",
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
		if qErr := h.workerPool.QueryRow(ctx, insertJobSQL,
			delivery.JobType, body, 5,
		).Scan(&jobID); qErr != nil {
			h.logger.Warn("complimentary delivery: enqueue worker_job failed",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", qErr.Error()),
			)
			continue
		}

		h.logger.Info("complimentary delivery: invitation job enqueued",
			slog.String("ticket_id", ticketID.String()),
			slog.String("delivery_job_id", dj.ID.String()),
			slog.String("worker_job_id", jobID),
			slog.String("template", delivery.TemplateInvitation),
		)
	}
}
