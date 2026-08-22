// dispatcher_test.go — unit tests for the MACS outbox dispatcher.
// No build tag needed — uses mock HTTP server and in-memory DB queries.
package macs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

	// Build the envelope directly (bypassing DB) to test the shape: the
	// data object is the export Ticket (MACS's required fields id, seatId,
	// barcode, actionEvent{...}) — a bare {ticketId} would be rejected by
	// the receiver's Pydantic model with 422.
	var capturedSig string
	env := macsEnvelope{
		ID:      42,
		Created: "2026-08-22T10:00:00Z",
		Type:    "order.paid",
		Data: macsEventData{
			Ticket: Ticket{
				ID:      42,
				SeatID:  7,
				Barcode: "2000000000421",
				ActionEvent: ActionEvent{
					ID:               99,
					CityName:         "Prague",
					VenueName:        "Palac Akropolis",
					ActionName:       "Gig",
					ActionLegalOwner: "Org s.r.o.",
					ShowTime:         "2026-09-01T19:00:00",
				},
			},
			OrderID: 42,
		},
	}

	d := &Dispatcher{
		pool:   nil,
		client: srv.Client(),
	}
	_ = capturedSig

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
	if got["created"] != "2026-08-22T10:00:00Z" {
		t.Errorf("created = %q; want RFC3339 UTC", got["created"])
	}

	// Validate the MACS-required Ticket fields are present in data.
	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %T", got["data"])
	}
	for _, key := range []string{"id", "seatId", "barcode", "actionEvent", "orderId"} {
		if _, present := data[key]; !present {
			t.Errorf("data.%s missing — MACS rejects the envelope without it", key)
		}
	}
	ae, ok := data["actionEvent"].(map[string]any)
	if !ok {
		t.Fatalf("data.actionEvent is not an object: %T", data["actionEvent"])
	}
	for _, key := range []string{"id", "cityName", "venueName", "actionName", "actionLegalOwner", "showTime"} {
		if _, present := ae[key]; !present {
			t.Errorf("data.actionEvent.%s missing", key)
		}
	}
}

// TestMACSDispatcher_HMACSignature proves the X-MACS-Signature header is an
// HMAC-SHA256 over the exact body and is absent when no secret is set.
func TestMACSDispatcher_HMACSignature(t *testing.T) {
	var body []byte
	var sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sig = r.Header.Get("X-MACS-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := &Dispatcher{client: srv.Client()}
	env := macsEnvelope{ID: 1, Created: "2026-08-22T10:00:00Z", Type: "ticket.refunded", Data: macsEventData{}}

	if err := d.post(context.Background(), srv.URL, "s3cret", env); err != nil {
		t.Fatalf("post: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Fatalf("signature = %q, want %q", sig, want)
	}
	if err := d.post(context.Background(), srv.URL, "", env); err != nil {
		t.Fatalf("post unsigned: %v", err)
	}
	if sig != "" {
		t.Fatalf("unsigned post carried a signature %q", sig)
	}
}

// TestMACSDispatcher_NonSuccessIsError pins "success is HTTP 200": a 3xx
// or 5xx answer must surface as an error so the outbox retries.
func TestMACSDispatcher_NonSuccessIsError(t *testing.T) {
	for _, code := range []int{302, 422, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		d := &Dispatcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		err := d.post(context.Background(), srv.URL, "", macsEnvelope{ID: 1, Type: "order.paid", Data: macsEventData{}})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: post returned nil, want error", code)
		}
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
