// bil24_505_order_wire_test.go — Feature #505 (W1-B7b) tests for the
// GET_ORDER_INFO upgrade: the answer now comes from the shared bil24wire
// encoder (spec §9.3) instead of the hand-built pre-#505 body.
//
// The seam under test is encodeOrderHeaderForWire, which is pure over an
// injected OrderProjector — no pool, no live DB, no HTTP.
package hbil24

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

func wireTestHandler(p OrderProjector) *Handler {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return h.WithOrderExport(p)
}

func wireTestSession() gen.CheckoutSessionRow {
	pf := int64(150)
	prov := int64(50)
	return gen.CheckoutSessionRow{
		ID:          uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b4"),
		State:       "completed",
		PlatformFee: &pf,
		ProviderFee: &prov,
	}
}

func wireTestChannel() gen.SalesChannelRow {
	return gen.SalesChannelRow{DisplayNumber: 4242, Name: "Lampyris WP"}
}

func wireTestProjection() *orderexport.Order {
	start := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	return &orderexport.Order{
		CheckoutSessionID: wireTestSession().ID,
		ID:                90001,
		CompletedAt:       start.Add(-48 * time.Hour),
		Currency:          "CZK",
		Subtotal:          50000,
		Discount:          5000,
		Total:             45200,
		BuyerEmail:        "buyer@example.test",
		Tickets: []orderexport.Ticket{{
			ID:             90001,
			SeatID:         7,
			OrderID:        90001,
			TicketUUID:     uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2c1"),
			Seated:         true,
			Seat:           orderexport.SeatLocation{Sector: "A", Row: "3", Number: "7"},
			TierName:       "Standard",
			Price:          50000,
			Discount:       5000,
			Charge:         45000,
			TotalPrice:     45000,
			Barcode:        "1234567890123",
			PlatformStatus: "active",
			Event: orderexport.Event{
				EventID:        uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2d1"),
				SessionID:      uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2d2"),
				EventName:      "Wine Tasting",
				OrgLegalName:   "Lampyris s.r.o.",
				OrgName:        "Lampyris",
				VenueID:        uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2d3"),
				VenueName:      "Main Hall",
				Currency:       "CZK",
				SessionStartAt: start,
				ShowTimeLocal:  "2026-09-06T20:00:00",
			},
		}},
	}
}

// TestBil24_505_EncodeOrderHeaderForWire_UsesProjection proves the wired path
// answers from the bil24wire encoder and carries NO ticketList (spec §7.8:
// GET_ORDER_INFO is the §9.3 order object minus its tickets).
func TestBil24_505_EncodeOrderHeaderForWire_UsesProjection(t *testing.T) {
	h := wireTestHandler(func(context.Context, uuid.UUID) (*orderexport.Order, error) {
		return wireTestProjection(), nil
	})

	order, ok := h.encodeOrderHeaderForWire(context.Background(), wireTestSession(), wireTestChannel())
	if !ok {
		t.Fatalf("encodeOrderHeaderForWire returned ok=false for a projection with issued tickets")
	}
	if len(order.TicketList) != 0 {
		t.Errorf("ticketList MUST be empty for GET_ORDER_INFO (spec §7.8), got %d", len(order.TicketList))
	}
	if order.ID != 90001 {
		t.Errorf("id = %d, want 90001 (order id from the projection)", order.ID)
	}
	if order.Frontend.ID != 4242 || order.Frontend.Name != "Lampyris WP" {
		t.Errorf("frontend = %+v, want the authenticated channel", order.Frontend)
	}
	// charge is the service fee booked on the checkout session (platform +
	// provider), in MAJOR units on the wire.
	if order.Charge != 2 {
		t.Errorf("charge = %v, want 2 (150+50 minor units)", order.Charge)
	}
	if order.TicketQuantity != 1 {
		t.Errorf("ticketQuantity = %d, want 1", order.TicketQuantity)
	}
}

// TestBil24_505_EncodeOrderHeaderForWire_FallsBack pins the three degraded
// cases that MUST hand the request back to the pre-#505 body rather than
// answer with an empty order: no projector wired, projection error, and a
// session with nothing issued (a pending cart is not a Bil24 order).
func TestBil24_505_EncodeOrderHeaderForWire_FallsBack(t *testing.T) {
	cases := map[string]OrderProjector{
		"unwired": nil,
		"error": func(context.Context, uuid.UUID) (*orderexport.Order, error) {
			return nil, errors.New("boom")
		},
		"nil projection": func(context.Context, uuid.UUID) (*orderexport.Order, error) {
			return nil, nil
		},
		"no tickets": func(context.Context, uuid.UUID) (*orderexport.Order, error) {
			return &orderexport.Order{ID: 1}, nil
		},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			h := wireTestHandler(p)
			if _, ok := h.encodeOrderHeaderForWire(context.Background(), wireTestSession(), wireTestChannel()); ok {
				t.Errorf("ok = true, want false so the caller falls back to the legacy body")
			}
		})
	}
}

// TestBil24_505_CheckoutCharge_SumsFees pins that the wire `charge` is the
// order's SERVICE FEE (platform + provider), nil-safe on both.
func TestBil24_505_CheckoutCharge_SumsFees(t *testing.T) {
	if got := checkoutCharge(gen.CheckoutSessionRow{}); got != 0 {
		t.Errorf("charge with no fees = %d, want 0", got)
	}
	pf := int64(150)
	if got := checkoutCharge(gen.CheckoutSessionRow{PlatformFee: &pf}); got != 150 {
		t.Errorf("charge with only a platform fee = %d, want 150", got)
	}
	if got := checkoutCharge(wireTestSession()); got != 200 {
		t.Errorf("charge = %d, want 200", got)
	}
}
