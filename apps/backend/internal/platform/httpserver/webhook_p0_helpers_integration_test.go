//go:build integration

// webhook_p0_helpers_integration_test.go — shared helpers of the P0-1 gate
// tests (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): the
// fulfilment footprint of one checkout and a runner for its
// checkout.issue_tickets jobs, built the way arena-worker builds them.
package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
)

// signStripeAt is signStripe with a chosen timestamp.
func signStripeAt(secret string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// fulfilment counts everything a paid checkout produces. Two equal values
// mean nothing was issued, mailed or announced twice.
type fulfilment struct {
	IntentEvents int // payment_intent_events rows of the payment
	IssueJobs    int // checkout.issue_tickets jobs
	ConvertJobs  int // checkout.convert_reservation jobs
	Tickets      int // tickets of the checkout
	Barcodes     int // platform barcodes of those tickets
	DeliveryJobs int // delivery_jobs rows (one letter slot per ticket)
	DeliverJobs  int // ticket.deliver worker jobs
	OrderPaid    int // v1.order.paid outbox rows of the order
}

func fulfilmentOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID uuid.UUID, providerID string) fulfilment {
	t.Helper()
	var f fulfilment
	cs := csID.String()
	err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM payment_intent_events WHERE provider_payment_id = $2),
		  (SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets'
		      AND payload->>'checkout_session_id' = $1),
		  (SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.convert_reservation'
		      AND payload->>'reservation_id' =
		          (SELECT reservation_id::text FROM checkout_sessions WHERE id = $1::uuid)),
		  (SELECT count(*) FROM tickets WHERE checkout_session_id = $1::uuid),
		  (SELECT count(*) FROM barcodes WHERE ticket_id IN
		      (SELECT id FROM tickets WHERE checkout_session_id = $1::uuid)),
		  (SELECT count(*) FROM delivery_jobs WHERE ticket_id IN
		      (SELECT id FROM tickets WHERE checkout_session_id = $1::uuid)),
		  (SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver'
		      AND payload->>'ticket_id' IN
		          (SELECT id::text FROM tickets WHERE checkout_session_id = $1::uuid)),
		  (SELECT count(*) FROM outbox_events WHERE event_type = 'v1.order.paid'
		      AND aggregate_id IN (SELECT id::text FROM orders WHERE checkout_session_id = $1::uuid))`,
		cs, providerID).Scan(&f.IntentEvents, &f.IssueJobs, &f.ConvertJobs, &f.Tickets,
		&f.Barcodes, &f.DeliveryJobs, &f.DeliverJobs, &f.OrderPaid)
	if err != nil {
		t.Fatalf("fulfilment footprint: %v", err)
	}
	return f
}

// issueJobHandler is the checkout.issue_tickets handler as arena-worker
// registers it (cmd/arena-worker/main.go), on this server's tickets handler.
func issueJobHandler(srv *Server) func(context.Context, []byte) error {
	return issuejob.NewHandler(issuejob.HandlerOptions{
		Queries:      gen.New(srv.pgxPool),
		IssueTickets: srv.ticketsHandler().IssueTicketsForCheckout,
	})
}

// runIssueJobs runs every checkout.issue_tickets job queued for the checkout,
// as the worker would, and returns how many it ran.
func runIssueJobs(t *testing.T, ctx context.Context, srv *Server, csID uuid.UUID) int {
	t.Helper()
	rows, err := srv.pgxPool.Query(ctx, `
		SELECT payload FROM worker_jobs
		 WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`, csID.String())
	if err != nil {
		t.Fatalf("list issue jobs: %v", err)
	}
	var payloads [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			t.Fatalf("scan issue job: %v", err)
		}
		payloads = append(payloads, p)
	}
	rows.Close()
	handle := issueJobHandler(srv)
	for _, p := range payloads {
		if err := handle(ctx, p); err != nil {
			t.Fatalf("checkout.issue_tickets: %v", err)
		}
	}
	return len(payloads)
}

// processed reads the "processed" flag of a webhook answer.
func processed(t *testing.T, body []byte) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode webhook answer: %v (body: %s)", err, body)
	}
	p, _ := m["processed"].(bool)
	return p
}

// startHostedCheckout starts a checkout for the fixture and returns its id,
// total and the provider's payment id stored on the payment intent.
func startHostedCheckout(t *testing.T, ctx context.Context, srv *Server, f *hostedOrgFixture) (uuid.UUID, int64, string) {
	t.Helper()
	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/p0")
	if code != 201 {
		t.Fatalf("checkout/start = %d; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode checkout/start: %v", err)
	}
	csID := uuid.MustParse(start.CheckoutSession.ID)
	q := gen.New(srv.pgxPool)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil {
		t.Fatalf("checkout session: %v", err)
	}
	intents, err := q.ListPaymentIntentsByCheckout(ctx, csID)
	if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		t.Fatalf("payment intent of the checkout: %v (%d rows)", err, len(intents))
	}
	return csID, *cs.Total, *intents[0].ProviderPaymentID
}
