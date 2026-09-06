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

	"github.com/google/uuid"

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

// okAckServer starts a receiver that captures the POST body and answers the
// MACS success ack (spec §10 M2: 200 plus {"status":"OK"}).
func okAckServer(t *testing.T, captured *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		if captured != nil {
			*captured = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"OK"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sampleOrder is the order.paid data object: one MACS Order carrying the whole
// ticketList (spec §10 M1).
func sampleOrder() Order {
	return Order{
		ID:             42,
		Date:           "2026-08-22T10:00:00Z",
		Status:         "PAID",
		Currency:       "CZK",
		Sum:            50000,
		Charge:         50000,
		TotalSum:       50000,
		TicketQuantity: 1,
		TicketList: []Ticket{{
			ID:      42,
			SeatID:  7,
			OrderID: 42,
			Barcode: "2000000000421",
			ActionEvent: ActionEvent{
				ID:               99,
				CityName:         "Prague",
				VenueName:        "Palac Akropolis",
				ActionName:       "Gig",
				ActionLegalOwner: "Org s.r.o.",
				ShowTime:         "2026-09-01T19:00:00",
			},
		}},
		SeatList:         []interface{}{},
		GatewayOrderList: []interface{}{},
	}
}

// TestMACSDispatcher_EnvelopeShape verifies the exact JSON shape of a MACS
// envelope for an order.paid event. Since W1-Ma (spec §10 M1) the data object
// is the whole ORDER — {id, status:"PAID", ticketList:[…]} — and MACS's
// required Ticket fields (id, seatId, barcode, actionEvent{…}) live inside
// each ticketList entry.
func TestMACSDispatcher_EnvelopeShape(t *testing.T) {
	var captured []byte
	srv := okAckServer(t, &captured)

	env := macsEnvelope{
		ID:      42,
		Created: "2026-08-22T10:00:00Z",
		Type:    "order.paid",
		Data:    sampleOrder(),
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
	if got["created"] != "2026-08-22T10:00:00Z" {
		t.Errorf("created = %q; want RFC3339 UTC", got["created"])
	}

	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %T", got["data"])
	}
	if data["status"] != "PAID" {
		t.Errorf("data.status = %v; want PAID", data["status"])
	}
	if data["id"] != float64(42) {
		t.Errorf("data.id = %v; want 42", data["id"])
	}
	list, ok := data["ticketList"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("data.ticketList = %#v; want one ticket", data["ticketList"])
	}
	tk, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("data.ticketList[0] is not an object: %T", list[0])
	}
	// Validate the MACS-required Ticket fields are present.
	for _, key := range []string{"id", "seatId", "barcode", "actionEvent", "orderId"} {
		if _, present := tk[key]; !present {
			t.Errorf("data.ticketList[0].%s missing — MACS rejects the envelope without it", key)
		}
	}
	ae, ok := tk["actionEvent"].(map[string]any)
	if !ok {
		t.Fatalf("data.ticketList[0].actionEvent is not an object: %T", tk["actionEvent"])
	}
	for _, key := range []string{"id", "cityName", "venueName", "actionName", "actionLegalOwner", "showTime"} {
		if _, present := ae[key]; !present {
			t.Errorf("data.ticketList[0].actionEvent.%s missing", key)
		}
	}
}

// TestMACSDispatcher_RefundedEnvelopeShape pins the ticket.refunded data
// object, which M1 left unchanged: the flat MACS Ticket shape.
func TestMACSDispatcher_RefundedEnvelopeShape(t *testing.T) {
	var captured []byte
	srv := okAckServer(t, &captured)

	order := sampleOrder()
	data := macsEventData{Ticket: order.TicketList[0], OrderID: order.ID}
	data.HolderStatus = StatusRefunded
	d := &Dispatcher{client: srv.Client()}
	if err := d.post(context.Background(), srv.URL, "", macsEnvelope{
		ID: 7, Created: "2026-08-22T10:00:00Z", Type: "ticket.refunded", Data: data,
	}); err != nil {
		t.Fatalf("post: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	body, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %T", got["data"])
	}
	for _, key := range []string{"id", "seatId", "barcode", "actionEvent", "orderId", "holderStatus"} {
		if _, present := body[key]; !present {
			t.Errorf("data.%s missing from ticket.refunded", key)
		}
	}
	if body["holderStatus"] != float64(StatusRefunded) {
		t.Errorf("data.holderStatus = %v; want %d", body["holderStatus"], StatusRefunded)
	}
}

// TestMACSDispatcher_AckBodyDecidesSuccess is the M2 contract: HTTP 2xx alone
// is not a delivery. Only a JSON body with status "OK" counts; every other
// answer must surface as an error so the outbox retries.
func TestMACSDispatcher_AckBodyDecidesSuccess(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"ok", `{"status":"OK"}`, false},
		{"ok lowercase", `{"status":"ok"}`, false},
		{"ok with extra fields", `{"status":"OK","imported":3}`, false},
		{"error", `{"status":"Error"}`, true},
		{"error with message", `{"status":"Error","message":"bad payload"}`, true},
		{"empty status", `{"status":""}`, true},
		{"no status field", `{"imported":3}`, true},
		{"empty body", ``, true},
		{"non-JSON body", `<html>oops</html>`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()

			d := &Dispatcher{client: srv.Client()}
			err := d.post(context.Background(), srv.URL, "", macsEnvelope{ID: 1, Type: "order.paid", Data: sampleOrder()})
			if tc.wantErr && err == nil {
				t.Fatalf("body %q: post returned nil, want error so the outbox retries", tc.body)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("body %q: post returned %v, want nil", tc.body, err)
			}
		})
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
		_, _ = io.WriteString(w, `{"status":"OK"}`)
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

// TestMACSDispatcher_OrderPaidMalformedPayloadSkipped covers the M1 entry
// point: a v1.order.paid event whose payload carries no usable order_id is
// skipped without touching the database (pool is nil — a DB call would panic).
func TestMACSDispatcher_OrderPaidMalformedPayloadSkipped(t *testing.T) {
	payloads := []map[string]any{
		{},                              // no order_id at all
		{"order_id": ""},                // empty
		{"order_id": "not-a-uuid"},      // unparseable
		{"order_id": 42},                // wrong type
		{"ticket_id": uuid.NewString()}, // ticket-shaped payload on an order event
	}
	d := &Dispatcher{pool: nil, client: &http.Client{}}
	for _, p := range payloads {
		ev := outbox.Event{EventType: EventOrderPaid, Payload: p, OccurredAt: time.Now()}
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("payload %v: Dispatch returned %v, want nil", p, err)
		}
	}
}

// TestMACSDispatcher_OrderPaidWithoutRoutableOrgSkipped proves that an order
// event that carries a valid order_id but no org_id/session_id is skipped
// before any subscriber lookup — resolveOrgID fails first, so the nil pool is
// never touched.
func TestMACSDispatcher_OrderPaidWithoutRoutableOrgSkipped(t *testing.T) {
	d := &Dispatcher{pool: nil, client: &http.Client{}}
	ev := outbox.Event{
		EventType:  EventOrderPaid,
		Payload:    map[string]any{"order_id": uuid.NewString()},
		OccurredAt: time.Now(),
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch returned %v, want nil", err)
	}
}

// TestMACSEventTypeMapping tests the macsEventType helper for known and unknown types.
func TestMACSEventTypeMapping(t *testing.T) {
	cases := []struct {
		input    string
		wantType string
		wantOK   bool
	}{
		{EventOrderPaid, "order.paid", true},
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
