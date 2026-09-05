// encoder_binding_test.go — feature #505 (W1-B7b) BINDING encoder test.
//
// binding_test.go proves the pseudonymized FIXTURE matches the spec §9.3
// inventory. This file closes the loop: it proves the bil24wire ENCODER emits
// exactly the same key sets, derived from the fixture itself rather than from
// a hand-copied list. Drift on either side — a key added to the encoder, a key
// dropped from it, a regenerated fixture with new fields — fails here.
//
// Values are NOT asserted here (68 pseudonymized orders carry no arena data);
// value semantics are owned by
// internal/platform/bil24wire/encode_test.go.
//
// No integration tag: pure JSON over checked-in test data.
package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// fixtureKeySets reads the 68 real exports and returns the UNION of keys seen
// at each of the three levels. The union (not the intersection) is the
// contract: Bil24 always emits every key, so a key missing from one order
// would already have failed the fixture test above.
func fixtureKeySets(t *testing.T) (order, ticket, event map[string]struct{}) {
	t.Helper()
	path := filepath.Join("testdata", "wp", "bil24_orders_pseudonymized.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var orders []map[string]interface{}
	if err := json.Unmarshal(data, &orders); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(orders) == 0 {
		t.Fatalf("fixture must contain at least one order")
	}

	order = map[string]struct{}{}
	ticket = map[string]struct{}{}
	event = map[string]struct{}{}
	for _, o := range orders {
		for k := range o {
			order[k] = struct{}{}
		}
		tickets, _ := o["ticketList"].([]interface{})
		for _, raw := range tickets {
			tm, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			for k := range tm {
				ticket[k] = struct{}{}
			}
			ev, ok := tm["actionEvent"].(map[string]interface{})
			if !ok {
				continue
			}
			for k := range ev {
				event[k] = struct{}{}
			}
		}
	}
	if len(order) == 0 || len(ticket) == 0 || len(event) == 0 {
		t.Fatalf("fixture yielded empty key sets: %d/%d/%d", len(order), len(ticket), len(event))
	}
	return order, ticket, event
}

// encoderSample is a two-ticket order (one seated, one general admission, one
// refunded) exercising every optional branch of the encoder at once. Even the
// branches that produce JSON null must still emit their key.
func encoderSample() (orderexport.Order, bil24wire.EncodeContext) {
	sessionID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	eventID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	venueID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	cityID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	refundAt := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	refundPrice := int64(1500)

	ev := orderexport.Event{
		EventID:        eventID,
		SessionID:      sessionID,
		EventName:      "Wine Tasting",
		OrgLegalName:   "Vino & Co s.r.o.",
		OrgName:        "Vino & Co",
		VenueID:        venueID,
		VenueName:      "Hall A",
		CityID:         &cityID,
		CityName:       "Prague",
		Currency:       "CZK",
		SessionStartAt: time.Date(2026, 5, 10, 17, 0, 0, 0, time.UTC),
		ShowTimeLocal:  "2026-05-10T19:00:00",
	}

	o := orderexport.Order{
		ID:              1000000500,
		CompletedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Currency:        "CZK",
		Subtotal:        3000,
		Discount:        500,
		Total:           2625,
		DiscountReason:  "Промокод WELCOME",
		PaymentProvider: "woocommerce",
		BuyerEmail:      "buyer@example.com",
		Tickets: []orderexport.Ticket{
			{
				ID: 4021, SeatID: 1731, OrderID: 1000000500,
				Seated:   true,
				Seat:     orderexport.SeatLocation{Sector: "A", Row: "3", Number: "12"},
				TierName: "Parter", Price: 1500, Discount: 250,
				DiscountReason: "Промокод WELCOME",
				Barcode:        "2100000040218",
				PlatformStatus: "active",
				Event:          ev,
			},
			{
				ID: 4022, SeatID: 1000004022, OrderID: 1000000500,
				Seated:   false,
				TierName: "Standing", Price: 1500, Discount: 250,
				Barcode:        "2100000040225",
				PlatformStatus: "cancelled",
				RefundDate:     &refundAt,
				RefundPrice:    &refundPrice,
				Event:          ev,
			},
		},
	}

	ec := bil24wire.EncodeContext{
		Agent:          bil24wire.Agent{ID: 7, Name: "Vino & Co"},
		Frontend:       bil24wire.Frontend{ID: 21, AgentID: 7, Name: "WordPress"},
		UserID:         901,
		Email:          "buyer@example.com",
		Phone:          "+420000000000",
		FullName:       "Test Buyer",
		LegalOwnerInn:  "000000000000",
		ActionEventIDs: map[uuid.UUID]int64{sessionID: 55501},
		ActionIDs:      map[uuid.UUID]int64{eventID: 3301},
		VenueIDs:       map[uuid.UUID]int64{venueID: 44},
		CityIDs:        map[uuid.UUID]int64{cityID: 12},
	}
	return o, ec
}

// marshalToMap round-trips a wire object through JSON so the assertion sees
// exactly what a WordPress receiver would parse (omitempty included).
func marshalToMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestCompatBil24_505_Encoder_KeySetsMatchRealExports is THE binding test of
// feature #505: the encoder's order / ticket / actionEvent key sets must equal
// the key sets of the 68 real Bil24 exports, and equal the spec §9.3
// inventories those exports were reconciled with.
func TestCompatBil24_505_Encoder_KeySetsMatchRealExports(t *testing.T) {
	wantOrder, wantTicket, wantEvent := fixtureKeySets(t)

	// The fixture is itself pinned to the spec inventory; assert the two
	// agree before using the fixture as the encoder's oracle, otherwise a
	// silently regenerated fixture could relax the contract.
	for name, pair := range map[string][2]map[string]struct{}{
		"order":       {wantOrder, toSet(spec93OrderKeys)},
		"ticket":      {wantTicket, toSet(spec93TicketKeys)},
		"actionEvent": {wantEvent, toSet(spec93ActionEventKeys)},
	} {
		got, spec := pair[0], pair[1]
		if len(got) != len(spec) {
			t.Fatalf("%s: fixture has %d keys, spec §9.3 has %d", name, len(got), len(spec))
		}
		for k := range spec {
			if _, ok := got[k]; !ok {
				t.Fatalf("%s: spec key %q absent from the fixture", name, k)
			}
		}
	}

	o, ec := encoderSample()
	encoded := marshalToMap(t, bil24wire.EncodeOrder(o, ec))

	if extra, missing := diffKeys(encoded, wantOrder); len(extra) > 0 || len(missing) > 0 {
		t.Errorf("EncodeOrder key drift: extra=%v missing=%v", extra, missing)
	}

	tickets, ok := encoded["ticketList"].([]interface{})
	if !ok || len(tickets) != len(o.Tickets) {
		t.Fatalf("ticketList must carry %d tickets, got %T %v", len(o.Tickets), encoded["ticketList"], encoded["ticketList"])
	}
	for i, raw := range tickets {
		tm, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("ticketList[%d] is not an object: %T", i, raw)
		}
		if extra, missing := diffKeys(tm, wantTicket); len(extra) > 0 || len(missing) > 0 {
			t.Errorf("encoded ticket[%d] key drift: extra=%v missing=%v", i, extra, missing)
		}
		ev, ok := tm["actionEvent"].(map[string]interface{})
		if !ok {
			t.Fatalf("ticketList[%d].actionEvent is not an object: %T", i, tm["actionEvent"])
		}
		if extra, missing := diffKeys(ev, wantEvent); len(extra) > 0 || len(missing) > 0 {
			t.Errorf("encoded ticket[%d].actionEvent key drift: extra=%v missing=%v", i, extra, missing)
		}
	}
}

// TestCompatBil24_505_EncoderHeader_IsOrderMinusTicketList pins the
// GET_ORDER_INFO surface (spec §7.8): the same order object with ticketList —
// and ONLY ticketList — absent.
func TestCompatBil24_505_EncoderHeader_IsOrderMinusTicketList(t *testing.T) {
	wantOrder, _, _ := fixtureKeySets(t)
	want := map[string]struct{}{}
	for k := range wantOrder {
		if k == "ticketList" {
			continue
		}
		want[k] = struct{}{}
	}

	o, ec := encoderSample()
	header := marshalToMap(t, bil24wire.EncodeOrderHeader(o, ec))
	if extra, missing := diffKeys(header, want); len(extra) > 0 || len(missing) > 0 {
		t.Errorf("EncodeOrderHeader key drift: extra=%v missing=%v", extra, missing)
	}
}

// TestCompatBil24_505_RefundedTicket_IsTicketSubset pins the §9.2
// `ticket.refunded` payload: nine keys, every one of them also a key of the
// full ticket object, so a receiver can reuse its ticket parser.
func TestCompatBil24_505_RefundedTicket_IsTicketSubset(t *testing.T) {
	_, wantTicket, wantEvent := fixtureKeySets(t)

	o, ec := encoderSample()
	payload := marshalToMap(t, bil24wire.EncodeTicketRefunded(o.Tickets[1], ec))

	spec92 := toSet([]string{
		"id", "orderId", "seatId", "barcode", "refundPrice", "refundDate",
		"category", "holderStatus", "actionEvent",
	})
	if extra, missing := diffKeys(payload, spec92); len(extra) > 0 || len(missing) > 0 {
		t.Errorf("EncodeTicketRefunded key drift: extra=%v missing=%v", extra, missing)
	}
	for k := range payload {
		if _, ok := wantTicket[k]; !ok {
			t.Errorf("refunded payload key %q is not a key of the Bil24 ticket object", k)
		}
	}
	ev, ok := payload["actionEvent"].(map[string]interface{})
	if !ok {
		t.Fatalf("refunded payload actionEvent is not an object: %T", payload["actionEvent"])
	}
	if extra, missing := diffKeys(ev, wantEvent); len(extra) > 0 || len(missing) > 0 {
		t.Errorf("refunded actionEvent key drift: extra=%v missing=%v", extra, missing)
	}
	if payload["holderStatus"] != bil24wire.HolderStatusRefund {
		t.Errorf("holderStatus = %v, want %q", payload["holderStatus"], bil24wire.HolderStatusRefund)
	}
}
