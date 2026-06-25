// report_delivery_enqueue.go — post-report-trigger delivery job enqueueing (feature #160).
//
// enqueueReportDeliveryJob is called from handleTriggerEventReport after the
// event_reports row is created in 'pending' state. It enqueues a worker_jobs
// row of type "report.deliver" so the report delivery handler runs once the
// report generation worker transitions the report to 'ready'.
//
// The delivery handler (reportdelivery.NewHandler) retries the job while the
// report remains in a non-terminal state, so enqueueing at trigger time is safe.
//
// Best-effort: errors are logged and swallowed so a delivery infrastructure
// issue never blocks the trigger API response.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/reportdelivery"
)

// enqueueReportDeliveryJob creates a worker_jobs row of type "report.deliver"
// for the given report ID. Called after a new event_reports row is created in
// 'pending' state by handleTriggerEventReport.
//
// No-op when workerPool is nil (delivery infrastructure not configured).
func (s *Server) enqueueReportDeliveryJob(ctx context.Context, reportID uuid.UUID) {
	if s.workerPool == nil {
		return
	}

	p := reportdelivery.Payload{ReportID: reportID.String()}
	body, jsonErr := json.Marshal(p)
	if jsonErr != nil {
		s.logger.Warn("reportdelivery: marshal payload failed",
			slog.String("report_id", reportID.String()),
			slog.String("error", jsonErr.Error()),
		)
		return
	}

	const insertJobSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now())
		RETURNING id::text`

	var jobID string
	if qErr := s.workerPool.QueryRow(ctx, insertJobSQL,
		reportdelivery.JobType, body, 5,
	).Scan(&jobID); qErr != nil {
		s.logger.Warn("reportdelivery: enqueue worker_job failed",
			slog.String("report_id", reportID.String()),
			slog.String("error", qErr.Error()),
		)
		return
	}

	s.logger.Info("reportdelivery: delivery job enqueued",
		slog.String("report_id", reportID.String()),
		slog.String("worker_job_id", jobID),
	)
}
