// Package outbox — WebhookClient: multi-subscriber HTTP delivery with
// per-subscriber retry, exponential backoff, dead-letter, and Prometheus metrics.
//
// Feature #111 — Webhook delivery client:
//
//	Step 1: Subscriber model (event_types[], url, secret, active)
//	Step 2: HTTP client with timeout and retry/exponential-backoff policy
//	Step 3: Subscriber routing/filtering by event type
//	Step 4: Metrics (delivery latency, retry count, dead-letter count)
//	Step 5: Integration test with mock subscriber (see webhook_client_111_test.go)
//
// WebhookClient implements the Dispatcher interface and can be wired directly
// into OutboxEventsDispatcher.Dispatcher, replacing the single-target
// WebhookDispatcher for production multi-tenant webhook fan-out.
//
// Delivery semantics:
//   - For each incoming Event, all active Subscribers whose EventTypes list
//     includes the event's EventType (or whose list is empty, meaning wildcard)
//     receive the event.
//   - Per subscriber, delivery is attempted up to MaxAttempts times with
//     exponential backoff (InitialBackoff * 2^n, capped at MaxBackoff).
//   - If all attempts fail, the subscriber delivery is dead-lettered: the error
//     is logged, the DeadLetterCount metric is incremented, and processing
//     continues to the next subscriber.
//   - The overall Dispatch call returns nil when the routing pass completes,
//     regardless of individual subscriber dead-letters.  This allows the
//     OutboxEventsDispatcher to mark the outbox row as dispatched even when
//     some subscribers are persistently unavailable.
package outbox

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// Subscriber model
// =============================================================================

// Subscriber defines a single webhook endpoint that receives outbox events.
//
// A Subscriber is matched against an incoming Event if:
//   - Active is true, AND
//   - EventTypes is empty (wildcard — receives all event types), OR
//     EventTypes contains the Event's EventType.
type Subscriber struct {
	// URL is the HTTP endpoint to POST events to. Required.
	URL string

	// Secret is the raw bytes of the HMAC-SHA256 signing key.
	// When non-empty, every POST carries an X-Arena-Signature: sha256=<hex>
	// header so the subscriber can verify the payload integrity.
	// When empty or nil, no signature header is added.
	Secret []byte

	// EventTypes is the list of event_type values this subscriber is
	// interested in.  An empty or nil slice means the subscriber receives
	// all event types (wildcard behaviour).
	EventTypes []string

	// Active controls whether the subscriber participates in event delivery.
	// Inactive subscribers are skipped entirely by WebhookClient.Dispatch.
	Active bool
}

// matchesEventType reports whether s is interested in the given eventType.
// A subscriber with an empty EventTypes slice matches every event type.
func (s Subscriber) matchesEventType(eventType string) bool {
	if len(s.EventTypes) == 0 {
		return true
	}
	for _, t := range s.EventTypes {
		if t == eventType {
			return true
		}
	}
	return false
}

// =============================================================================
// WebhookClientMetrics
// =============================================================================

// WebhookClientMetrics groups the three Prometheus collectors used by
// WebhookClient.  All fields are optional — nil values disable the
// corresponding metric.
//
// Recommended label cardinality: use stable subscriber URLs and event type
// strings. Avoid high-cardinality labels (e.g. per-event UUIDs).
type WebhookClientMetrics struct {
	// DeliveryDuration observes the round-trip latency of each successful
	// HTTP delivery attempt.
	// Suggested metric name: arena_webhook_delivery_duration_seconds
	// Labels: subscriber_url, event_type
	DeliveryDuration *prometheus.HistogramVec

	// RetryCount counts the number of additional delivery attempts beyond
	// the first (i.e. incremented once per retry, not on the initial attempt).
	// Suggested metric name: arena_webhook_retry_total
	// Labels: subscriber_url, event_type
	RetryCount *prometheus.CounterVec

	// DeadLetterCount counts subscriber deliveries that exhausted all retry
	// attempts without success.
	// Suggested metric name: arena_webhook_dead_letter_total
	// Labels: subscriber_url, event_type
	DeadLetterCount *prometheus.CounterVec
}

// NewWebhookClientMetrics constructs and registers the three standard
// WebhookClient Prometheus collectors against the supplied registry.
// Pass prometheus.DefaultRegisterer in production; a fresh
// prometheus.NewRegistry() in tests.
func NewWebhookClientMetrics(reg prometheus.Registerer) (*WebhookClientMetrics, error) {
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "arena",
		Subsystem: "webhook",
		Name:      "delivery_duration_seconds",
		Help:      "Round-trip latency of each successful webhook delivery attempt.",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"subscriber_url", "event_type"})

	retry := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "arena",
		Subsystem: "webhook",
		Name:      "retry_total",
		Help:      "Number of webhook delivery retries (attempts beyond the first).",
	}, []string{"subscriber_url", "event_type"})

	dl := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "arena",
		Subsystem: "webhook",
		Name:      "dead_letter_total",
		Help:      "Number of webhook deliveries that exhausted all retry attempts.",
	}, []string{"subscriber_url", "event_type"})

	for _, c := range []prometheus.Collector{dur, retry, dl} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("outbox webhook client: register metric: %w", err)
		}
	}
	return &WebhookClientMetrics{
		DeliveryDuration: dur,
		RetryCount:       retry,
		DeadLetterCount:  dl,
	}, nil
}

// =============================================================================
// WebhookClientOptions
// =============================================================================

// WebhookClientOptions configures a WebhookClient.
type WebhookClientOptions struct {
	// Subscribers is the list of registered webhook subscribers.
	// An empty slice means no events are delivered.
	Subscribers []Subscriber

	// MaxAttempts is the maximum number of delivery attempts per subscriber
	// before the delivery is dead-lettered.  Minimum 1.  Defaults to 3.
	MaxAttempts int

	// InitialBackoff is the wait before the first retry.  Defaults to 1s.
	InitialBackoff time.Duration

	// MaxBackoff caps the exponential growth of inter-retry delays.
	// Defaults to 30s.
	MaxBackoff time.Duration

	// HTTPTimeout is the per-request timeout for each delivery attempt.
	// Defaults to 10s.
	HTTPTimeout time.Duration

	// Metrics holds optional Prometheus collectors.  Nil collectors are
	// silently skipped.
	Metrics *WebhookClientMetrics

	// Logger receives structured delivery log records.
	// Defaults to slog.Default() when nil.
	Logger *slog.Logger

	// httpClient overrides the net/http client used for delivery.
	// For testing only; production code should leave this nil.
	httpClient *http.Client
}

// =============================================================================
// WebhookClient
// =============================================================================

// WebhookClient implements Dispatcher by routing each incoming Event to all
// active Subscribers that have expressed interest in the event's EventType.
//
// Delivery to each subscriber is tried up to MaxAttempts times with
// exponential backoff.  Exhausted subscribers are dead-lettered (logged +
// metric) and skipped; the overall Dispatch call always returns nil so the
// outbox dispatcher can mark the row as processed.
type WebhookClient struct {
	subscribers    []Subscriber
	maxAttempts    int
	initBackoff    time.Duration
	maxBackoff     time.Duration
	httpTimeout    time.Duration
	metrics        *WebhookClientMetrics
	logger         *slog.Logger
	httpClientFunc func() *http.Client
}

// NewWebhookClient validates opts and returns a ready-to-use WebhookClient.
func NewWebhookClient(opts WebhookClientOptions) (*WebhookClient, error) {
	maxAttempts := opts.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	initBackoff := opts.InitialBackoff
	if initBackoff <= 0 {
		initBackoff = time.Second
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	httpTimeout := opts.HTTPTimeout
	if httpTimeout <= 0 {
		httpTimeout = 10 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build httpClientFunc so tests can inject a custom client.
	var hcFunc func() *http.Client
	if opts.httpClient != nil {
		hcFunc = func() *http.Client { return opts.httpClient }
	} else {
		hcFunc = func() *http.Client {
			return &http.Client{Timeout: httpTimeout}
		}
	}

	return &WebhookClient{
		subscribers:    opts.Subscribers,
		maxAttempts:    maxAttempts,
		initBackoff:    initBackoff,
		maxBackoff:     maxBackoff,
		httpTimeout:    httpTimeout,
		metrics:        opts.Metrics,
		logger:         logger.With(slog.String("component", "webhook_client")),
		httpClientFunc: hcFunc,
	}, nil
}

// Dispatch routes ev to all matching active subscribers and performs delivery
// with per-subscriber retry/backoff.  Always returns nil (dead-lettered
// subscribers are logged but do not propagate as errors).
func (c *WebhookClient) Dispatch(ctx context.Context, ev Event) error {
	for _, sub := range c.subscribers {
		if !sub.Active {
			continue
		}
		if !sub.matchesEventType(ev.EventType) {
			continue
		}
		c.deliverToSubscriber(ctx, sub, ev)
	}
	return nil
}

// deliverToSubscriber attempts to deliver ev to sub up to c.maxAttempts times.
// On exhaustion the delivery is dead-lettered.
func (c *WebhookClient) deliverToSubscriber(ctx context.Context, sub Subscriber, ev Event) {
	var lastErr error
	client := c.httpClientFunc()

	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		// Backoff before each retry (not before the first attempt).
		if attempt > 0 {
			backoff := c.calcBackoff(attempt)
			c.logger.Debug("webhook client: backing off before retry",
				slog.String("subscriber_url", sub.URL),
				slog.String("event_type", ev.EventType),
				slog.Int("attempt", attempt),
				slog.String("backoff", backoff.String()),
			)
			if c.metrics != nil && c.metrics.RetryCount != nil {
				c.metrics.RetryCount.WithLabelValues(sub.URL, ev.EventType).Inc()
			}
			select {
			case <-ctx.Done():
				c.logger.Warn("webhook client: context cancelled during backoff",
					slog.String("subscriber_url", sub.URL),
					slog.String("event_type", ev.EventType),
				)
				return
			case <-time.After(backoff):
			}
		}

		start := time.Now()
		err := c.doHTTPPost(ctx, client, sub, ev)
		elapsed := time.Since(start)

		if err == nil {
			// Success — record latency and return.
			if c.metrics != nil && c.metrics.DeliveryDuration != nil {
				c.metrics.DeliveryDuration.WithLabelValues(sub.URL, ev.EventType).
					Observe(elapsed.Seconds())
			}
			c.logger.Info("webhook client: event delivered",
				slog.String("subscriber_url", sub.URL),
				slog.String("event_type", ev.EventType),
				slog.Int("attempt", attempt+1),
				slog.String("latency", elapsed.String()),
			)
			return
		}

		lastErr = err
		c.logger.Warn("webhook client: delivery attempt failed",
			slog.String("subscriber_url", sub.URL),
			slog.String("event_type", ev.EventType),
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", c.maxAttempts),
			slog.String("error", err.Error()),
		)
	}

	// All attempts exhausted — dead-letter this subscriber delivery.
	if c.metrics != nil && c.metrics.DeadLetterCount != nil {
		c.metrics.DeadLetterCount.WithLabelValues(sub.URL, ev.EventType).Inc()
	}
	c.logger.Error("webhook client: delivery dead-lettered after max attempts",
		slog.String("subscriber_url", sub.URL),
		slog.String("event_type", ev.EventType),
		slog.Int("max_attempts", c.maxAttempts),
		slog.String("last_error", lastErr.Error()),
	)
}

// doHTTPPost performs a single HTTP POST to sub.URL with the event payload.
func (c *WebhookClient) doHTTPPost(ctx context.Context, client *http.Client, sub Subscriber, ev Event) error {
	env := webhookEnvelope{
		AggregateID:   ev.AggregateID,
		AggregateType: ev.AggregateType,
		EventType:     ev.EventType,
		OccurredAt:    ev.OccurredAt.UTC(),
		Payload:       sortedMapCopy(ev.Payload),
	}
	if id, _ := ev.Payload["event_id"].(string); id != "" {
		env.EventID = id
	}

	body, err := StableJSONPayload(env)
	if err != nil {
		return fmt.Errorf("webhook client: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook client: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Arena-Event-Type", ev.EventType)
	req.Header.Set("X-Arena-Aggregate-Type", ev.AggregateType)
	if env.EventID != "" {
		req.Header.Set("X-Arena-Event-ID", env.EventID)
		// Idempotency key header for subscriber-side deduplication.
		req.Header.Set("X-Arena-Idempotency-Key", env.EventID)
	}
	if len(sub.Secret) > 0 {
		sig := ComputeHMAC(body, sub.Secret)
		req.Header.Set("X-Arena-Signature", "sha256="+sig)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook client: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook client: subscriber %s returned %d", sub.URL, resp.StatusCode)
	}
	return nil
}

// calcBackoff returns the wait duration before the n-th retry (1-indexed).
// Uses full exponential backoff: InitialBackoff * 2^(n-1), capped at MaxBackoff.
func (c *WebhookClient) calcBackoff(n int) time.Duration {
	exp := math.Pow(2, float64(n-1))
	d := time.Duration(float64(c.initBackoff) * exp)
	if d > c.maxBackoff {
		d = c.maxBackoff
	}
	return d
}

// Subscribers returns a copy of the configured subscriber list.
// Useful for health-check endpoints and admin inspection.
func (c *WebhookClient) Subscribers() []Subscriber {
	out := make([]Subscriber, len(c.subscribers))
	copy(out, c.subscribers)
	return out
}

// Compile-time interface guard.
var _ Dispatcher = (*WebhookClient)(nil)
