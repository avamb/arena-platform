//go:build integration

// dead_letters_integration_test.go is the integration test for the dead
// letter check family (worker_dead_letter sub-check): a new dead-lettered
// job must be reported exactly once, never replayed, and never duplicated
// on a subsequent run.
package opswatchdog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

func TestDeadLetters_LiveDB_WorkerDeadLetterReportedOnce(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()

	notifier := &recordingNotifier{}
	handler := opswatchdog.NewHandler(opswatchdog.Options{
		Pool:     pool,
		Notifier: notifier,
		Logger:   quietTestLogger(),
	})

	// Prime every cursor (including dead_letter_worker_jobs) to "now" BEFORE
	// seeding our own row — this is the same "never replay history" rule the
	// sales-feed test exercises explicitly. Without this priming run, a row
	// inserted before the watchdog's very first invocation in this process
	// would itself count as "pre-existing history" and be correctly (but,
	// for this test, inconveniently) skipped.
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("priming run: %v", err)
	}

	jobType := "watchdog_test.marker." + uuid.NewString()[:8]
	originalJobID := uuid.New()

	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_dead_letter
		    (original_job_id, job_type, payload, attempts, last_error, original_created_at)
		VALUES ($1, $2, '{}', 10, 'boom: buyer test-email@example.com failed', now())`,
		originalJobID, jobType,
	); err != nil {
		t.Fatalf("seed worker_dead_letter row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_dead_letter WHERE job_type = $1`, jobType)
	})

	// ---- Run 1: exactly one HIGH message for this job_type -----------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	msgs := notifier.messages()
	if got := countContaining(msgs, jobType); got != 1 {
		t.Fatalf("run 1: messages mentioning %q = %d, want 1\nall:\n%s", jobType, got, strings.Join(msgs, "\n---\n"))
	}
	msg := oneContaining(msgs, jobType)
	if !strings.Contains(msg, "HIGH") {
		t.Errorf("run 1: expected HIGH severity, got: %s", msg)
	}
	if !strings.Contains(msg, "dead letter") {
		t.Errorf("run 1: expected 'dead letter' text, got: %s", msg)
	}
	// The seeded last_error embeds an email address — it must never reach
	// the message unscrubbed (no PII rule).
	if strings.Contains(msg, "test-email@example.com") {
		t.Errorf("run 1: message leaked an unscrubbed email address: %s", msg)
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Errorf("run 1: expected the scrubbed [redacted] marker in place of the email, got: %s", msg)
	}

	// ---- Run 2: cursor advanced past this row, must not repeat -------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	msgs = notifier.messages()
	if got := countContaining(msgs, jobType); got != 1 {
		t.Fatalf("run 2: messages mentioning %q = %d, want still 1 (no replay)\nall:\n%s", jobType, got, strings.Join(msgs, "\n---\n"))
	}
}
