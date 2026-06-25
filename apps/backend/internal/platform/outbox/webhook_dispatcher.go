// Package outbox — WebhookDispatcher: HTTP delivery with HMAC-SHA256 signing.
//
// Feature #110 — Outbox dispatcher:
//
//	Step 2: Stable JSON serialisation (encoding/json sorts map keys
//	        alphabetically; using a struct envelope eliminates all
//	        map-key ambiguity in the outer envelope fields).
//	Step 3: X-Arena-Signature: sha256=<hex> HMAC-SHA256 header.
//	Step 4: HTTP POST delivery to a configurable webhook URL.
//
// The WebhookDispatcher implements the Dispatcher interface and is wired
// into OutboxEventsDispatcher (see outbox_events_dispatcher.go) to deliver
// outbox events to HTTP subscribers.
//
// Replay safety is guaranteed by the OutboxEventsDispatcher loop:
// once processed_at is set by MarkDispatched, ClaimNext never returns
// the same row again. Subscribers can further deduplicate using the
// X-Arena-Event-ID header which carries the outbox_events row UUID.
package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// WebhookDispatcherOptions configures a WebhookDispatcher.
type WebhookDispatcherOptions struct {
	// TargetURL is the HTTP endpoint to POST events to. Required.
	TargetURL string

	// SigningSecret is the raw bytes of the HMAC-SHA256 key used to sign
	// each payload. If nil or empty, no X-Arena-Signature header is added.
	SigningSecret []byte

	// Client is the HTTP client used for delivery.
	// Defaults to a client with a 10-second timeout when nil.
	Client *http.Client
}

// WebhookDispatcher implements Dispatcher by POSTing each outbox event as
// a signed JSON payload to a configurable HTTP endpoint.
//
// Every POST includes:
//
//	Content-Type:          application/json
//	X-Arena-Event-Type:    <event_type>
//	X-Arena-Aggregate-Type: <aggregate_type>
//	X-Arena-Event-ID:      <outbox row event_id from payload, if present>
//	X-Arena-Signature:     sha256=<hex> (only when SigningSecret is configured)
//
// The body is a stable JSON object (envelope fields in struct-declaration
// order; payload map keys sorted alphabetically by encoding/json).
type WebhookDispatcher struct {
	url    string
	secret []byte
	client *http.Client
}

// NewWebhookDispatcher validates opts and returns a ready-to-use dispatcher.
// Returns an error when TargetURL is empty.
func NewWebhookDispatcher(opts WebhookDispatcherOptions) (*WebhookDispatcher, error) {
	if opts.TargetURL == "" {
		return nil, errors.New("outbox: WebhookDispatcher: TargetURL is required")
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WebhookDispatcher{
		url:    opts.TargetURL,
		secret: opts.SigningSecret,
		client: client,
	}, nil
}

// webhookEnvelope is the stable JSON structure written to the POST body.
//
// Using a Go struct (rather than map[string]any) ensures envelope-level
// fields are always serialised in declaration order by encoding/json,
// satisfying the "stable JSON ordering" requirement of feature #110 step 2.
// The embedded Payload map is serialised with alphabetically-sorted keys
// by encoding/json (standard Go behaviour).
type webhookEnvelope struct {
	AggregateID   string         `json:"aggregate_id"`
	AggregateType string         `json:"aggregate_type"`
	EventID       string         `json:"event_id,omitempty"`
	EventType     string         `json:"event_type"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Payload       map[string]any `json:"payload"`
}

// Dispatch serialises ev into a stable JSON envelope, optionally signs it
// with HMAC-SHA256, and POSTs the body to the configured URL.
//
// A 4xx or 5xx HTTP response is returned as a non-nil error, triggering
// the at-least-once retry path inside OutboxEventsDispatcher.
func (d *WebhookDispatcher) Dispatch(ctx context.Context, ev Event) error {
	env := webhookEnvelope{
		AggregateID:   ev.AggregateID,
		AggregateType: ev.AggregateType,
		EventType:     ev.EventType,
		OccurredAt:    ev.OccurredAt.UTC(),
		Payload:       sortedMapCopy(ev.Payload),
	}
	// Propagate event_id embedded by the writer in the payload (if any).
	if id, _ := ev.Payload["event_id"].(string); id != "" {
		env.EventID = id
	}

	body, err := StableJSONPayload(env)
	if err != nil {
		return fmt.Errorf("outbox webhook: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("outbox webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Arena-Event-Type", ev.EventType)
	req.Header.Set("X-Arena-Aggregate-Type", ev.AggregateType)
	if env.EventID != "" {
		req.Header.Set("X-Arena-Event-ID", env.EventID)
	}
	if len(d.secret) > 0 {
		sig := ComputeHMAC(body, d.secret)
		req.Header.Set("X-Arena-Signature", "sha256="+sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("outbox webhook: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("outbox webhook: server returned %d", resp.StatusCode)
	}
	return nil
}

// TargetURL returns the webhook endpoint this dispatcher delivers to.
func (d *WebhookDispatcher) TargetURL() string { return d.url }

// HasSigningSecret reports whether this dispatcher will add an
// X-Arena-Signature header to outgoing requests.
func (d *WebhookDispatcher) HasSigningSecret() bool { return len(d.secret) > 0 }

// Compile-time interface guard.
var _ Dispatcher = (*WebhookDispatcher)(nil)

// =============================================================================
// Public helpers (used by tests and subscriber implementations)
// =============================================================================

// StableJSONPayload serialises v to JSON with deterministic key ordering.
//
// encoding/json sorts map[string]any keys alphabetically, so this is
// already guaranteed for payload maps. Using a struct envelope (see
// webhookEnvelope) ensures the outer field order is also deterministic.
// This function is the named seam for feature #110 step 2.
func StableJSONPayload(v any) ([]byte, error) {
	return json.Marshal(v)
}

// StableJSONMap serialises a flat map[string]any with keys sorted
// alphabetically. Identical input maps always produce identical output bytes.
func StableJSONMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make(map[string]any, len(m))
	for _, k := range keys {
		ordered[k] = m[k]
	}
	return json.Marshal(ordered)
}

// ComputeHMAC returns the hex-encoded HMAC-SHA256 digest of body using secret.
func ComputeHMAC(body, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHMACSignature reports whether sigHeader (the value of the
// X-Arena-Signature request header) is the correct HMAC-SHA256 of body
// computed with secret.
//
// Example subscriber validation:
//
//	ok := outbox.VerifyHMACSignature(body, []byte(secret), r.Header.Get("X-Arena-Signature"))
func VerifyHMACSignature(body, secret []byte, sigHeader string) bool {
	const prefix = "sha256="
	if len(sigHeader) <= len(prefix) {
		return false
	}
	got := sigHeader[len(prefix):]
	want := ComputeHMAC(body, secret)
	// Constant-time comparison prevents timing attacks.
	return hmac.Equal([]byte(got), []byte(want))
}

// =============================================================================
// Internal helpers
// =============================================================================

// sortedMapCopy returns a shallow copy of m with map-key iteration order
// normalised. encoding/json already sorts map keys, so this copy is mainly
// defensive against callers mutating the original payload after Dispatch.
func sortedMapCopy(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
