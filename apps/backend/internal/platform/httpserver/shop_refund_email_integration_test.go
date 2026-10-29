//go:build integration

package httpserver

// shop_refund_email_integration_test.go — finding M-13: the buyer of a shop
// order is written to when money of the order goes back. A partial and then a
// full refund through the real router and the refund engine each queue ONE
// shop.order_email job of kind "refunded" in the transaction that folds the
// refund into the order; a replay queues nothing; the event center's resend
// accepts the kind; and the real worker handler renders the letter with the
// right amounts and remembers it per refund.
//
//	go test -tags integration -run TestShopRefundEmail ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

type captureSender struct {
	mu   sync.Mutex
	sent []email.Message
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureSender) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *captureSender) last() email.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent[len(c.sent)-1]
}

func refundedJobs(jobs []shop.OrderEmailPayload) []shop.OrderEmailPayload {
	var out []shop.OrderEmailPayload
	for _, j := range jobs {
		if j.Kind == shop.EmailRefunded {
			out = append(out, j)
		}
	}
	return out
}

func TestShopRefundEmail_PartialThenFullRefundQueueOneLetterEach(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 2) // 2 x 45.00 = 90.00 (9000 minor) + any fee
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	f.pay(token)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	refundPath := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund")

	// Nothing to write about before a refund; the resend refuses the kind.
	resend := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/resend-email")
	if code, body, _ := f.do(http.MethodPost, resend, f.ownerA, `{"kind":"refunded"}`); code != http.StatusConflict || shopErrCode(body) != "shop.order_email_unavailable" {
		t.Fatalf("refunded letter of an order with no refund = %d %v; want 409 shop.order_email_unavailable", code, body)
	}

	// Partial refund: one letter, naming the refund.
	if code, body, _ := f.do(http.MethodPost, refundPath, f.ownerA, `{"amount_minor":5000}`); code != http.StatusOK {
		t.Fatalf("partial refund = %d %v", code, body)
	}
	first := refundedJobs(f.emailJobs(o.ID))
	if len(first) != 1 || first[0].RefundID == "" || first[0].Resend || first[0].OrgID != f.orgA.String() {
		t.Fatalf("jobs after the partial refund = %+v; want one refunded letter naming the refund", first)
	}
	var firstRefund uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM refunds WHERE payment_intent_id = $1 AND state = 'succeeded' AND amount = 5000`,
		*o.PaymentIntentID).Scan(&firstRefund); err != nil || firstRefund.String() != first[0].RefundID {
		t.Fatalf("the letter names refund %s; the succeeded refund is %s (%v)", first[0].RefundID, firstRefund, err)
	}

	// A replay (the refunds folded into the order again) queues nothing.
	svc := shop.New(f.pool, slog.Default())
	for i := 0; i < 2; i++ {
		if _, err := svc.SyncRefunds(context.Background(), f.orgA, o.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Even if the order row lagged behind and the same refund were folded in
	// again, the per-refund guard keeps it to one job.
	f.exec(`UPDATE shop_orders SET refunded_minor = 0, status = 'paid' WHERE id = $1`, o.ID)
	if _, err := svc.SyncRefunds(context.Background(), f.orgA, o.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(refundedJobs(f.emailJobs(o.ID))); n != 1 {
		t.Fatalf("refunded jobs after the replays = %d; want still 1", n)
	}

	// The rest: a second letter for the second refund.
	if code, body, _ := f.do(http.MethodPost, refundPath, f.ownerA, ``); code != http.StatusOK {
		t.Fatalf("refund of the rest = %d %v", code, body)
	}
	all := refundedJobs(f.emailJobs(o.ID))
	if len(all) != 2 || all[1].RefundID == all[0].RefundID {
		t.Fatalf("jobs after the full refund = %+v; want two letters for two refunds", all)
	}

	// The real handler: the partial letter first, the full one second, each
	// remembered per refund, a redelivery mails nothing.
	f.exec(`UPDATE shop_orders SET buyer_locale = 'en' WHERE id = $1`, o.ID) // the fixture buyer is Russian-speaking
	sender := &captureSender{}
	handle := delivery.NewShopEmailHandler(delivery.ShopEmailOptions{
		HandlerOptions: delivery.HandlerOptions{Sender: sender, Logger: slog.Default()},
		DB:             f.pool,
	})
	run := func(p shop.OrderEmailPayload) {
		raw, _ := json.Marshal(p)
		if err := handle(context.Background(), raw); err != nil {
			t.Fatalf("handler: %v", err)
		}
	}
	run(all[0])
	if sender.count() != 1 {
		t.Fatalf("letters after the first job = %d; want 1", sender.count())
	}
	partial := sender.last()
	if !strings.Contains(partial.Subject, "Refund for order") || !strings.Contains(partial.TextBody, "50.00") {
		t.Fatalf("partial letter: subject %q body %q; want the partial wording and the 50.00 amount", partial.Subject, partial.TextBody)
	}
	if strings.Contains(partial.TextBody, "refunded in full") {
		t.Fatalf("a partial refund must not say the order is refunded in full:\n%s", partial.TextBody)
	}
	run(all[0]) // redelivery
	if sender.count() != 1 {
		t.Fatalf("a redelivered job mailed again (%d letters)", sender.count())
	}
	run(all[1])
	if sender.count() != 2 {
		t.Fatalf("letters after the second job = %d; want 2 (the marker is per refund)", sender.count())
	}
	full := sender.last()
	if !strings.Contains(full.Subject, "refunded") || !strings.Contains(full.TextBody, "refunded in full") {
		t.Fatalf("full letter: subject %q body %q; want the order reported as refunded", full.Subject, full.TextBody)
	}

	// The event center can send it again: the newest refund, flagged Resend.
	code, rb, _ := f.do(http.MethodPost, resend, f.ownerA, `{"kind":"refunded"}`)
	if code != http.StatusAccepted || rb["queued"] != true || rb["kind"] != "refunded" {
		t.Fatalf("resend of the refunded letter = %d %v; want 202 queued", code, rb)
	}
	again := refundedJobs(f.emailJobs(o.ID))
	if len(again) != 3 || !again[2].Resend || again[2].RefundID != all[1].RefundID {
		t.Fatalf("jobs after the resend = %+v; want a Resend one for the newest refund", again)
	}
	run(again[2])
	if sender.count() != 3 {
		t.Fatalf("the resend was swallowed by the sent marker (%d letters)", sender.count())
	}
}
