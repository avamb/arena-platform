//go:build integration

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// TestW1A6c_PublicFeed_SecondCheckoutSameBuyerSupersedesOpenOrder pins the
// regression that turned the CI job "Widget Acceptance (real backend)" red
// after feature #488: a widget buyer who starts a second checkout for the same
// event session with the same email hit the partial unique index
// orders_one_pending_per_customer_session_uq (spec §14.3) and got a 500.
//
// Expected: both checkout/start calls answer 201. Since the supersede fix
// (2026-11-24) the first order, whose payment page is still open, is NOT
// cancelled — its payment may still arrive — and the second is minted without
// customer_id so the index admits it. The "every attempt failed → closed
// whole" case is TestSupersede_SameEmailFailedAttemptsClosesFirstWhole.
func TestW1A6c_PublicFeed_SecondCheckoutSameBuyerSupersedesOpenOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()

	defer enableStripeForChannel(t, ctx, pool, f.orgID, f.channelID)()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	start := func() uuid.UUID {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"session_id": f.sessionID.String(),
			"return_url": hostedTicketsBaseURL + "/embed",
			"tier_id":    f.tierID.String(),
			"qty":        1,
			"buyer": map[string]any{
				"email": f.buyerMail,
				"name":  "Wanda Buyer",
				"phone": f.buyerPhone,
			},
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost,
			"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("checkout/start = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			CheckoutSession struct {
				ID string `json:"id"`
			} `json:"checkout_session"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode start response: %v (body: %s)", err, rec.Body.String())
		}
		id, err := uuid.Parse(resp.CheckoutSession.ID)
		if err != nil {
			t.Fatalf("checkout_session.id is not a UUID: %v", err)
		}
		return id
	}

	firstCS := start()
	secondCS := start() // used to 500 on the unique index

	first, err := q.GetOrderByCheckoutSession(ctx, firstCS)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession(first): %v", err)
	}
	second, err := q.GetOrderByCheckoutSession(ctx, secondCS)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession(second): %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("both checkouts map to the same order %s; want two orders", first.ID)
	}
	// The first checkout's Stripe page is still open (its intent is not
	// 'failed'), so its order may still be paid: it is NOT cancelled, and the
	// second order goes without a customer link (the index admits one open
	// order per customer and session).
	if first.CustomerID == nil {
		t.Fatal("first order has no customer; want the resolved customer")
	}
	if second.CustomerID != nil {
		t.Errorf("second order customer_id = %v; want NULL while the first order is open", second.CustomerID)
	}
	if first.Status != ordering.StatusPendingPayment {
		t.Errorf("first order status = %q, want %q (it can still be paid)", first.Status, ordering.StatusPendingPayment)
	}
	if second.Status != ordering.StatusPendingPayment {
		t.Errorf("second order status = %q, want %q", second.Status, ordering.StatusPendingPayment)
	}
	events, err := q.ListOrderEventsByOrder(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListOrderEventsByOrder: %v", err)
	}
	if got := ordering.LastCancelReason(events); got != "" {
		t.Errorf("first order has a cancelled event (%q); want none", got)
	}

	// The read side of the index: the one open order of this customer+session
	// is still the first.
	open, err := ordering.FindOpenOrder(ctx, q, *first.CustomerID, f.sessionID)
	if err != nil {
		t.Fatalf("FindOpenOrder: %v", err)
	}
	if open.ID != first.ID {
		t.Errorf("FindOpenOrder = %s, want the first order %s", open.ID, first.ID)
	}
}
