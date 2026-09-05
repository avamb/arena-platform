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
// Expected: both checkout/start calls answer 201; the first order is closed as
// cancelled with a "superseded_by_new_checkout" event; the second is the one
// pending_payment order the index allows.
func TestW1A6c_PublicFeed_SecondCheckoutSameBuyerSupersedesOpenOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()

	srv := buildIntegrationResetServer(t, pool)
	q := gen.New(pool)

	start := func() uuid.UUID {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"session_id": f.sessionID.String(),
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
	if first.CustomerID == nil || second.CustomerID == nil || *first.CustomerID != *second.CustomerID {
		t.Fatalf("customer_id first=%v second=%v; want the same resolved customer", first.CustomerID, second.CustomerID)
	}
	if first.Status != ordering.StatusCancelled {
		t.Errorf("first order status = %q, want %q", first.Status, ordering.StatusCancelled)
	}
	if first.CancelledAt == nil {
		t.Error("first order cancelled_at is NULL; want the supersede timestamp")
	}
	if second.Status != ordering.StatusPendingPayment {
		t.Errorf("second order status = %q, want %q", second.Status, ordering.StatusPendingPayment)
	}

	events, err := q.ListOrderEventsByOrder(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListOrderEventsByOrder: %v", err)
	}
	var superseded bool
	for _, ev := range events {
		if ev.Type != ordering.EventCancelled {
			continue
		}
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode cancelled event payload: %v", err)
		}
		if payload.Reason == "superseded_by_new_checkout" {
			superseded = true
		}
	}
	if !superseded {
		t.Errorf("first order has no cancelled event with reason superseded_by_new_checkout; events=%+v", events)
	}

	// The read side of the index: exactly one open order for this customer+session.
	open, err := ordering.FindOpenOrder(ctx, q, *second.CustomerID, f.sessionID)
	if err != nil {
		t.Fatalf("FindOpenOrder: %v", err)
	}
	if open.ID != second.ID {
		t.Errorf("FindOpenOrder = %s, want the second order %s", open.ID, second.ID)
	}
}
