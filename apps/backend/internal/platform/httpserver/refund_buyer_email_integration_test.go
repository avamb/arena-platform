//go:build integration

// refund_buyer_email_integration_test.go — PAY-07: the letter to a buyer whose
// money went back. Real router and refund engine, the real
// refund.buyer_email handler, a recording mail sender.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// recordingSender is a production-looking sender that keeps what it is given.
type recordingSender struct {
	mu   sync.Mutex
	msgs []email.Message
	fail error
}

func (s *recordingSender) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *recordingSender) sent() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]email.Message(nil), s.msgs...)
}

func TestRefundBuyerEmail_OneLetterPerOperation(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay07-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY07")
	ctx := context.Background()
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_`+uuid.NewString()[:8]+`","status":"succeeded"}`)
	}
	stub.sessionPI = "pi_pay07_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]

	sender := &recordingSender{}
	handler := delivery.NewRefundEmailHandler(delivery.RefundEmailOptions{
		HandlerOptions: delivery.HandlerOptions{
			TicketQueries: pf.q, CredentialQueries: pf.q, DeliveryJobQueries: pf.q, Sender: sender,
		},
		DB: srv.pgxPool,
	})

	newOrder := func(name string) *pay03Fixture {
		f := newPay03Order(t, srv.pgxPool, org, "stripe")
		if _, err := srv.pgxPool.Exec(ctx, `UPDATE orders SET buyer_email = $2, buyer_name = 'Ana Perez' WHERE id = $1`,
			f.order, name+"@pay07.test"); err != nil {
			t.Fatal(err)
		}
		return f
	}
	engine := srv.refundEngine()
	create := func(f *pay03Fixture, notify bool) string {
		res, err := engine.CreateBatch(ctx, refunds.BatchInput{
			OrgID: org, OrderID: f.order, Reason: "pay07", CancelTickets: true, NotifyBuyer: notify,
			IdempotencyKey: "pay07-" + uuid.NewString(), Approved: true,
			Items: []refunds.Item{{TicketID: f.tickets[0]}, {TicketID: f.tickets[1]}},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		batch := res.BatchID.String()
		t.Cleanup(func() {
			_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2`, refunds.JobTypeBuyerEmail, batch)
		})
		return batch
	}
	queued := func(batch string) int {
		var n int
		_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2`,
			refunds.JobTypeBuyerEmail, batch).Scan(&n)
		return n
	}
	run := func(batch string) error {
		raw, _ := json.Marshal(refunds.BuyerEmailPayload{BatchID: batch})
		return handler(ctx, raw)
	}

	// ── one operation of two refunds queues ONE job and sends ONE letter
	f1 := newOrder("one")
	b1 := create(f1, true)
	if n := queued(b1); n != 1 {
		t.Fatalf("jobs queued for the operation = %d; want exactly 1", n)
	}
	if err := run(b1); err != nil {
		t.Fatalf("handler: %v", err)
	}
	msgs := sender.sent()
	if len(msgs) != 1 || msgs[0].To != "one@pay07.test" {
		t.Fatalf("messages = %+v; want one letter to the buyer", msgs)
	}
	if !strings.Contains(msgs[0].TextBody, "50.00 EUR") {
		t.Fatalf("letter does not state the 50.00 EUR returned:\n%s", msgs[0].TextBody)
	}
	if err := run(b1); err != nil || len(sender.sent()) != 1 {
		t.Fatalf("a redelivered job mailed again: err=%v messages=%d", err, len(sender.sent()))
	}
	var unmarked int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE batch_id = $1 AND buyer_notified_at IS NULL`, b1).Scan(&unmarked)
	if unmarked != 0 {
		t.Fatalf("%d refunds of the operation are not marked as mailed", unmarked)
	}

	// ── notify_buyer=false: queued maybe, but never mailed
	f2 := newOrder("two")
	b2 := create(f2, false)
	if err := run(b2); err != nil || len(sender.sent()) != 1 {
		t.Fatalf("notify_buyer=false mailed: err=%v messages=%d", err, len(sender.sent()))
	}

	// ── a failed send gives the claim back, the retry sends once
	f3 := newOrder("three")
	b3 := create(f3, true)
	sender.mu.Lock()
	sender.fail = errors.New("smtp down")
	sender.mu.Unlock()
	if err := run(b3); err == nil {
		t.Fatal("a failed send must return an error so the worker retries")
	}
	var claimed int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE batch_id = $1 AND (buyer_notice_claimed_at IS NOT NULL OR buyer_notified_at IS NOT NULL)`, b3).Scan(&claimed)
	if claimed != 0 {
		t.Fatalf("%d refunds stay claimed after a failed send", claimed)
	}
	sender.mu.Lock()
	sender.fail = nil
	sender.mu.Unlock()
	if err := run(b3); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := sender.sent(); len(got) != 2 || got[1].To != "three@pay07.test" {
		t.Fatalf("after the retry messages = %+v; want the second letter to three@", got)
	}
}
