//go:build integration

// issue_convert_jobs_p0_integration_test.go — ASY-05 of the P0 gates
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): the two jobs a
// paid webhook queues, checkout.issue_tickets (platform/issuejob) and
// checkout.convert_reservation (platform/convertjob), run through the
// handlers arena-worker registers. Each is made to fail in the middle of its
// transaction, then run concurrently and again: the result is exactly one
// set of tickets, barcodes, letters and one held->sold conversion.
//
// The failure is a real one: a temporary trigger raises an exception on a
// row of THIS checkout only, so the job's own transaction aborts after some
// of its statements ran. The trigger is dropped before the test goes on.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/convertjob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
)

// failingTrigger installs a BEFORE trigger on table that raises for rows
// matching cond, and returns its removal.
func failingTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, timing, cond string) func() {
	t.Helper()
	name := "p0_fail_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	stmts := []string{
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fn$
			BEGIN
				IF %s THEN RAISE EXCEPTION 'p0 injected failure'; END IF;
				RETURN NEW;
			END $fn$`, name, cond),
		fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, name, timing, table, name),
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("install failing trigger: %v", err)
		}
	}
	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		bg := context.Background()
		if _, err := pool.Exec(bg, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table)); err != nil {
			t.Errorf("drop trigger: %v", err)
		}
		if _, err := pool.Exec(bg, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name)); err != nil {
			t.Errorf("drop trigger function: %v", err)
		}
	}
	t.Cleanup(drop)
	return drop
}

// paidCheckoutAwaitingJobs is a Stripe checkout of two GA tickets whose
// webhook was delivered: both jobs are queued, neither has run.
func paidCheckoutAwaitingJobs(t *testing.T, ctx context.Context, label string) (*hostedOrgFixture, *Server, uuid.UUID, string) {
	t.Helper()
	pool := integrationPool(t)
	f := newHostedOrgFixture(t, ctx, pool, label)
	t.Cleanup(f.cleanup)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	csID, _, providerID := startHostedCheckout(t, ctx, srv, f)
	paid := sessionCompletedEvent(providerID, "pi_"+label+"_"+uuid.NewString()[:8], "paid")
	if code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(), paid, f.webhookSecret); code != 200 || !processed(t, body) {
		t.Fatalf("paid event = %d; body: %s", code, body)
	}
	return f, srv, csID, providerID
}

func jobPayload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobType, key, value string) []byte {
	t.Helper()
	var p []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>$2 = $3`,
		jobType, key, value).Scan(&p); err != nil {
		t.Fatalf("%s job: %v", jobType, err)
	}
	return p
}

func runConcurrently(n int, fn func() error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn()
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// TestASY05_IssueTicketsJob_FailureConcurrencyAndRepeats — checkout.issue_tickets.
func TestASY05_IssueTicketsJob_FailureConcurrencyAndRepeats(t *testing.T) {
	ctx := t.Context()
	_, srv, csID, providerID := paidCheckoutAwaitingJobs(t, ctx, "p0iss")
	pool := srv.pgxPool
	payload := jobPayload(t, ctx, pool, issuejob.JobType, "checkout_session_id", csID.String())
	handle := issueJobHandler(srv)
	queued := fulfilmentOf(t, ctx, pool, csID, providerID)

	// 1. The job fails on its second ticket, inside its own transaction:
	//    nothing of the first ticket may survive.
	drop := failingTrigger(t, ctx, pool, "tickets", "BEFORE INSERT",
		fmt.Sprintf("NEW.checkout_session_id = '%s'::uuid AND NEW.ordinal = 1", csID))
	if err := handle(ctx, payload); err == nil {
		t.Fatal("the job did not fail with the injected failure")
	}
	drop()
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != queued {
		t.Fatalf("a failed run left rows behind: queued %+v, after %+v", queued, after)
	}

	// 2. The retry races three more copies of itself (a stale claim taken
	//    over by a second worker).
	for i, err := range runConcurrently(4, func() error { return handle(ctx, payload) }) {
		if err != nil {
			t.Errorf("concurrent run %d: %v", i, err)
		}
	}
	issued := fulfilmentOf(t, ctx, pool, csID, providerID)
	want := queued
	want.Tickets, want.Barcodes, want.DeliveryJobs, want.DeliverJobs, want.OrderPaid = 2, 2, 2, 2, 1
	if issued != want {
		t.Fatalf("after the concurrent runs: %+v, want %+v", issued, want)
	}

	// 3. The job is run again after it succeeded (the worker died before it
	//    marked the job done).
	for i := 0; i < 2; i++ {
		if err := handle(ctx, payload); err != nil {
			t.Fatalf("repeat %d: %v", i+1, err)
		}
	}
	if after := fulfilmentOf(t, ctx, pool, csID, providerID); after != issued {
		t.Fatalf("repeats changed the result: %+v -> %+v", issued, after)
	}
	var ordinals string
	if err := pool.QueryRow(ctx, `SELECT string_agg(ordinal::text, ',' ORDER BY ordinal) FROM tickets WHERE checkout_session_id = $1`, csID).Scan(&ordinals); err != nil {
		t.Fatal(err)
	}
	if ordinals != "0,1" {
		t.Errorf("ticket ordinals = %q, want 0,1", ordinals)
	}
}

type conversion struct {
	State      string
	Held, Sold int64
	SeatsHeld  int
	SeatsSold  int
}

func conversionOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID, sessionID uuid.UUID) conversion {
	t.Helper()
	var c conversion
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT state FROM reservations WHERE id = $1),
		       (SELECT COALESCE(sum(capacity_held), 0) FROM inventory_ledger WHERE session_id = $2),
		       (SELECT COALESCE(sum(capacity_sold), 0) FROM inventory_ledger WHERE session_id = $2),
		       (SELECT count(*) FROM session_seats WHERE session_id = $2 AND status = 'held'),
		       (SELECT count(*) FROM session_seats WHERE session_id = $2 AND status = 'sold')`,
		reservationID, sessionID).Scan(&c.State, &c.Held, &c.Sold, &c.SeatsHeld, &c.SeatsSold); err != nil {
		t.Fatalf("conversion state: %v", err)
	}
	return c
}

// TestASY05_ConvertReservationJob_FailureConcurrencyAndRepeats —
// checkout.convert_reservation. The paid webhook converts the reservation on
// its own transaction and queues this job as the backstop, so the job is
// first run on a hold the webhook has not reached yet (a backstop that runs
// alone), then the webhook arrives, then the queued job runs again.
func TestASY05_ConvertReservationJob_FailureConcurrencyAndRepeats(t *testing.T) {
	ctx := t.Context()
	pool := integrationPool(t)
	f := newHostedOrgFixture(t, ctx, pool, "p0cnv")
	t.Cleanup(f.cleanup)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	csID, _, providerID := startHostedCheckout(t, ctx, srv, f)
	cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(convertjob.Payload{ReservationID: cs.ReservationID.String()})
	handle := convertjob.NewHandler(convertjob.HandlerOptions{ConvertFn: srv.checkoutHandler().ConvertReservationTx})

	held := conversionOf(t, ctx, pool, cs.ReservationID, f.sessionID)
	if held.State == "converted" || held.SeatsHeld != 2 || held.SeatsSold != 0 {
		t.Fatalf("before the job: %+v; want two held places of an unconverted reservation", held)
	}

	// 1. The job fails at its last statement (the reservation state):
	//    the places it already marked sold must roll back with it.
	drop := failingTrigger(t, ctx, pool, "reservations", "BEFORE UPDATE",
		fmt.Sprintf("NEW.id = '%s'::uuid AND NEW.state = 'converted'", cs.ReservationID))
	if err := handle(ctx, payload); err == nil {
		t.Fatal("the job did not fail with the injected failure")
	}
	drop()
	if after := conversionOf(t, ctx, pool, cs.ReservationID, f.sessionID); after != held {
		t.Fatalf("a failed run left a half conversion: %+v -> %+v", held, after)
	}

	// 2. Four copies at once.
	for i, err := range runConcurrently(4, func() error { return handle(ctx, payload) }) {
		if err != nil {
			t.Errorf("concurrent run %d: %v", i, err)
		}
	}
	converted := conversionOf(t, ctx, pool, cs.ReservationID, f.sessionID)
	want := conversion{State: "converted", Held: held.Held - 2, Sold: held.Sold + 2, SeatsHeld: 0, SeatsSold: 2}
	if converted != want {
		t.Fatalf("after the concurrent runs: %+v, want %+v (from %+v)", converted, want, held)
	}

	// 3. And again after it succeeded.
	for i := 0; i < 2; i++ {
		if err := handle(ctx, payload); err != nil {
			t.Fatalf("repeat %d: %v", i+1, err)
		}
	}
	if after := conversionOf(t, ctx, pool, cs.ReservationID, f.sessionID); after != converted {
		t.Fatalf("repeats changed the conversion: %+v -> %+v", converted, after)
	}

	// 4. The paid webhook arrives for the converted hold and queues the job
	//    once more; neither sells the places a second time.
	paid := sessionCompletedEvent(providerID, "pi_p0cnv_"+uuid.NewString()[:8], "paid")
	if code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(), paid, f.webhookSecret); code != 200 || !processed(t, body) {
		t.Fatalf("paid event = %d; body: %s", code, body)
	}
	queued := jobPayload(t, ctx, pool, convertjob.JobType, "reservation_id", cs.ReservationID.String())
	if err := handle(ctx, queued); err != nil {
		t.Fatalf("queued job: %v", err)
	}
	if after := conversionOf(t, ctx, pool, cs.ReservationID, f.sessionID); after != converted {
		t.Fatalf("the webhook and its job sold the places again: %+v -> %+v", converted, after)
	}
}
