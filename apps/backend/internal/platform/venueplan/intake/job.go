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

// GeneratePayload is the payload of a venue_plan.generate job. One row is one
// short step of an attempt: without GenJobID it is the step "submit" (send
// the files), with GenJobID the step "poll" (ask the generator once about
// that job).
type GeneratePayload struct {
	IntakeID string `json:"intake_id"`
	Attempt  int    `json:"attempt"`
	// Seq numbers the rows of one intake's chain: every step enqueues the
	// next row with Seq+1. A row whose intake already has a row with a
	// higher Seq is a repeat of a step that was done, and does nothing.
	Seq int `json:"seq,omitempty"`
	// GenJobID is the generator's job id (the step poll).
	GenJobID string `json:"gen_job_id,omitempty"`
	// SubmittedAt is when the files were sent; the poll gives up after the
	// deadline counted from it.
	SubmittedAt *time.Time `json:"submitted_at,omitempty"`
	// Resubmitted: the generator lost the job once and the files were sent
	// again; a second loss is a failed attempt.
	Resubmitted bool `json:"resubmitted,omitempty"`
}

// EnqueueGenerate writes one venue_plan.generate job on db (the caller's
// transaction), to run after delay.
func EnqueueGenerate(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, intakeID uuid.UUID, attempt int, delay time.Duration) error {
	return EnqueueGenerateStep(ctx, db, GeneratePayload{IntakeID: intakeID.String(), Attempt: attempt}, delay)
}

// EnqueueGenerateStep writes one venue_plan.generate row with payload p on db
// (the caller's transaction), to run after delay.
func EnqueueGenerateStep(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, p GeneratePayload, delay time.Duration) error {
	if p.Attempt < 1 {
		p.Attempt = 1
	}
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
