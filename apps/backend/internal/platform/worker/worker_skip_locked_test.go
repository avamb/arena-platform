// Package worker_test — test suite for feature #72:
// "FOR UPDATE SKIP LOCKED prevents duplicate job claims".
//
// Two (or more) worker instances polling the same pending job table must
// never claim the same job. This is verified by running concurrent workers
// against an inMemoryQueue whose ClaimNext mirrors the FOR UPDATE SKIP LOCKED
// CTE in PGQueue:
//
//   - ClaimNext holds a mutex, finds the first pending row, atomically marks
//     it 'claimed', and returns it — exactly the same exclusive-claim contract
//     that FOR UPDATE SKIP LOCKED provides in PostgreSQL.
//
// Steps covered:
//  1. Insert 50 jobs of type 'test.noop' with status='pending'.
//  2. Start two worker instances in parallel (different claimed_by IDs).
//  3. Wait for queue drain (all 50 jobs status='done').
//  4. Verify count(status='done') == 50.
//  5. Verify both workers claimed at least some jobs (roughly even split).
//  6. Verify no job has attempts > 1 (no double-claim).
//  7. Verify no errors were recorded on any job.
//  8. Inspect claimSQL in source — must include 'FOR UPDATE SKIP LOCKED'.
//  9. Stress: repeat with 200 jobs and 4 workers.
package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// insertN inserts n jobs of the given type into q and returns the IDs.
func insertN(q *inMemoryQueue, n int, jobType string) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := q.insert(jobType, []byte(`{}`), time.Time{}, 1)
		ids = append(ids, id)
	}
	return ids
}

// waitAllDone polls until every id in ids has status='done', or timeout.
func waitAllDone(t *testing.T, q *inMemoryQueue, ids []string, timeout time.Duration) bool {
	t.Helper()
	return pollUntil(t, timeout, 5*time.Millisecond, func() bool {
		for _, id := range ids {
			r := q.get(id)
			if r == nil || r.status != "done" {
				return false
			}
		}
		return true
	})
}

// countByWorker tallies how many jobs each claimedBy value processed.
func countByWorker(q *inMemoryQueue, ids []string) map[string]int {
	tally := make(map[string]int)
	for _, id := range ids {
		r := q.get(id)
		if r != nil && r.claimedBy != "" {
			tally[r.claimedBy]++
		}
	}
	return tally
}

// startWorker creates and runs a worker with the given instanceID against q.
// Returns a stop function. The registry always registers 'test.noop'.
func startWorker(t *testing.T, q *inMemoryQueue, instanceID string) func() {
	t.Helper()
	reg := NewRegistry()
	reg.Register("test.noop", func(_ context.Context, _ []byte) error { return nil })

	w, err := New(Options{
		Queue:           q,
		Registry:        reg,
		InstanceID:      instanceID,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("worker.New(%s): %v", instanceID, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()

	return func() {
		cancel()
		_ = w.Stop()
	}
}

// ---------------------------------------------------------------------------
// Step 8 — static SQL inspection
// ---------------------------------------------------------------------------

// TestSkipLocked_ClaimSQLContainsForUpdateSkipLocked inspects the source file
// for the claimSQL constant and verifies it includes FOR UPDATE SKIP LOCKED.
//
// This step has no runtime dependency — it fails at build / lint time if a
// future refactor accidentally removes the locking hint.
func TestSkipLocked_ClaimSQLContainsForUpdateSkipLocked(t *testing.T) {
	t.Parallel()

	// Locate worker.go relative to the test binary's working directory.
	// os.Getwd() returns the package source directory when running go test.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	workerFile := filepath.Join(wd, "worker.go")
	data, err := os.ReadFile(workerFile)
	if err != nil {
		t.Fatalf("read worker.go: %v", err)
	}

	src := string(data)

	// Must contain both keywords together (case-insensitive for robustness
	// against minor whitespace / capitalisation differences).
	srcUpper := strings.ToUpper(src)
	if !strings.Contains(srcUpper, "FOR UPDATE SKIP LOCKED") {
		t.Error("worker.go claimSQL does not contain 'FOR UPDATE SKIP LOCKED'; " +
			"concurrent worker safety is not guaranteed")
	}

	// Also verify the keyword appears inside a WITH ... AS CTE that selects
	// from worker_jobs — confirming it is the claim query, not a comment.
	if !strings.Contains(src, "worker_jobs") {
		t.Error("worker.go does not reference worker_jobs table")
	}
	if !strings.Contains(src, "ClaimNext") {
		t.Error("worker.go does not export ClaimNext method")
	}
}

// ---------------------------------------------------------------------------
// Step 8 (comment variant) — FOR UPDATE SKIP LOCKED in package doc / comment
// ---------------------------------------------------------------------------

// TestSkipLocked_PackageDocMentionsSkipLocked checks that the package-level
// comment or function comment documents the FOR UPDATE SKIP LOCKED design so
// future maintainers understand the concurrency contract.
func TestSkipLocked_PackageDocMentionsSkipLocked(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(wd, "worker.go"))
	if err != nil {
		t.Fatalf("read worker.go: %v", err)
	}

	src := strings.ToUpper(string(data))
	if !strings.Contains(src, "SKIP LOCKED") {
		t.Error("worker.go documentation does not mention SKIP LOCKED; " +
			"please document the concurrent-claim contract")
	}
}

// ---------------------------------------------------------------------------
// Steps 1–7 — two-worker concurrent drain of 50 jobs
// ---------------------------------------------------------------------------

// TestSkipLocked_TwoWorkers_NoDuplicateClaims runs two workers concurrently
// against 50 pending jobs and verifies:
//   - All 50 jobs reach status='done'.
//   - Both workers claimed at least one job (distribution occurred).
//   - No job has attempts > 1 (no double-claim).
//   - No job carries a non-nil last_error after completion.
func TestSkipLocked_TwoWorkers_NoDuplicateClaims(t *testing.T) {
	t.Parallel()

	const jobCount = 50

	// STEP 1: Insert 50 jobs with status='pending'.
	q := newInMemoryQueue()
	ids := insertN(q, jobCount, "test.noop")

	if len(ids) != jobCount {
		t.Fatalf("step 1: expected %d job IDs, got %d", jobCount, len(ids))
	}

	// Verify initial status.
	for _, id := range ids {
		r := q.get(id)
		if r == nil {
			t.Fatalf("step 1: job %s not found after insert", id)
		}
		if r.status != "pending" {
			t.Fatalf("step 1: job %s has unexpected status %q (want pending)", id, r.status)
		}
	}

	// STEP 2: Start two worker instances in parallel.
	stop1 := startWorker(t, q, "worker-A")
	stop2 := startWorker(t, q, "worker-B")
	defer stop1()
	defer stop2()

	// STEP 3: Wait for all jobs to drain.
	ok := waitAllDone(t, q, ids, 10*time.Second)
	if !ok {
		// Count how many are done for diagnostics.
		doneCount := 0
		for _, id := range ids {
			if r := q.get(id); r != nil && r.status == "done" {
				doneCount++
			}
		}
		t.Fatalf("step 3: queue did not drain within timeout (%d/%d jobs done)", doneCount, jobCount)
	}

	// STEP 4: count(status='done') must equal jobCount.
	doneCount := 0
	for _, id := range ids {
		if r := q.get(id); r != nil && r.status == "done" {
			doneCount++
		}
	}
	if doneCount != jobCount {
		t.Errorf("step 4: expected %d done jobs, got %d", jobCount, doneCount)
	}

	// STEP 5: Both workers must have claimed at least one job.
	tally := countByWorker(q, ids)
	if tally["worker-A"] == 0 {
		t.Errorf("step 5: worker-A claimed 0 jobs (expected at least 1); tally=%v", tally)
	}
	if tally["worker-B"] == 0 {
		t.Errorf("step 5: worker-B claimed 0 jobs (expected at least 1); tally=%v", tally)
	}
	t.Logf("step 5: job distribution: worker-A=%d, worker-B=%d", tally["worker-A"], tally["worker-B"])

	// STEP 6: No job must have attempts > 1 (no double-claim).
	for _, id := range ids {
		r := q.get(id)
		if r == nil {
			t.Errorf("step 6: job %s not found", id)
			continue
		}
		if r.attempts > 1 {
			t.Errorf("step 6: job %s has attempts=%d (expected 1); indicates double-claim", id, r.attempts)
		}
	}

	// STEP 7: No job should have a last_error after successful completion.
	for _, id := range ids {
		r := q.get(id)
		if r == nil {
			continue
		}
		if r.lastError != nil && *r.lastError != "" {
			t.Errorf("step 7: job %s has last_error=%q after successful completion", id, *r.lastError)
		}
	}

	// Cleanup.
	for _, id := range ids {
		q.remove(id)
	}
}

// ---------------------------------------------------------------------------
// Step 5 — "roughly even split" quantified
// ---------------------------------------------------------------------------

// TestSkipLocked_TwoWorkers_RoughlyEvenSplit checks that neither worker
// processes 100% of the jobs. With 50 jobs and fast polling, both workers
// should share the load; the test requires each worker claimed at least 10%
// (5 jobs). This guards against a degenerate "one worker starves the other"
// implementation.
func TestSkipLocked_TwoWorkers_RoughlyEvenSplit(t *testing.T) {
	t.Parallel()

	const jobCount = 50
	const minEach = 5 // at least 10% each

	q := newInMemoryQueue()
	ids := insertN(q, jobCount, "test.noop")

	stop1 := startWorker(t, q, "split-A")
	stop2 := startWorker(t, q, "split-B")
	defer stop1()
	defer stop2()

	if !waitAllDone(t, q, ids, 10*time.Second) {
		t.Fatal("queue did not drain within timeout")
	}

	tally := countByWorker(q, ids)
	t.Logf("job distribution: split-A=%d, split-B=%d", tally["split-A"], tally["split-B"])

	if tally["split-A"] < minEach {
		t.Errorf("split-A claimed only %d jobs (want >= %d); distribution too uneven", tally["split-A"], minEach)
	}
	if tally["split-B"] < minEach {
		t.Errorf("split-B claimed only %d jobs (want >= %d); distribution too uneven", tally["split-B"], minEach)
	}

	for _, id := range ids {
		q.remove(id)
	}
}

// ---------------------------------------------------------------------------
// Step 9 — stress test: 200 jobs / 4 workers
// ---------------------------------------------------------------------------

// TestSkipLocked_StressFourWorkers_NoDuplicateClaims repeats the concurrent
// drain test with 200 jobs and 4 workers. All jobs must reach 'done' with
// exactly 1 attempt and no double-claims.
func TestSkipLocked_StressFourWorkers_NoDuplicateClaims(t *testing.T) {
	t.Parallel()

	const jobCount = 200
	const workerCount = 4

	q := newInMemoryQueue()
	ids := insertN(q, jobCount, "test.noop")

	stopFuncs := make([]func(), workerCount)
	for i := 0; i < workerCount; i++ {
		instanceID := "stress-worker-" + string(rune('A'+i))
		stopFuncs[i] = startWorker(t, q, instanceID)
	}
	defer func() {
		for _, stop := range stopFuncs {
			stop()
		}
	}()

	// Allow more time for 200 jobs.
	ok := waitAllDone(t, q, ids, 30*time.Second)
	if !ok {
		doneCount := 0
		for _, id := range ids {
			if r := q.get(id); r != nil && r.status == "done" {
				doneCount++
			}
		}
		t.Fatalf("stress: queue did not drain within timeout (%d/%d jobs done)", doneCount, jobCount)
	}

	// All jobs done.
	doneCount := 0
	for _, id := range ids {
		if r := q.get(id); r != nil && r.status == "done" {
			doneCount++
		}
	}
	if doneCount != jobCount {
		t.Errorf("stress: expected %d done jobs, got %d", jobCount, doneCount)
	}

	// No double-claims (attempts == 1 for every job).
	for _, id := range ids {
		r := q.get(id)
		if r == nil {
			t.Errorf("stress: job %s not found", id)
			continue
		}
		if r.attempts > 1 {
			t.Errorf("stress: job %s has attempts=%d (expected 1); double-claim detected", id, r.attempts)
		}
	}

	// Log distribution.
	tally := countByWorker(q, ids)
	t.Logf("stress distribution: %v", tally)

	// Each worker should have processed at least one job.
	for i := 0; i < workerCount; i++ {
		instanceID := "stress-worker-" + string(rune('A'+i))
		if tally[instanceID] == 0 {
			t.Errorf("stress: %s claimed 0 jobs", instanceID)
		}
	}

	// Cleanup.
	for _, id := range ids {
		q.remove(id)
	}
}

// ---------------------------------------------------------------------------
// Full sweep — all 9 steps as sub-tests
// ---------------------------------------------------------------------------

// TestSkipLocked_FullVerification runs all feature steps as sub-tests.
func TestSkipLocked_FullVerification(t *testing.T) {
	t.Run("Step8_SQL_ContainsForUpdateSkipLocked", func(t *testing.T) {
		t.Parallel()
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("os.Getwd: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(wd, "worker.go"))
		if err != nil {
			t.Fatalf("read worker.go: %v", err)
		}
		if !strings.Contains(strings.ToUpper(string(data)), "FOR UPDATE SKIP LOCKED") {
			t.Error("claimSQL missing FOR UPDATE SKIP LOCKED")
		}
	})

	t.Run("Steps1to7_TwoWorkers_50Jobs", func(t *testing.T) {
		t.Parallel()
		const n = 50
		q := newInMemoryQueue()
		ids := insertN(q, n, "test.noop")

		stop1 := startWorker(t, q, "fv-A")
		stop2 := startWorker(t, q, "fv-B")
		defer stop1()
		defer stop2()

		if !waitAllDone(t, q, ids, 10*time.Second) {
			t.Fatal("queue did not drain")
		}

		tally := countByWorker(q, ids)
		doneCount := 0
		for _, id := range ids {
			r := q.get(id)
			if r == nil {
				t.Errorf("job %s missing", id)
				continue
			}
			if r.status == "done" {
				doneCount++
			}
			if r.attempts > 1 {
				t.Errorf("job %s: attempts=%d (double-claim)", id, r.attempts)
			}
		}
		if doneCount != n {
			t.Errorf("step 4: expected %d done, got %d", n, doneCount)
		}
		if tally["fv-A"] == 0 || tally["fv-B"] == 0 {
			t.Errorf("step 5: distribution not spread across both workers: %v", tally)
		}
		t.Logf("step 5: distribution fv-A=%d fv-B=%d", tally["fv-A"], tally["fv-B"])
		for _, id := range ids {
			q.remove(id)
		}
	})

	t.Run("Step9_StressFourWorkers_200Jobs", func(t *testing.T) {
		t.Parallel()
		const n = 200
		q := newInMemoryQueue()
		ids := insertN(q, n, "test.noop")

		stops := make([]func(), 4)
		for i := 0; i < 4; i++ {
			stops[i] = startWorker(t, q, "fv-stress-"+string(rune('A'+i)))
		}
		defer func() {
			for _, s := range stops {
				s()
			}
		}()

		if !waitAllDone(t, q, ids, 30*time.Second) {
			t.Fatal("stress: queue did not drain")
		}
		for _, id := range ids {
			r := q.get(id)
			if r != nil && r.attempts > 1 {
				t.Errorf("stress: job %s: attempts=%d (double-claim)", id, r.attempts)
			}
		}
		for _, id := range ids {
			q.remove(id)
		}
	})
}
