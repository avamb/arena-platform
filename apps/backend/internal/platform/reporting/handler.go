// Package reporting implements the event.generate_report worker job type
// (feature #159).
//
// After event.end_at + a configurable cutoff window, the worker job:
//  1. Transitions the event_reports row to state='generating'.
//  2. Runs per-category aggregation queries (sales, refunds, complimentary,
//     scans, commissions, payouts) against the live database.
//  3. Inserts one event_report_lines row per category.
//  4. Transitions the event_reports row to state='ready'.
//  5. Emits an outbox domain event (v1.report.generated) and an audit log entry.
//
// On any aggregation error the handler transitions the row to state='failed',
// stores the error message, and returns a non-nil error so the worker retries
// the job (up to max_attempts). After max_attempts the job moves to
// worker_dead_letter and operators receive a failed report row.
//
// The commission and payout lines are derived from the sales aggregation:
//
//	commissions = gross_amount - net_amount (= platform_fee + provider_fee)
//	payouts     = net_amount
//
// This derivation avoids a separate aggregation query and keeps the six
// canonical line categories in sync with the sales computation.
package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobType is the worker job type string for post-event report generation.
const JobType = "event.generate_report"

// ReportEventType is the outbox event type emitted on successful generation.
const ReportEventType = "v1.report.generated"

// ReportAggregateType is the outbox aggregate_type for report domain events.
const ReportAggregateType = "event.report"

// DefaultCutoffDuration is the time window after event.end_at before report
// generation is triggered. Operators may override this via configuration.
const DefaultCutoffDuration = 2 * time.Hour

// Payload is the JSON payload stored in worker_jobs.payload for
// event.generate_report jobs.
//
// EventID and OrgID identify the event being reported. ReportID is the
// pre-created event_reports row that the handler should update (transitions
// pending → generating → ready | failed). CutoffTime records the
// scheduled_at time so the handler can populate report_window_end.
type Payload struct {
	EventID    string `json:"event_id"`
	OrgID      string `json:"org_id"`
	ReportID   string `json:"report_id"`
	CutoffTime string `json:"cutoff_time"` // RFC3339
}

// HandlerOptions bundles the dependencies required by the reporting handler.
type HandlerOptions struct {
	// ReportQueries provides access to event_reports and event_report_lines tables.
	ReportQueries *gen.Queries

	// OutboxWriter publishes the v1.report.generated domain event within a
	// short-lived transaction. When nil, event publication is skipped (dev/test).
	OutboxWriter outbox.Writer

	// Pool is used to begin the transaction for outbox publishing.
	// Required when OutboxWriter is non-nil; otherwise ignored.
	Pool *pgxpool.Pool

	// Logger receives structured log entries. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewHandler constructs a worker.HandlerFunc for event.generate_report jobs.
//
// The returned HandlerFunc is safe for concurrent calls. It closes over the
// provided HandlerOptions — all dependencies must remain valid for the
// lifetime of the worker process.
func NewHandler(opts HandlerOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, rawPayload []byte) error {
		var p Payload
		if err := json.Unmarshal(rawPayload, &p); err != nil {
			// Malformed payload is not retryable — return error so the job goes
			// to dead letter after max_attempts.
			return fmt.Errorf("reporting: unmarshal payload: %w", err)
		}

		reportID, err := uuid.Parse(p.ReportID)
		if err != nil {
			return fmt.Errorf("reporting: invalid report_id %q: %w", p.ReportID, err)
		}
		eventID, err := uuid.Parse(p.EventID)
		if err != nil {
			return fmt.Errorf("reporting: invalid event_id %q: %w", p.EventID, err)
		}

		logger.Info("report generation started",
			"report_id", reportID,
			"event_id", eventID,
		)

		if opts.ReportQueries == nil {
			return fmt.Errorf("reporting: ReportQueries is nil")
		}

		// 1. Transition to 'generating' state.
		if _, err := opts.ReportQueries.UpdateEventReportState(ctx, reportID, "generating", nil); err != nil {
			return fmt.Errorf("reporting: transition to generating: %w", err)
		}

		// 2. Run per-category aggregation queries.
		sales, err := opts.ReportQueries.AggregateSalesForEvent(ctx, eventID)
		if err != nil {
			return markFailed(ctx, opts, reportID, fmt.Errorf("aggregate sales: %w", err), logger)
		}

		complimentary, err := opts.ReportQueries.AggregateComplimentaryForEvent(ctx, eventID)
		if err != nil {
			return markFailed(ctx, opts, reportID, fmt.Errorf("aggregate complimentary: %w", err), logger)
		}

		refunds, err := opts.ReportQueries.AggregateRefundsForEvent(ctx, eventID)
		if err != nil {
			return markFailed(ctx, opts, reportID, fmt.Errorf("aggregate refunds: %w", err), logger)
		}

		scans, err := opts.ReportQueries.AggregateScansForEvent(ctx, eventID)
		if err != nil {
			return markFailed(ctx, opts, reportID, fmt.Errorf("aggregate scans: %w", err), logger)
		}

		// Derive commission and payout from the sales aggregation.
		// commissions = platform_fee + provider_fee = gross - net
		commissionAmt := sales.GrossAmount - sales.NetAmount
		commissions := gen.EventReportAggRow{
			Quantity:    sales.Quantity,
			GrossAmount: commissionAmt,
			NetAmount:   commissionAmt,
			Currency:    sales.Currency,
		}
		payouts := gen.EventReportAggRow{
			Quantity:    sales.Quantity,
			GrossAmount: sales.NetAmount,
			NetAmount:   sales.NetAmount,
			Currency:    sales.Currency,
		}

		// 3. Insert one line per category.
		lines := []struct {
			category string
			agg      gen.EventReportAggRow
		}{
			{"sales", sales},
			{"refunds", refunds},
			{"complimentary", complimentary},
			{"scans", scans},
			{"commissions", commissions},
			{"payouts", payouts},
		}

		for _, l := range lines {
			currency := l.agg.Currency
			if currency == "" {
				currency = "usd"
			}
			if _, err := opts.ReportQueries.InsertEventReportLine(
				ctx,
				reportID,
				l.category,
				l.agg.Quantity,
				l.agg.GrossAmount,
				l.agg.NetAmount,
				currency,
			); err != nil {
				return markFailed(ctx, opts, reportID, fmt.Errorf("insert line %q: %w", l.category, err), logger)
			}
		}

		// 4. Transition to 'ready' state.
		if _, err := opts.ReportQueries.UpdateEventReportState(ctx, reportID, "ready", nil); err != nil {
			return fmt.Errorf("reporting: transition to ready: %w", err)
		}

		// 5. Publish domain event to outbox.
		if opts.OutboxWriter != nil && opts.Pool != nil {
			eventPayload := map[string]any{
				"report_id": reportID.String(),
				"event_id":  eventID.String(),
				"org_id":    p.OrgID,
			}
			tx, txErr := opts.Pool.BeginTx(ctx, pgx.TxOptions{})
			if txErr == nil {
				committed := false
				defer func() {
					if !committed {
						tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
					}
				}()
				if pubErr := opts.OutboxWriter.Append(ctx, tx, outbox.Event{
					AggregateType: ReportAggregateType,
					AggregateID:   reportID.String(),
					EventType:     ReportEventType,
					Payload:       eventPayload,
				}); pubErr != nil {
					// Outbox publish failure is non-fatal — log and continue.
					logger.Warn("reporting: outbox append failed",
						"report_id", reportID,
						"error", pubErr,
					)
				} else if commitErr := tx.Commit(ctx); commitErr != nil {
					logger.Warn("reporting: outbox tx commit failed",
						"report_id", reportID,
						"error", commitErr,
					)
				} else {
					committed = true
				}
			} else {
				logger.Warn("reporting: outbox begin tx failed",
					"report_id", reportID,
					"error", txErr,
				)
			}
		}

		logger.Info("report generation completed",
			"report_id", reportID,
			"event_id", eventID,
			"sales_quantity", sales.Quantity,
			"sales_gross", sales.GrossAmount,
			"refunds_quantity", refunds.Quantity,
			"scans_quantity", scans.Quantity,
		)

		return nil
	}
}

// markFailed transitions the report to 'failed', stores the error message,
// and returns the original error so the worker retries / dead-letters the job.
func markFailed(
	ctx context.Context,
	opts HandlerOptions,
	reportID uuid.UUID,
	cause error,
	logger *slog.Logger,
) error {
	errMsg := cause.Error()
	if _, updateErr := opts.ReportQueries.UpdateEventReportState(
		ctx, reportID, "failed", &errMsg,
	); updateErr != nil {
		logger.Error("reporting: transition to failed after error also failed",
			"report_id", reportID,
			"original_error", cause,
			"update_error", updateErr,
		)
	}
	return cause
}

// ScheduleReportJob inserts a new event_reports row (state='pending') and
// enqueues an event.generate_report job in worker_jobs scheduled at
// eventEndAt + cutoff. The report row and the worker job are inserted in two
// separate statements — callers that require atomicity should wrap them in a
// transaction or use the outbox pattern.
//
// Returns the new report ID string on success.
func ScheduleReportJob(
	ctx context.Context,
	reportQueries *gen.Queries,
	pool *pgxpool.Pool,
	eventID uuid.UUID,
	orgID uuid.UUID,
	eventEndAt time.Time,
	cutoff time.Duration,
) (string, error) {
	windowStart := eventEndAt
	windowEnd := eventEndAt.Add(cutoff)

	report, err := reportQueries.InsertEventReport(ctx, eventID, orgID, &windowStart, &windowEnd)
	if err != nil {
		return "", fmt.Errorf("reporting: insert event_report: %w", err)
	}

	jobPayload := Payload{
		EventID:    eventID.String(),
		OrgID:      orgID.String(),
		ReportID:   report.ID.String(),
		CutoffTime: windowEnd.UTC().Format(time.RFC3339),
	}

	scheduledAt := windowEnd // Run the job only after the cutoff window closes.
	_ = scheduledAt          // Used by the worker INSERT below.

	if pool != nil {
		const insertJobSQL = `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, $2::jsonb, $3, 'pending', $4)
			RETURNING id::text
		`
		payloadBytes, _ := json.Marshal(jobPayload)
		var jobID string
		if err := pool.QueryRow(ctx, insertJobSQL,
			JobType, payloadBytes, 10, scheduledAt,
		).Scan(&jobID); err != nil {
			return report.ID.String(), fmt.Errorf("reporting: enqueue worker job: %w", err)
		}
		return report.ID.String(), nil
	}

	return report.ID.String(), nil
}
