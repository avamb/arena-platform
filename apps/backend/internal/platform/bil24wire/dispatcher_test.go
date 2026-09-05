package bil24wire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

// fakeLoader answers every Loader question from fields, so the tests below
// exercise the real mapping, the real envelope and a real HTTP round trip
// without a database.
type fakeLoader struct {
	sub      Subscriber
	hasSub   bool
	eventSub []Subscriber

	order     *Order
	systemID  int64
	channelID uuid.UUID
	ticket    *RefundedTicket
	actionIDs []int64

	subErr error

	// calls records the channels the dispatcher routed by.
	routedChannels []uuid.UUID
}

func (f *fakeLoader) SubscriberByChannel(_ context.Context, channelID uuid.UUID) (Subscriber, bool, error) {
	f.routedChannels = append(f.routedChannels, channelID)
	if f.subErr != nil {
		return Subscriber{}, false, f.subErr
	}
	return f.sub, f.hasSub, nil
}

func (f *fakeLoader) SubscribersForEvent(context.Context, uuid.UUID) ([]Subscriber, error) {
	return f.eventSub, nil
}

func (f *fakeLoader) Order(context.Context, uuid.UUID) (*Order, error) { return f.order, nil }

func (f *fakeLoader) OrderRef(context.Context, uuid.UUID) (int64, uuid.UUID, error) {
	return f.systemID, f.channelID, nil
}

func (f *fakeLoader) RefundedTicket(context.Context, uuid.UUID) (*RefundedTicket, uuid.UUID, error) {
	return f.ticket, f.channelID, nil
}

func (f *fakeLoader) ActionEventIDs(context.Context, uuid.UUID, []uuid.UUID) ([]int64, error) {
	return f.actionIDs, nil
}

// receiver is the stand-in WordPress site: it records every delivery.
type receiver struct {
	srv     *httptest.Server
	status  int
	bodies  [][]byte
	headers []http.Header
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("receiver: read body: %v", err)
		}
		r.bodies = append(r.bodies, body)
		r.headers = append(r.headers, req.Header.Clone())
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) subscriber(secret string) Subscriber {
	return Subscriber{CallbackURL: r.srv.URL, SigningSecret: secret}
}

// envelopeOf decodes delivery i, keeping `data` raw so each test can assert
// the shape its own mapping promises.
func (r *receiver) envelopeOf(t *testing.T, i int) (Envelope, json.RawMessage) {
	t.Helper()
	if len(r.bodies) <= i {
		t.Fatalf("expected at least %d deliveries, got %d", i+1, len(r.bodies))
	}
	var raw struct {
		ID      int64           `json:"id"`
		Created string          `json:"created"`
		Type    string          `json:"type"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.bodies[i], &raw); err != nil {
		t.Fatalf("decode envelope %d: %v (%s)", i, err, r.bodies[i])
	}
	return Envelope{ID: raw.ID, Created: raw.Created, Type: raw.Type}, raw.Data
}

// outboxEvent is one outbox row as the worker hands it to a dispatcher.
func outboxEvent(eventType string, payload map[string]any) outbox.Event {
	return outbox.Event{
		ID:         "b0a2f1d4-0000-4000-8000-000000000001",
		EventType:  eventType,
		Payload:    payload,
		OccurredAt: time.Date(2026, 9, 6, 12, 30, 0, 0, time.UTC),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mapping tests
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_OrderPaid_DeliversFullOrder(t *testing.T) {
	rcv := newReceiver(t)
	channel := uuid.New()
	order := &Order{ID: 4242, Currency: "HUF", TicketList: []Ticket{{ID: 7}}}
	loader := &fakeLoader{sub: rcv.subscriber("s3cret"), hasSub: true, order: order, channelID: channel}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": channel.String(),
	})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	env, data := rcv.envelopeOf(t, 0)
	if env.Type != SiteEventOrderPaid {
		t.Errorf("type = %q, want %q", env.Type, SiteEventOrderPaid)
	}
	if env.Created != "2026-09-06T12:30:00Z" {
		t.Errorf("created = %q, want the outbox occurrence time in UTC", env.Created)
	}
	if env.ID == 0 {
		t.Error("envelope id must be derived from the outbox uuid, got 0")
	}
	var got Order
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if got.ID != 4242 || len(got.TicketList) != 1 {
		t.Errorf("order.paid must carry the full order with ticketList, got %+v", got)
	}
	// Routed by the payload's channel, without a second lookup.
	if len(loader.routedChannels) != 1 || loader.routedChannels[0] != channel {
		t.Errorf("routed channels = %v, want [%s]", loader.routedChannels, channel)
	}
}

func TestDispatch_OrderPaid_SignsBodyWithSubscriberSecret(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{sub: rcv.subscriber("topsecret"), hasSub: true, order: &Order{ID: 1}}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	hdr := rcv.headers[0]
	if got := hdr.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := hdr.Get("X-Arena-Event-Type"); got != SiteEventOrderPaid {
		t.Errorf("X-Arena-Event-Type = %q", got)
	}
	want := Sign(rcv.bodies[0], "topsecret")
	if got := hdr.Get("X-Arena-Signature"); got != want {
		t.Errorf("X-Arena-Signature = %q, want %q", got, want)
	}
}

func TestDispatch_OrderPaid_NoSecretMeansNoSignature(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, order: &Order{ID: 1}}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := rcv.headers[0].Get("X-Arena-Signature"); got != "" {
		t.Errorf("unsigned subscriber must get no signature header, got %q", got)
	}
}

func TestDispatch_OrderPaid_FallsBackToOrderChannel(t *testing.T) {
	rcv := newReceiver(t)
	channel := uuid.New()
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, order: &Order{ID: 9}, channelID: channel}
	d := NewDispatcherWithLoader(loader)

	// A pre-#506 outbox row carries no channel_id; the order knows it.
	ev := outboxEvent(EventOrderPaid, map[string]any{"order_id": uuid.New().String()})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(loader.routedChannels) != 1 || loader.routedChannels[0] != channel {
		t.Errorf("routed channels = %v, want the order's channel %s", loader.routedChannels, channel)
	}
}

func TestDispatch_OrderCancelled_DeliversIDAndStatus(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, systemID: 777, channelID: uuid.New()}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderCancelled, map[string]any{"order_id": uuid.New().String()})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	env, data := rcv.envelopeOf(t, 0)
	if env.Type != SiteEventOrderCancelled {
		t.Errorf("type = %q, want %q", env.Type, SiteEventOrderCancelled)
	}
	var got CancelledOrder
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != 777 || got.Status != StatusCancelled {
		t.Errorf("data = %+v, want {777 CANCELLED}", got)
	}
	// Bil24 sends exactly two keys — a site parsing the void must not have to
	// tolerate an order-shaped body here.
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("decode keys: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("order.cancelled data keys = %v, want exactly id + status", keys)
	}
}

func TestDispatch_TicketTerminalStates_AllMapToRefunded(t *testing.T) {
	for _, platformType := range []string{EventTicketCancelled, EventTicketRefunded, EventTicketRevoked} {
		t.Run(platformType, func(t *testing.T) {
			rcv := newReceiver(t)
			loader := &fakeLoader{
				sub:       rcv.subscriber(""),
				hasSub:    true,
				ticket:    &RefundedTicket{ID: 55, OrderID: 4242, HolderStatus: HolderStatusRefund},
				channelID: uuid.New(),
			}
			d := NewDispatcherWithLoader(loader)

			ev := outboxEvent(platformType, map[string]any{"ticket_id": uuid.New().String()})
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}

			env, data := rcv.envelopeOf(t, 0)
			if env.Type != SiteEventTicketRefunded {
				t.Errorf("type = %q, want %q", env.Type, SiteEventTicketRefunded)
			}
			var got RefundedTicket
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.ID != 55 || got.HolderStatus != HolderStatusRefund {
				t.Errorf("data = %+v, want the refunded ticket", got)
			}
		})
	}
}

func TestDispatch_CatalogEvents_MapAndFanOut(t *testing.T) {
	cases := []struct {
		platformType string
		wantType     string
	}{
		{EventEventPublished, SiteEventEventCreated},
		{EventEventUpdated, SiteEventEventChanged},
		{EventSessionUpdated, SiteEventEventChanged},
		{EventSessionCancel, SiteEventEventDeleted},
	}
	for _, tc := range cases {
		t.Run(tc.platformType, func(t *testing.T) {
			rcv := newReceiver(t)
			// Two sites publish the same event: both must be notified.
			loader := &fakeLoader{
				eventSub:  []Subscriber{rcv.subscriber(""), rcv.subscriber("")},
				actionIDs: []int64{101, 102},
			}
			d := NewDispatcherWithLoader(loader)

			ev := outboxEvent(tc.platformType, map[string]any{
				"event_id":    uuid.New().String(),
				"session_ids": []any{uuid.New().String()},
			})
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(rcv.bodies) != 2 {
				t.Fatalf("deliveries = %d, want one per subscribed site", len(rcv.bodies))
			}

			env, data := rcv.envelopeOf(t, 0)
			if env.Type != tc.wantType {
				t.Errorf("type = %q, want %q", env.Type, tc.wantType)
			}
			var refs []ActionEventRef
			if err := json.Unmarshal(data, &refs); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(refs) != 2 || refs[0].ActionEventID != 101 || refs[1].ActionEventID != 102 {
				t.Errorf("data = %+v, want [{101} {102}]", refs)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Skip / failure semantics
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_SkipsWhenChannelHasNoSubscriber(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{hasSub: false, order: &Order{ID: 1}, channelID: uuid.New()}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("a channel without a WordPress site is a skip, not a failure: %v", err)
	}
	if len(rcv.bodies) != 0 {
		t.Errorf("delivered %d envelopes to a channel with no subscriber", len(rcv.bodies))
	}
}

func TestDispatch_SkipsUnknownEventTypeAndMalformedPayload(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, order: &Order{ID: 1}}
	d := NewDispatcherWithLoader(loader)

	cases := []outbox.Event{
		outboxEvent("v1.user.registered", map[string]any{"order_id": uuid.New().String()}),
		outboxEvent(EventOrderPaid, map[string]any{}),
		outboxEvent(EventOrderPaid, map[string]any{"order_id": "not-a-uuid"}),
		outboxEvent(EventTicketRefunded, map[string]any{}),
		outboxEvent(EventEventPublished, map[string]any{}),
	}
	for _, ev := range cases {
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Errorf("Dispatch(%s) = %v, want nil so the outbox stops retrying", ev.EventType, err)
		}
	}
	if len(rcv.bodies) != 0 {
		t.Errorf("delivered %d envelopes for undeliverable events", len(rcv.bodies))
	}
}

func TestDispatch_SkipsOrderWithNothingIssued(t *testing.T) {
	rcv := newReceiver(t)
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, order: nil}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(rcv.bodies) != 0 {
		t.Errorf("an order with no exportable tickets must not be delivered, got %d", len(rcv.bodies))
	}
}

func TestDispatch_NonSuccessStatusIsRetryableError(t *testing.T) {
	rcv := newReceiver(t)
	rcv.status = http.StatusInternalServerError
	loader := &fakeLoader{sub: rcv.subscriber(""), hasSub: true, order: &Order{ID: 1}}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err == nil {
		t.Fatal("a 500 from the site must fail the dispatch so the outbox retries")
	}
}

func TestDispatch_LookupFailureIsRetryableError(t *testing.T) {
	loader := &fakeLoader{subErr: errors.New("connection refused"), order: &Order{ID: 1}}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventOrderPaid, map[string]any{
		"order_id":   uuid.New().String(),
		"channel_id": uuid.New().String(),
	})
	if err := d.Dispatch(context.Background(), ev); err == nil {
		t.Fatal("a failed subscriber lookup must be retried, not swallowed")
	}
}

func TestDispatch_CatalogFanOut_OneFailingSiteStillDeliversTheOthers(t *testing.T) {
	ok := newReceiver(t)
	bad := newReceiver(t)
	bad.status = http.StatusBadGateway

	loader := &fakeLoader{
		eventSub:  []Subscriber{bad.subscriber(""), ok.subscriber("")},
		actionIDs: []int64{7},
	}
	d := NewDispatcherWithLoader(loader)

	ev := outboxEvent(EventEventUpdated, map[string]any{"event_id": uuid.New().String()})
	err := d.Dispatch(context.Background(), ev)
	if err == nil {
		t.Fatal("a failing site must make the whole dispatch retryable")
	}
	if len(ok.bodies) != 1 {
		t.Errorf("healthy site got %d deliveries, want 1", len(ok.bodies))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Envelope details
// ─────────────────────────────────────────────────────────────────────────────

func TestEnvelopeID_IsStableAndNonNegative(t *testing.T) {
	ev := outboxEvent(EventOrderPaid, nil)
	first := envelopeID(ev)
	if first != envelopeID(ev) {
		t.Error("envelope id must be a pure function of the outbox uuid")
	}
	if first < 0 {
		t.Errorf("envelope id = %d, must be non-negative", first)
	}
	if got := envelopeID(outbox.Event{ID: "not-a-uuid"}); got != 0 {
		t.Errorf("unparseable outbox id must degrade to 0, got %d", got)
	}
}

func TestPayloadSessionIDs_AcceptsBothProducerShapes(t *testing.T) {
	one := uuid.New()
	two := uuid.New()

	if got := payloadSessionIDs(map[string]any{"session_ids": []any{one.String(), two.String()}}); len(got) != 2 {
		t.Errorf("[]any session_ids = %v, want 2 ids", got)
	}
	if got := payloadSessionIDs(map[string]any{"session_ids": []string{one.String()}}); len(got) != 1 {
		t.Errorf("[]string session_ids = %v, want 1 id", got)
	}
	// v1.session.cancelled names a single session.
	got := payloadSessionIDs(map[string]any{"session_id": one.String()})
	if len(got) != 1 || got[0] != one {
		t.Errorf("single session_id = %v, want [%s]", got, one)
	}
	if got := payloadSessionIDs(map[string]any{"session_ids": []any{"nonsense"}}); len(got) != 0 {
		t.Errorf("garbage ids must be dropped, got %v", got)
	}
}
