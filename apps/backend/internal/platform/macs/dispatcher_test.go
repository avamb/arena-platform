// dispatcher_test.go — unit tests for the MACS outbox dispatcher.
// No build tag needed — uses mock HTTP server and in-memory DB queries.
package macs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TestMACSDispatcher_NonMACSEventSkipped verifies that events with unrelated
// event types (e.g. "order.placed") are silently skipped — Dispatch returns nil
// without making any HTTP call.
func TestMACSDispatcher_NonMACSEventSkipped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &Dispatcher{
		pool:   nil, // should not be touched
		client: srv.Client(),
	}

	ev := outbox.Event{
		EventType:  "order.placed",
		Payload:    map[string]any{"ticket_id": "some-uuid"},
		OccurredAt: time.Now(),
	}

	err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatalf("expected nil error for non-MACS event, got %v", err)
	}
	if called {
		t.Fatal("expected no HTTP call for non-MACS event")
	}
}

// TestMACSDispatcher_EnvelopeShape verifies the exact JSON shape of a MACS
// envelope for an order.paid event. The test uses a mock httptest server to
// capture the POST body and validate the envelope fields.
func TestMACSDispatcher_EnvelopeShape(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		captured, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Build the envelope directly (bypassing DB) to test the shape.
	env := macsEnvelope{
		ID:      42,
		Created: "2026-08-22T10:00:00",
		Type:    "order.paid",
		Data: macsOrderPaidData{
			TicketID:   42,
			SessionID:  "aaa-bbb-ccc",
			CheckoutID: "ddd-eee-fff",
		},
	}

	d := &Dispatcher{
		pool:   nil,
		client: srv.Client(),
	}

	err := d.post(context.Background(), srv.URL, "test-secret", env)
	if err != nil {
		t.Fatalf("post returned unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("no request body captured")
	}

	var got map[string]any
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Validate top-level fields.
	if got["type"] != "order.paid" {
		t.Errorf("type = %q; want %q", got["type"], "order.paid")
	}
	if got["id"] != float64(42) {
		t.Errorf("id = %v; want 42", got["id"])
	}
	if got["created"] != "2026-08-22T10:00:00" {
		t.Errorf("created = %q; want %q", got["created"], "2026-08-22T10:00:00")
	}

	// Validate data fields.
	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %T", got["data"])
	}
	if data["ticketId"] != float64(42) {
		t.Errorf("data.ticketId = %v; want 42", data["ticketId"])
	}
	if data["sessionId"] != "aaa-bbb-ccc" {
		t.Errorf("data.sessionId = %q; want %q", data["sessionId"], "aaa-bbb-ccc")
	}
	if data["checkoutId"] != "ddd-eee-fff" {
		t.Errorf("data.checkoutId = %q; want %q", data["checkoutId"], "ddd-eee-fff")
	}
}

// TestMACSDispatcher_MissingPayloadFieldsSkipped ensures that events missing
// ticket_id or session_id are silently skipped without panicking or erroring.
func TestMACSDispatcher_MissingPayloadFieldsSkipped(t *testing.T) {
	d := &Dispatcher{
		pool:   nil, // should not be touched
		client: &http.Client{},
	}

	ev := outbox.Event{
		EventType:  EventTicketIssued,
		Payload:    map[string]any{}, // missing ticket_id and session_id
		OccurredAt: time.Now(),
	}

	err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatalf("expected nil for malformed payload, got %v", err)
	}
}

// TestMACSEventTypeMapping tests the macsEventType helper for known and unknown types.
func TestMACSEventTypeMapping(t *testing.T) {
	cases := []struct {
		input    string
		wantType string
		wantOK   bool
	}{
		{EventTicketIssued, "order.paid", true},
		{EventTicketRefunded, "ticket.refunded", true},
		{EventTicketCancelled, "ticket.refunded", true},
		{EventTicketRevoked, "ticket.refunded", true},
		{"order.placed", "", false},
		{"v1.echo.created", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		got, ok := macsEventType(tc.input)
		if ok != tc.wantOK {
			t.Errorf("macsEventType(%q): ok = %v; want %v", tc.input, ok, tc.wantOK)
		}
		if got != tc.wantType {
			t.Errorf("macsEventType(%q): type = %q; want %q", tc.input, got, tc.wantType)
		}
	}
}
