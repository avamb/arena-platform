package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// GenerateJobType is the worker_jobs.job_type of one generator attempt of an
// intake (spec 64 section 4.4). arena-api enqueues attempt 1 in the create
// transaction; the worker handler (venueplan/generate) enqueues the next
// attempts itself, as NEW rows with a later scheduled_at.
const GenerateJobType = "venue_plan.generate"

// GenerateJobAttempts is max_attempts of one venue_plan.generate row: how
// often the worker re-runs the SAME attempt after the handler returned an
// error (a database failure). Generator failures never return an error; they
// are counted in Payload.Attempt.
const GenerateJobAttempts = 5

// GeneratePayload is the payload of a venue_plan.generate job.
type GeneratePayload struct {
	IntakeID string `json:"intake_id"`
	Attempt  int    `json:"attempt"`
}

// EnqueueGenerate writes one venue_plan.generate job on db (the caller's
// transaction), to run after delay.
func EnqueueGenerate(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, intakeID uuid.UUID, attempt int, delay time.Duration) error {
	if attempt < 1 {
		attempt = 1
	}
	p := GeneratePayload{IntakeID: intakeID.String(), Attempt: attempt}
	if delay <= 0 {
		if _, err := worker.EnqueueInTx(ctx, db, GenerateJobType, p, GenerateJobAttempts); err != nil {
			return fmt.Errorf("venue plan generate job: %w", err)
		}
		return nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("venue plan generate job: %w", err)
	}
	var id string
	if err := db.QueryRow(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now() + make_interval(secs => $4))
		RETURNING id::text`,
		GenerateJobType, body, GenerateJobAttempts, delay.Seconds()).Scan(&id); err != nil {
		return fmt.Errorf("venue plan generate job: enqueue: %w", err)
	}
	return nil
}
