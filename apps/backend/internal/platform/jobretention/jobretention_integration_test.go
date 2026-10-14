//go:build integration

// jobretention_integration_test.go — worker_jobs.cleanup against a live
// PostgreSQL: old finished rows go, recent ones and every pending or claimed
// row stay. The delete is table-wide (it is a retention job), so run it on a
// scratch database (the CI-Integration recipe in AGENTS.md), never on the
// shared dev stand.
package jobretention

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCleanup_Integration_DeletesOnlyOldFinishedRows(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	jobType := "jobretention.test." + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1`, jobType)
	})

	type row struct {
		name    string
		status  string
		ageDays int
		keep    bool
	}
	rows := []row{
		{"done_31d", "done", 31, false},
		{"done_400d", "done", 400, false},
		{"done_29d", "done", 29, true},
		{"done_1h", "done", 0, true},
		{"failed_91d", "failed", 91, false},
		{"failed_60d", "failed", 60, true},
		{"pending_400d", "pending", 400, true},
		{"claimed_400d", "claimed", 400, true},
	}
	ids := map[string]string{}
	for _, r := range rows {
		var id string
		// claimed_at = now() keeps the stale-claim reaper away from the
		// claimed row; the pending row gets a huge max_attempts so a generic
		// drain in a parallel package can only push it forward, never finish it.
		err := pool.QueryRow(ctx, `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at, created_at, claimed_at, claimed_by)
			VALUES ($1, jsonb_build_object('name', $2::text), 1000, $3,
			        now() - make_interval(days => $4) - interval '1 hour',
			        now() - make_interval(days => $4) - interval '1 hour',
			        CASE WHEN $3 = 'claimed' THEN now() END,
			        CASE WHEN $3 = 'claimed' THEN 'jobretention-test' END)
			RETURNING id::text`, jobType, r.name, r.status, r.ageDays).Scan(&id)
		if err != nil {
			t.Fatalf("insert %s: %v", r.name, err)
		}
		ids[r.name] = id
	}

	// BatchSize 1 proves the batching loop drains more than one row per status.
	sched := &fakeScheduler{}
	h := NewHandler(Options{Store: NewPGStore(pool), Scheduler: sched, BatchSize: 1, Logger: quiet()})
	if err := h(ctx, nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(sched.at) != 1 {
		t.Fatalf("scheduled %d next runs, want 1", len(sched.at))
	}

	for _, r := range rows {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE id = $1::uuid`, ids[r.name]).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", r.name, err)
		}
		if r.keep && n != 1 {
			t.Errorf("%s was deleted, want kept", r.name)
		}
		if !r.keep && n != 0 {
			t.Errorf("%s was kept, want deleted", r.name)
		}
	}

	// The production scheduler and the startup seed each leave exactly one
	// pending run.
	if _, err := pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, JobType); err != nil {
		t.Fatal(err)
	}
	if err := ScheduleInitialJob(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := ScheduleInitialJob(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var seeded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, JobType).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != 1 {
		t.Fatalf("ScheduleInitialJob twice left %d pending runs, want 1", seeded)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, JobType); err != nil {
		t.Fatal(err)
	}
}
