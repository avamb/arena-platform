// stub_test.go — the MACS stub's ack contract (spec §10 M2/M5, feature #510).
//
// The stub is the only executable statement of what a real MACS receiver
// answers, so its behaviour is pinned here rather than only inside the
// integration round-trips: 200 {"status":"OK"} on acceptance, and
// 200 {"status":"Error"} when an order.paid envelope is not a complete paid
// order.
package stub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// postEnvelope sends one JSON envelope to the stub's webhook endpoint and
// returns the status code together with the decoded ack body.
func postEnvelope(t *testing.T, r *Receiver, env map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	resp, err := http.Post(r.WebhookURL(), "application/json", bytes.NewReader(body)) //nolint:noctx // test helper
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var ack struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		t.Fatalf("ack body is not JSON: %v", err)
	}
	return resp.StatusCode, ack.Status
}

// orderPaidEnvelope builds an order.paid envelope with the given data object.
func orderPaidEnvelope(data map[string]any) map[string]any {
	return map[string]any{
		"id":      1,
		"created": "2026-08-22T10:00:00Z",
		"type":    "order.paid",
		"data":    data,
	}
}

// validOrderData is a complete paid order with two tickets.
func validOrderData() map[string]any {
	return map[string]any{
		"id":     int64(1001),
		"status": "PAID",
		"ticketList": []any{
			map[string]any{"id": int64(1001), "holderStatus": 0},
			map[string]any{"id": int64(1002), "holderStatus": 0},
		},
	}
}

// TestStub_OrderPaidAckContract covers M5: the stub accepts a complete paid
// order and refuses anything else at the BODY level (still HTTP 200).
func TestStub_OrderPaidAckContract(t *testing.T) {
	cases := []struct {
		name       string
		data       map[string]any
		wantStatus string
	}{
		{"complete order", validOrderData(), "OK"},
		{
			"missing ticketList",
			map[string]any{"id": int64(1), "status": "PAID"},
			"Error",
		},
		{
			"empty ticketList",
			map[string]any{"id": int64(1), "status": "PAID", "ticketList": []any{}},
			"Error",
		},
		{
			"null ticketList",
			map[string]any{"id": int64(1), "status": "PAID", "ticketList": nil},
			"Error",
		},
		{
			"status not PAID",
			map[string]any{"id": int64(1), "status": "PENDING", "ticketList": []any{map[string]any{"id": int64(1), "holderStatus": 0}}},
			"Error",
		},
		{
			"status missing",
			map[string]any{"id": int64(1), "ticketList": []any{map[string]any{"id": int64(1), "holderStatus": 0}}},
			"Error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			defer r.Close()

			code, status := postEnvelope(t, r, orderPaidEnvelope(tc.data))
			if code != http.StatusOK {
				t.Fatalf("status code = %d; want 200 — a payload refusal is a body-level answer", code)
			}
			if status != tc.wantStatus {
				t.Fatalf("ack status = %q; want %q", status, tc.wantStatus)
			}
		})
	}
}

// TestStub_OrderPaidUpdatesTicketsFromTicketList proves the stub reads the
// per-ticket state out of data.ticketList (the W1-Ma order.paid shape) rather
// than the flat data.id/data.holderStatus pair it used before.
func TestStub_OrderPaidUpdatesTicketsFromTicketList(t *testing.T) {
	r := New()
	defer r.Close()

	if _, status := postEnvelope(t, r, orderPaidEnvelope(validOrderData())); status != "OK" {
		t.Fatalf("ack status = %q; want OK", status)
	}

	for _, id := range []int64{1001, 1002} {
		tk := r.TicketByID(id)
		if tk == nil {
			t.Fatalf("ticket %d not tracked after order.paid", id)
		}
		if tk.HolderStatus != 0 {
			t.Fatalf("ticket %d holderStatus = %d; want 0", id, tk.HolderStatus)
		}
	}

	// A refund flips exactly one ticket, and keeps the flat data shape.
	refund := map[string]any{
		"id":      2,
		"created": "2026-08-22T11:00:00Z",
		"type":    "ticket.refunded",
		"data":    map[string]any{"id": int64(1001), "holderStatus": 3},
	}
	if _, status := postEnvelope(t, r, refund); status != "OK" {
		t.Fatalf("refund ack status = %q; want OK", status)
	}
	if got := r.TicketByID(1001).HolderStatus; got != 3 {
		t.Fatalf("ticket 1001 holderStatus = %d; want 3 (refunded)", got)
	}
	if got := r.TicketByID(1002).HolderStatus; got != 0 {
		t.Fatalf("ticket 1002 holderStatus = %d; want 0 (untouched by the refund)", got)
	}
}

// TestStub_RefusedOrderPaidChangesNoState proves a body-level refusal is inert:
// the envelope is recorded (the call did arrive) but no ticket is tracked, so a
// retry cannot be masked by half-applied state.
func TestStub_RefusedOrderPaidChangesNoState(t *testing.T) {
	r := New()
	defer r.Close()

	bad := map[string]any{
		"id":     int64(1),
		"status": "PENDING",
		"ticketList": []any{
			map[string]any{"id": int64(7001), "holderStatus": 0},
		},
	}
	if _, status := postEnvelope(t, r, orderPaidEnvelope(bad)); status != "Error" {
		t.Fatalf("ack status = %q; want Error", status)
	}
	if r.TicketByID(7001) != nil {
		t.Fatal("a refused order.paid must not update the ticket store")
	}
	if got := len(r.EventsByType("order.paid")); got != 1 {
		t.Fatalf("recorded order.paid envelopes = %d; want 1", got)
	}
}

// TestStub_SetOnceErrorPath covers the one-shot body-level rejection used by
// the round-trip retry test: the first call is refused, the next succeeds.
func TestStub_SetOnceErrorPath(t *testing.T) {
	r := New()
	defer r.Close()

	r.SetOnceErrorPath("/_wh/tickets")

	if _, status := postEnvelope(t, r, orderPaidEnvelope(validOrderData())); status != "Error" {
		t.Fatalf("first ack status = %q; want Error", status)
	}
	if r.TicketByID(1001) != nil {
		t.Fatal("the one-shot refusal must not update the ticket store")
	}
	if _, status := postEnvelope(t, r, orderPaidEnvelope(validOrderData())); status != "OK" {
		t.Fatalf("second ack status = %q; want OK", status)
	}
	if r.TicketByID(1001) == nil {
		t.Fatal("the retry must land in the ticket store")
	}
	if got := r.DeliveryCount("/_wh/tickets"); got != 2 {
		t.Fatalf("delivery count = %d; want 2 (refusal + retry)", got)
	}
}
