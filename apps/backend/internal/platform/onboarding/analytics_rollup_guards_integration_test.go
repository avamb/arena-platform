//go:build integration

package onboarding

// Review guards of analytics.rollup (spec 50 stage A5): one job chain, one
// run at a time, no merge of two different CRM contacts, only confirmed
// applications link, dirty-people recomputation with a daily full pass, and
// session retention. Run on a FRESH scratch database, like the other
// TestAnalyticsRollup tests.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

func TestAnalyticsRollup_OneJobChainOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	clear := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, analytics.RollupJobType)
	}
	clear()
	t.Cleanup(clear)
	pending := func() int {
		return countRows(t, pool, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, analytics.RollupJobType)
	}

	sched := analytics.NewPGRollupScheduler(pool)
	for i := 0; i < 3; i++ {
		if err := sched.ScheduleNext(ctx, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := pending(); n != 1 {
		t.Fatalf("three ScheduleNext calls left %d pending jobs, want 1", n)
	}
	if err := analytics.ScheduleRollupJob(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if n := pending(); n != 1 {
		t.Fatalf("the startup insert forked the chain: %d pending jobs", n)
	}

	// A handler run (and a worker retry of it) queues nothing while one waits,
	// and returns nil.
	handler := analytics.NewRollupHandler(analytics.RollupHandlerOptions{
		Rollup:    analytics.NewRollup(analytics.RollupOptions{Pool: pool}),
		Scheduler: sched,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for i := 0; i < 2; i++ {
		if err := handler(ctx, nil); err != nil {
			t.Fatalf("handler run %d: %v", i, err)
		}
	}
	if n := pending(); n != 1 {
		t.Fatalf("after two handler runs there are %d pending jobs, want 1", n)
	}
}

func TestAnalyticsRollup_OneRunAtATimeAndDailyFullPass(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})

	// Another session holds the lock: the run is skipped, not queued behind.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, analytics.RollupLockKey); err != nil {
		t.Fatal(err)
	}
	res, err := roll.Run(ctx)
	if err != nil || !res.Skipped {
		t.Fatalf("run while the lock is held: %+v %v", res, err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, analytics.RollupLockKey); err != nil {
		t.Fatal(err)
	}
	conn.Release()

	// First run of the day is a full pass, the next one is not, and a person
	// with no event, no user and no e-mail is left alone until the next full pass.
	_, _ = pool.Exec(ctx, `DELETE FROM analytics.state WHERE key = 'rollup'`)
	res, err = roll.Run(ctx)
	if err != nil || res.Skipped || !res.FullPass {
		t.Fatalf("first run: %+v %v", res, err)
	}
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	bare := uuid.New()
	aid := "a_bare_" + nonceString()[8:]
	if _, err := pool.Exec(ctx, `INSERT INTO analytics.people (id) VALUES ($1)`, bare); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analytics.identities (kind, value, person_id, seen_at) VALUES ('aid', $1, $2, now())`, aid, bare); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE id = $1`, bare) })
	res, err = roll.Run(ctx)
	if err != nil || res.FullPass {
		t.Fatalf("second run of the day: %+v %v", res, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.profiles WHERE person_id = $1`, bare); n != 0 {
		t.Fatal("a quiet person was recomputed outside the daily full pass")
	}
	_, _ = pool.Exec(ctx, `UPDATE analytics.state SET value = value - 'full_day' WHERE key = 'rollup'`)
	res, err = roll.Run(ctx)
	if err != nil || !res.FullPass {
		t.Fatalf("full pass after the reset: %+v %v", res, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.profiles WHERE person_id = $1`, bare); n != 1 {
		t.Fatal("the full pass did not recompute everybody")
	}
}

func TestAnalyticsRollup_TwoCRMContactsAreNotMerged(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	cid1, cid2 := "c1"+nonce[len(nonce)-8:], "c2"+nonce[len(nonce)-8:]
	tw1, tw2 := "twenty-a-"+nonce, "twenty-b-"+nonce
	aid := "a_two_" + nonce[len(nonce)-12:]
	for cid, tw := range map[string]string{cid1: tw1, cid2: tw2} {
		if _, err := pool.Exec(ctx, `INSERT INTO analytics.contact_codes (code, twenty_person_id) VALUES ($1, $2)`, cid, tw); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.events WHERE id_value IN ($1, $2, $3)`, cid1, cid2, aid)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE twenty_person_id IN ($1, $2)`, tw1, tw2)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.contact_codes WHERE code IN ($1, $2)`, cid1, cid2)
	})
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})
	now := time.Now().UTC()
	send := func(events func(w *analytics.Writer)) {
		w := analytics.NewWriter(analytics.Options{DB: pool})
		events(w)
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := w.Close(fctx); err != nil {
			t.Fatal(err)
		}
	}
	send(func(w *analytics.Writer) {
		trackAt(w, now.Add(-5*time.Minute), cid1, analytics.IDKindCID, "email_click", map[string]any{"path": "/", "aid": aid}, true)
		trackAt(w, now.Add(-4*time.Minute), cid2, analytics.IDKindCID, "email_click", map[string]any{"path": "/"}, true)
	})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := personOf(t, pool, analytics.IDKindCID, cid1), personOf(t, pool, analytics.IDKindCID, cid2)
	if a == b {
		t.Fatal("two contacts started as one person")
	}

	// The second contact now "arrives" with the first one's visitor id.
	send(func(w *analytics.Writer) {
		trackAt(w, now.Add(-time.Minute), cid2, analytics.IDKindCID, "email_click", map[string]any{"path": "/", "aid": aid}, true)
	})
	res, err := roll.Run(ctx)
	if err != nil {
		t.Fatalf("run must not fail on a refused link: %v", err)
	}
	if res.MergeConflicts < 1 {
		t.Errorf("merge conflicts = %d, want at least 1", res.MergeConflicts)
	}
	if got := personOf(t, pool, analytics.IDKindCID, cid1); got != a {
		t.Error("the first contact moved")
	}
	if got := personOf(t, pool, analytics.IDKindCID, cid2); got != b {
		t.Error("the second contact was merged into another person")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.people WHERE id IN ($1, $2)`, a, b); n != 2 {
		t.Errorf("people left = %d, want both", n)
	}
}

func TestAnalyticsRollup_UnconfirmedApplicationDoesNotLink(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	aid := "a_unc_" + nonce[len(nonce)-12:]
	email := fmt.Sprintf("rollup-unconfirmed-%s@example.com", nonce)
	cleanupApplication(t, pool, email)
	svc := New(Options{Pool: pool, Notifier: &captureNotifier{}})
	if _, err := svc.Start(ctx, StartInput{FirstName: "Un", LastName: "Confirmed", Email: email, Phone: "+34 600 111 222", AID: aid}); err != nil {
		t.Fatal(err)
	}
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.identities WHERE kind = 'email_sha256' AND value = $1`, analytics.EmailSHA256(email)); n != 0 {
		t.Error("an application whose e-mail was never confirmed was linked")
	}
}

func TestAnalyticsRollup_SessionsOutliveRawEvents(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	aid := "a_ret_" + nonce[len(nonce)-12:]
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.events WHERE id_value = $1`, aid)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE id IN (SELECT person_id FROM analytics.identities WHERE value = $1)`, aid)
	})
	w := analytics.NewWriter(analytics.Options{DB: pool})
	now := time.Now().UTC()
	trackAt(w, now.Add(-10*time.Minute), aid, analytics.IDKindAID, "page_view", map[string]any{"path": "/"}, false)
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.Close(fctx); err != nil {
		t.Fatal(err)
	}
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	person := personOf(t, pool, analytics.IDKindAID, aid)

	// A session from before the oldest raw event still kept is the only record
	// of that visit; one inside the event range that no event backs is stale.
	old, stale := uuid.New(), uuid.New()
	for id, started := range map[uuid.UUID]time.Time{old: now.AddDate(0, -20, 0), stale: now.Add(-time.Minute)} {
		if _, err := pool.Exec(ctx, `
INSERT INTO analytics.sessions (id, person_id, surface, started_at, ended_at, pages, engaged_seconds)
VALUES ($1, $2, 'site', $3, $3, 1, 0)`, id, person, started); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = pool.Exec(ctx, `DELETE FROM analytics.state WHERE key = 'rollup'`)
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.sessions WHERE id = $1`, old); n != 1 {
		t.Error("a session older than the oldest raw event was deleted")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.sessions WHERE id = $1`, stale); n != 0 {
		t.Error("a session no event backs was kept")
	}
}
