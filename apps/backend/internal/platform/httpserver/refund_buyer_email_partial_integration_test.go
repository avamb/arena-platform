//go:build integration

package httpserver

// Spec 51 section 2: Stripe writes to the buyer about a refund only when the
// payment has a Customer object, which a hosted checkout usually does not
// create, so arena's own refund letter must go for a PARTIAL refund as well as
// a full one, and it names the organization as the seller and the provider
// that processed the payment.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func TestRefundBuyerEmail_PartialRefundIsMailedWithSellerAndProvider(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "s51-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "S51R")
	ctx := context.Background()
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE organizations SET legal_name = 'Teatro Partial S.L.' WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_`+uuid.NewString()[:8]+`","status":"succeeded"}`)
	}
	stub.sessionPI = "pi_s51_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]

	sender := &recordingSender{}
	handler := delivery.NewRefundEmailHandler(delivery.RefundEmailOptions{
		HandlerOptions: delivery.HandlerOptions{
			TicketQueries: pf.q, CredentialQueries: pf.q, DeliveryJobQueries: pf.q, Sender: sender,
		},
		DB: srv.pgxPool,
	})
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE orders SET buyer_email = 'partial@s51.test', buyer_name = 'Ana Perez' WHERE id = $1`, f.order); err != nil {
		t.Fatal(err)
	}
	part := int64(1000)
	res, err := srv.refundEngine().CreateBatch(ctx, refunds.BatchInput{
		OrgID: org, OrderID: f.order, Reason: "s51 partial", CancelTickets: false, NotifyBuyer: true,
		IdempotencyKey: "s51-" + uuid.NewString(), Approved: true,
		Items: []refunds.Item{{TicketID: f.tickets[0], Amount: &part}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	batch := res.BatchID.String()
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2`, refunds.JobTypeBuyerEmail, batch)
	})
	var queued int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2`,
		refunds.JobTypeBuyerEmail, batch).Scan(&queued)
	if queued != 1 {
		t.Fatalf("a partial refund queued %d letters; want 1", queued)
	}
	raw, _ := json.Marshal(refunds.BuyerEmailPayload{BatchID: batch})
	if err := handler(ctx, raw); err != nil {
		t.Fatalf("handler: %v", err)
	}
	msgs := sender.sent()
	if len(msgs) != 1 || msgs[0].To != "partial@s51.test" {
		t.Fatalf("messages = %+v; want one letter to the buyer", msgs)
	}
	body := msgs[0].TextBody
	for _, want := range []string{"10.00 EUR", "Seller: Teatro Partial S.L.", "Payment processed by Stripe"} {
		if !strings.Contains(body, want) {
			t.Errorf("letter misses %q:\n%s", want, body)
		}
	}
}
