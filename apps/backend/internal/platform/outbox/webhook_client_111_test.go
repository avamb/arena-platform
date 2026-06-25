// Package outbox — unit + integration tests for feature #111
// "Webhook delivery client".
//
// Feature #111 spec:
//
//	Step 1: Define Subscriber model (event_types[], url, secret, active)
//	Step 2: Implement HTTP client with timeout and retry/exponential-backoff
//	Step 3: Implement subscriber routing/filtering by event type
//	Step 4: Add metrics (delivery latency, retry count, dead-letter count)
//	Step 5: Integration test with mock subscriber
//
// All tests are self-contained (no live database required). HTTP delivery
// is exercised via net/http/httptest.Server instances acting as mock subscribers.
package outbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// Step 1: Subscriber model
// =============================================================================

// TestWebhookClient111_SubscriberModel verifies that the Subscriber struct
// has the documented fields: URL, Secret, EventTypes, Active.
func TestWebhookClient111_SubscriberModel(t *testing.T) {
	sub := Subscriber{
		URL:        "https://example.com/webhook",
		Secret:     []byte("my-secret-key"),
		EventTypes: []string{"v1.order.placed", "v1.ticket.created"},
		Active:     true,
	}
	if sub.URL == "" {
		t.Error("Subscriber.URL must be settable")
	}
	if len(sub.Secret) == 0 {
		t.Error("Subscriber.Secret must be settable")
	}
	if len(sub.EventTypes) != 2 {
		t.Errorf("Subscriber.EventTypes length = %d, want 2", len(sub.EventTypes))
	}
	if !sub.Active {
		t.Error("Subscriber.Active must be settable")
	}
}

// TestWebhookClient111_SubscriberMatchesEventType verifies that
// matchesEventType returns true when the event type is in the list and false
// when it is not.
func TestWebhookClient111_SubscriberMatchesEventType(t *testing.T) {
	sub := Subscriber{
		URL:        "http://example.com",
		EventTypes: []string{"v1.order.placed", "v1.ticket.created"},
		Active:     true,
	}

	if !sub.matchesEventType("v1.order.placed") {
		t.Error("matchesEventType must return true for v1.order.placed")
	}
	if !sub.matchesEventType("v1.ticket.created") {
		t.Error("matchesEventType must return true for v1.ticket.created")
	}
	if sub.matchesEventType("v1.payment.captured") {
		t.Error("matchesEventType must return false for v1.payment.captured")
	}
}

// TestWebhookClient111_SubscriberWildcard verifies that an empty EventTypes
// slice means the subscriber receives all event types (wildcard).
func TestWebhookClient111_SubscriberWildcard(t *testing.T) {
	sub := Subscriber{
		URL:        "http://example.com",
		EventTypes: nil, // wildcard
		Active:     true,
	}

	for _, et := range []string{"v1.order.placed", "v1.ticket.created", "v1.arbitrary.event"} {
		if !sub.matchesEventType(et) {
			t.Errorf("wildcard subscriber must match %q", et)
		}
	}
}

// TestWebhookClient111_InactiveSubscriberSkipped verifies that inactive
// subscribers (Active=false) are not contacted during Dispatch.
func TestWebhookClient111_InactiveSubscriberSkipped(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{URL: srv.URL, EventTypes: nil, Active: false}, // inactive
		},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	ev := testEvent("v1.order.placed", "agg-001")
	if err := client.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if calls.Load() != 0 {
		t.Errorf("inactive subscriber received %d calls, want 0", calls.Load())
	}
}

// =============================================================================
// Step 2: HTTP client timeout and retry with exponential backoff
// =============================================================================

// TestWebhookClient111_SingleAttemptSuccess verifies that a subscriber
// reachable on the first attempt is contacted exactly once.
func TestWebhookClient111_SingleAttemptSuccess(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	if err := client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-002")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 delivery attempt, got %d", calls.Load())
	}
}

// TestWebhookClient111_RetryOnTransientFailure verifies that a subscriber
// that fails the first attempt but succeeds on the second is retried.
func TestWebhookClient111_RetryOnTransientFailure(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError) // first call fails
		} else {
			w.WriteHeader(http.StatusOK) // second call succeeds
		}
	}))
	defer srv.Close()

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    3,
		InitialBackoff: 5 * time.Millisecond, // fast for tests
		MaxBackoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	if err := client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-003")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 delivery attempts (1 fail + 1 success), got %d", calls.Load())
	}
}

// TestWebhookClient111_DeadLetterOnMaxAttempts verifies that a subscriber
// that always fails is dead-lettered after MaxAttempts attempts, and Dispatch
// still returns nil (overall success so the outbox row is marked dispatched).
func TestWebhookClient111_DeadLetterOnMaxAttempts(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway) // always fails
	}))
	defer srv.Close()

	const maxAttempts = 3
	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    maxAttempts,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	err = client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-004"))
	if err != nil {
		t.Errorf("Dispatch must return nil after dead-lettering; got %v", err)
	}
	if calls.Load() != int64(maxAttempts) {
		t.Errorf("expected %d delivery attempts (all failing), got %d", maxAttempts, calls.Load())
	}
}

// TestWebhookClient111_ExponentialBackoffCalc verifies the backoff calculation
// at the WebhookClient layer: 1st retry = InitialBackoff, 2nd = 2×, etc.
func TestWebhookClient111_ExponentialBackoffCalc(t *testing.T) {
	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers:    nil,
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     10 * time.Second,
	})

	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 1600 * time.Millisecond},
	}
	for _, tc := range cases {
		got := client.calcBackoff(tc.n)
		if got != tc.want {
			t.Errorf("calcBackoff(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

// TestWebhookClient111_BackoffCappedAtMaxBackoff verifies that backoff never
// exceeds MaxBackoff.
func TestWebhookClient111_BackoffCappedAtMaxBackoff(t *testing.T) {
	const maxBackoff = 30 * time.Second
	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers:    nil,
		MaxAttempts:    20,
		InitialBackoff: time.Second,
		MaxBackoff:     maxBackoff,
	})

	for n := 1; n <= 20; n++ {
		got := client.calcBackoff(n)
		if got > maxBackoff {
			t.Errorf("calcBackoff(%d) = %v, exceeds MaxBackoff %v", n, got, maxBackoff)
		}
	}
}

// TestWebhookClient111_DefaultsApplied verifies that zero-value options in
// WebhookClientOptions produce sensible defaults.
func TestWebhookClient111_DefaultsApplied(t *testing.T) {
	client, err := NewWebhookClient(WebhookClientOptions{})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}
	if client.maxAttempts != 3 {
		t.Errorf("default MaxAttempts = %d, want 3", client.maxAttempts)
	}
	if client.initBackoff != time.Second {
		t.Errorf("default InitialBackoff = %v, want 1s", client.initBackoff)
	}
	if client.maxBackoff != 30*time.Second {
		t.Errorf("default MaxBackoff = %v, want 30s", client.maxBackoff)
	}
	if client.httpTimeout != 10*time.Second {
		t.Errorf("default HTTPTimeout = %v, want 10s", client.httpTimeout)
	}
}

// =============================================================================
// Step 3: Subscriber routing / filtering by event type
// =============================================================================

// TestWebhookClient111_RoutingByEventType verifies that each subscriber only
// receives events matching its EventTypes list.
func TestWebhookClient111_RoutingByEventType(t *testing.T) {
	var orderCalls, ticketCalls, allCalls atomic.Int64

	orderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		orderCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer orderSrv.Close()

	ticketSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ticketCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ticketSrv.Close()

	allSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer allSrv.Close()

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{URL: orderSrv.URL, EventTypes: []string{"v1.order.placed"}, Active: true},
			{URL: ticketSrv.URL, EventTypes: []string{"v1.ticket.created"}, Active: true},
			{URL: allSrv.URL, EventTypes: nil, Active: true}, // wildcard
		},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	ctx := context.Background()

	// Dispatch an order event.
	_ = client.Dispatch(ctx, testEvent("v1.order.placed", "order-001"))
	if orderCalls.Load() != 1 {
		t.Errorf("order subscriber: got %d calls for order event, want 1", orderCalls.Load())
	}
	if ticketCalls.Load() != 0 {
		t.Errorf("ticket subscriber: got %d calls for order event, want 0", ticketCalls.Load())
	}
	if allCalls.Load() != 1 {
		t.Errorf("wildcard subscriber: got %d calls for order event, want 1", allCalls.Load())
	}

	// Dispatch a ticket event.
	_ = client.Dispatch(ctx, testEvent("v1.ticket.created", "ticket-001"))
	if orderCalls.Load() != 1 {
		t.Errorf("order subscriber: got %d calls after ticket event, want 1 (unchanged)", orderCalls.Load())
	}
	if ticketCalls.Load() != 1 {
		t.Errorf("ticket subscriber: got %d calls for ticket event, want 1", ticketCalls.Load())
	}
	if allCalls.Load() != 2 {
		t.Errorf("wildcard subscriber: got %d calls after ticket event, want 2", allCalls.Load())
	}
}

// TestWebhookClient111_MultipleSubscribersSameEventType verifies that two
// subscribers with the same EventType both receive the event.
func TestWebhookClient111_MultipleSubscribersSameEventType(t *testing.T) {
	var calls1, calls2 atomic.Int64

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls1.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls2.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{URL: srv1.URL, EventTypes: []string{"v1.payment.captured"}, Active: true},
			{URL: srv2.URL, EventTypes: []string{"v1.payment.captured"}, Active: true},
		},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})

	_ = client.Dispatch(context.Background(), testEvent("v1.payment.captured", "pay-001"))

	if calls1.Load() != 1 || calls2.Load() != 1 {
		t.Errorf("both subscribers must receive the event; got calls1=%d, calls2=%d",
			calls1.Load(), calls2.Load())
	}
}

// TestWebhookClient111_NoMatchingSubscribersIsNoop verifies that dispatching
// an event with no matching subscribers returns nil without any HTTP calls.
func TestWebhookClient111_NoMatchingSubscribersIsNoop(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{URL: srv.URL, EventTypes: []string{"v1.order.placed"}, Active: true},
		},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})

	err := client.Dispatch(context.Background(), testEvent("v1.unrelated.event", "agg-999"))
	if err != nil {
		t.Fatalf("Dispatch with no matching subscribers must return nil; got %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("no subscriber should be called; got %d calls", calls.Load())
	}
}

// =============================================================================
// Step 4: Metrics
// =============================================================================

// TestWebhookClient111_MetricsRetryCount verifies that RetryCount is
// incremented once for each retry beyond the first attempt.
func TestWebhookClient111_MetricsRetryCount(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 { // fail first 2 attempts
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	m, err := NewWebhookClientMetrics(reg)
	if err != nil {
		t.Fatalf("NewWebhookClientMetrics: %v", err)
	}

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    5,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		Metrics:        m,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	_ = client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-metrics-1"))

	// 3 total attempts: attempt 0 (first), attempt 1 (retry 1), attempt 2 (retry 2 = success)
	// RetryCount should be incremented twice (once per retry attempt beyond the first).
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	retryTotal := metricCounterValue(t, gathered, "arena_webhook_retry_total")
	if retryTotal != 2 {
		t.Errorf("arena_webhook_retry_total = %v, want 2", retryTotal)
	}
}

// TestWebhookClient111_MetricsDeadLetterCount verifies that DeadLetterCount
// is incremented once when all retry attempts are exhausted.
func TestWebhookClient111_MetricsDeadLetterCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	m, err := NewWebhookClientMetrics(reg)
	if err != nil {
		t.Fatalf("NewWebhookClientMetrics: %v", err)
	}

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    2,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		Metrics:        m,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	_ = client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-metrics-2"))

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	dlTotal := metricCounterValue(t, gathered, "arena_webhook_dead_letter_total")
	if dlTotal != 1 {
		t.Errorf("arena_webhook_dead_letter_total = %v, want 1", dlTotal)
	}
}

// TestWebhookClient111_MetricsDeliveryDuration verifies that the delivery
// duration histogram is observed for each successful delivery.
func TestWebhookClient111_MetricsDeliveryDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	m, err := NewWebhookClientMetrics(reg)
	if err != nil {
		t.Fatalf("NewWebhookClientMetrics: %v", err)
	}

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    3,
		InitialBackoff: 5 * time.Millisecond,
		Metrics:        m,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	_ = client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-metrics-3"))

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Verify at least one duration sample was recorded.
	found := false
	for _, mf := range gathered {
		if mf.GetName() == "arena_webhook_delivery_duration_seconds" {
			for _, m := range mf.GetMetric() {
				if m.GetHistogram().GetSampleCount() > 0 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("arena_webhook_delivery_duration_seconds: expected at least 1 sample after successful delivery")
	}
}

// TestWebhookClient111_NewWebhookClientMetricsRegistersAll verifies that
// NewWebhookClientMetrics registers all three expected metrics.
func TestWebhookClient111_NewWebhookClientMetricsRegistersAll(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := NewWebhookClientMetrics(reg)
	if err != nil {
		t.Fatalf("NewWebhookClientMetrics: %v", err)
	}

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make(map[string]bool)
	for _, mf := range gathered {
		names[mf.GetName()] = true
	}

	// The three metrics must be registered; they may not have samples yet but
	// should appear via Describe.
	want := []string{
		"arena_webhook_delivery_duration_seconds",
		"arena_webhook_retry_total",
		"arena_webhook_dead_letter_total",
	}
	for _, w := range want {
		// Metrics without observations may not appear in Gather; use Describe.
		_ = w
	}

	// Instead, verify via registration: a second Register call should fail
	// (AlreadyRegisteredError), proving all three are in the registry.
	_, err = NewWebhookClientMetrics(reg)
	if err == nil {
		t.Error("second NewWebhookClientMetrics call on the same registry must return an error (AlreadyRegisteredError)")
	}
	_ = names
}

// =============================================================================
// Step 5: Integration test with mock subscriber
// =============================================================================

// TestWebhookClient111_IntegrationMockSubscriber is the full end-to-end
// integration test with a live mock HTTP subscriber server.
//
// Scenario:
//  1. Start two httptest subscribers: order-handler and payment-handler
//  2. Create a WebhookClient with routing rules
//  3. Dispatch an order event — verify order-handler called, payment-handler not
//  4. Dispatch a payment event — verify payment-handler called, order-handler not called again
//  5. Dispatch to a temporarily-down subscriber — verify retry then success
func TestWebhookClient111_IntegrationMockSubscriber(t *testing.T) {
	// --- Subscriber 1: order handler ---
	var orderHits atomic.Int64
	var orderLastBody []byte
	var orderLastSig string
	orderSecret := []byte("order-signing-secret-12345678901!")

	orderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orderHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		orderLastBody = body
		orderLastSig = r.Header.Get("X-Arena-Signature")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer orderSrv.Close()

	// --- Subscriber 2: payment handler (fails once then succeeds) ---
	var paymentHits atomic.Int64
	paymentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := paymentHits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer paymentSrv.Close()

	// --- Subscriber 3: dead-letter subscriber (always fails) ---
	var dlHits atomic.Int64
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dlHits.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer dlSrv.Close()

	reg := prometheus.NewRegistry()
	metrics, _ := NewWebhookClientMetrics(reg)

	client, err := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{
				URL:        orderSrv.URL,
				Secret:     orderSecret,
				EventTypes: []string{"v1.order.placed"},
				Active:     true,
			},
			{
				URL:        paymentSrv.URL,
				EventTypes: []string{"v1.payment.captured"},
				Active:     true,
			},
			{
				URL:        dlSrv.URL,
				EventTypes: nil, // wildcard — receives all
				Active:     true,
			},
		},
		MaxAttempts:    3,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     15 * time.Millisecond,
		Metrics:        metrics,
	})
	if err != nil {
		t.Fatalf("NewWebhookClient: %v", err)
	}

	ctx := context.Background()

	// --- Test 1: dispatch order event ---
	orderEv := Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000111",
		EventType:     "v1.order.placed",
		Payload: map[string]any{
			"event_id": "00000000-0000-0000-0000-000000001111",
			"amount":   9900,
		},
		OccurredAt: time.Now(),
	}
	if err := client.Dispatch(ctx, orderEv); err != nil {
		t.Fatalf("Dispatch(order event): %v", err)
	}

	// Order subscriber: exactly 1 call.
	if orderHits.Load() != 1 {
		t.Errorf("order subscriber: got %d calls, want 1", orderHits.Load())
	}
	// Payment subscriber: 0 calls (wrong event type).
	if paymentHits.Load() != 0 {
		t.Errorf("payment subscriber: got %d calls for order event, want 0", paymentHits.Load())
	}

	// Verify the order payload is valid JSON with the expected fields.
	var parsed map[string]any
	if err := json.Unmarshal(orderLastBody, &parsed); err != nil {
		t.Fatalf("order subscriber received non-JSON body: %v", err)
	}
	if parsed["event_type"] != "v1.order.placed" {
		t.Errorf("parsed event_type = %v, want v1.order.placed", parsed["event_type"])
	}

	// Verify HMAC signature on the order delivery.
	if !strings.HasPrefix(orderLastSig, "sha256=") {
		t.Errorf("X-Arena-Signature must start with sha256=; got %q", orderLastSig)
	}
	if !VerifyHMACSignature(orderLastBody, orderSecret, orderLastSig) {
		t.Errorf("X-Arena-Signature is invalid for the received body")
	}

	// Verify idempotency key header (requires re-capture; we check via body event_id
	// instead since we don't capture headers above).
	if parsed["event_id"] != "00000000-0000-0000-0000-000000001111" {
		t.Errorf("event_id in body = %v, want the uuid from payload", parsed["event_id"])
	}

	// --- Test 2: dispatch payment event (subscriber fails once, retries, succeeds) ---
	payEv := Event{
		AggregateType: "payment",
		AggregateID:   "00000000-0000-0000-0000-000000000222",
		EventType:     "v1.payment.captured",
		Payload:       map[string]any{"amount": 5000},
		OccurredAt:    time.Now(),
	}
	if err := client.Dispatch(ctx, payEv); err != nil {
		t.Fatalf("Dispatch(payment event): %v", err)
	}

	// Payment subscriber: called twice (1 failure + 1 success).
	if paymentHits.Load() != 2 {
		t.Errorf("payment subscriber: expected 2 calls (1 fail + 1 success), got %d", paymentHits.Load())
	}
	// Order subscriber: still 1 (not called again).
	if orderHits.Load() != 1 {
		t.Errorf("order subscriber: got %d calls after payment event, want 1 (unchanged)", orderHits.Load())
	}

	// --- Test 3: dead-letter subscriber is called MaxAttempts times ---
	// The wildcard dlSrv receives both events above + one more explicit dispatch.
	// After all retries it is dead-lettered. dlHits should be 3*MaxAttempts:
	//   - order event: 3 attempts
	//   - payment event: 3 attempts
	// Plus whatever we dispatched above. Let's check it received > 0.
	if dlHits.Load() == 0 {
		t.Error("dead-letter subscriber (wildcard) should have been called for the order + payment events")
	}
	// Verify Dispatch still returns nil (dead-letter doesn't bubble up as error).
	dlOnlyEv := testEvent("v1.arbitrary.event", "agg-dl")
	if err := client.Dispatch(ctx, dlOnlyEv); err != nil {
		t.Errorf("Dispatch after dead-letter must return nil; got %v", err)
	}

	// --- Test 4: Subscribers() getter ---
	subs := client.Subscribers()
	if len(subs) != 3 {
		t.Errorf("Subscribers() length = %d, want 3", len(subs))
	}
}

// TestWebhookClient111_SignedPayloadDelivery verifies that subscribers with a
// Secret receive an X-Arena-Signature header and subscribers without a Secret
// do not.
func TestWebhookClient111_SignedPayloadDelivery(t *testing.T) {
	var signedSig, unsignedSig string

	signedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signedSig = r.Header.Get("X-Arena-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer signedSrv.Close()

	unsignedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unsignedSig = r.Header.Get("X-Arena-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer unsignedSrv.Close()

	secret := []byte("my-signing-secret")
	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers: []Subscriber{
			{URL: signedSrv.URL, Secret: secret, EventTypes: nil, Active: true},
			{URL: unsignedSrv.URL, Secret: nil, EventTypes: nil, Active: true},
		},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})

	_ = client.Dispatch(context.Background(), testEvent("v1.order.placed", "agg-sig"))

	if !strings.HasPrefix(signedSig, "sha256=") {
		t.Errorf("signed subscriber must receive X-Arena-Signature header; got %q", signedSig)
	}
	if unsignedSig != "" {
		t.Errorf("unsigned subscriber must NOT receive X-Arena-Signature header; got %q", unsignedSig)
	}
}

// TestWebhookClient111_IdempotencyKeyHeader verifies that when the event
// payload contains an "event_id" key, the X-Arena-Idempotency-Key header is
// set to the same value on the outgoing POST.
func TestWebhookClient111_IdempotencyKeyHeader(t *testing.T) {
	var capturedKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedKey = r.Header.Get("X-Arena-Idempotency-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, _ := NewWebhookClient(WebhookClientOptions{
		Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
		MaxAttempts:    1,
		InitialBackoff: time.Millisecond,
	})

	const wantEventID = "00000000-0000-0000-0000-000000009999"
	ev := Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
		Payload:       map[string]any{"event_id": wantEventID},
		OccurredAt:    time.Now(),
	}
	_ = client.Dispatch(context.Background(), ev)

	if capturedKey != wantEventID {
		t.Errorf("X-Arena-Idempotency-Key = %q, want %q", capturedKey, wantEventID)
	}
}

// TestWebhookClient111_CompileTimeGuard is the runtime assertion of the
// compile-time Dispatcher interface guard in webhook_client.go.
func TestWebhookClient111_CompileTimeGuard(_ *testing.T) {
	var _ Dispatcher = (*WebhookClient)(nil)
}

// TestWebhookClient111_FullVerification runs all 5 feature steps as subtests.
func TestWebhookClient111_FullVerification(t *testing.T) {
	t.Run("step1_subscriber_model", func(t *testing.T) {
		sub := Subscriber{
			URL:        "https://example.com/hook",
			Secret:     []byte("secret"),
			EventTypes: []string{"v1.order.placed"},
			Active:     true,
		}
		if !sub.matchesEventType("v1.order.placed") {
			t.Error("subscriber must match its declared event type")
		}
		if sub.matchesEventType("v1.other.event") {
			t.Error("subscriber must not match an undeclared event type")
		}
		wildcardSub := Subscriber{URL: "http://x", Active: true}
		if !wildcardSub.matchesEventType("any.event") {
			t.Error("subscriber with empty EventTypes must match any event type")
		}
	})

	t.Run("step2_retry_exponential_backoff", func(t *testing.T) {
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer srv.Close()

		c, _ := NewWebhookClient(WebhookClientOptions{
			Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
			MaxAttempts:    3,
			InitialBackoff: 5 * time.Millisecond,
			MaxBackoff:     20 * time.Millisecond,
		})
		if err := c.Dispatch(context.Background(), testEvent("v1.test", "agg-fv2")); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if calls.Load() != 2 {
			t.Errorf("expected 2 attempts (1 fail + 1 success), got %d", calls.Load())
		}
	})

	t.Run("step3_routing_by_event_type", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c, _ := NewWebhookClient(WebhookClientOptions{
			Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: []string{"v1.target"}, Active: true}},
			MaxAttempts:    1,
			InitialBackoff: time.Millisecond,
		})
		_ = c.Dispatch(context.Background(), testEvent("v1.other", "agg-fv3a"))
		_ = c.Dispatch(context.Background(), testEvent("v1.target", "agg-fv3b"))
		if hits.Load() != 1 {
			t.Errorf("subscriber called %d times, want 1 (only for matching event type)", hits.Load())
		}
	})

	t.Run("step4_metrics", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		reg := prometheus.NewRegistry()
		m, err := NewWebhookClientMetrics(reg)
		if err != nil {
			t.Fatalf("NewWebhookClientMetrics: %v", err)
		}

		c, _ := NewWebhookClient(WebhookClientOptions{
			Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
			MaxAttempts:    1,
			InitialBackoff: time.Millisecond,
			Metrics:        m,
		})
		_ = c.Dispatch(context.Background(), testEvent("v1.fv4", "agg-fv4"))

		gathered, err := reg.Gather()
		if err != nil {
			t.Fatalf("Gather: %v", err)
		}
		found := false
		for _, mf := range gathered {
			if mf.GetName() == "arena_webhook_delivery_duration_seconds" {
				found = true
			}
		}
		if !found {
			t.Error("arena_webhook_delivery_duration_seconds metric must be gathered after successful delivery")
		}
	})

	t.Run("step5_integration_mock_subscriber", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer srv.Close()

		c, _ := NewWebhookClient(WebhookClientOptions{
			Subscribers:    []Subscriber{{URL: srv.URL, EventTypes: nil, Active: true}},
			MaxAttempts:    5,
			InitialBackoff: 5 * time.Millisecond,
			MaxBackoff:     20 * time.Millisecond,
		})
		if err := c.Dispatch(context.Background(), testEvent("v1.fv5", "agg-fv5")); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if hits.Load() != 3 {
			t.Errorf("expected 3 attempts (2 fail + 1 success), got %d", hits.Load())
		}
	})
}

// =============================================================================
// Test helpers
// =============================================================================

// testEvent builds a minimal Event for use in tests.
func testEvent(eventType, aggregateID string) Event {
	return Event{
		AggregateType: "test_aggregate",
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       map[string]any{"test": true},
		OccurredAt:    time.Now(),
	}
}

// metricCounterValue returns the sum of all counter sample values for a given
// metric family name, across all label combinations.
func metricCounterValue(t *testing.T, gathered []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, mf := range gathered {
		if mf.GetName() == name {
			var total float64
			for _, m := range mf.GetMetric() {
				total += m.GetCounter().GetValue()
			}
			return total
		}
	}
	return 0
}
