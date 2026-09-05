// dispatcher.go — the outbox.Dispatcher that delivers arena events to the
// migrated WordPress sites in Bil24's own webhook vocabulary (spec §9.1/§9.2,
// W1-B7c, feature #506).
//
// This half is PURE: it owns the platform-event → site-event mapping, the
// envelope, the signature and the HTTP delivery, and reaches the database only
// through the Loader interface (implemented next door in loader.go). That
// split is what keeps the encoder unit-testable — every mapping below is
// exercised against an httptest receiver and a fake Loader, with no pool.
//
// Delivery contract (spec §9.2):
//   - POST the envelope {id, created, type, data} as JSON.
//   - Headers: Content-Type, X-Arena-Event-Type, and X-Arena-Signature
//     ("sha256=" + hex HMAC-SHA256 of the body) when the subscriber has a
//     signing secret.
//   - 2xx is success. Anything else — and any transport failure — returns an
//     error so the outbox retries (MaxAttempts 30, ~24h of backoff).
//   - "Nothing to deliver" is NOT a failure: an event type the sites do not
//     consume, a channel with no registered site, an order with no exportable
//     tickets and a malformed payload all return nil so the outbox row is
//     marked processed instead of being retried for a day.
package bil24wire

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// Platform outbox event types this dispatcher consumes. The strings are the
// contract of the producing packages (htickets, ordering, hscanner, hcatalog);
// they are repeated here rather than imported because an adapter must not
// depend on the HTTP layer.
const (
	EventOrderPaid       = "v1.order.paid"
	EventOrderCancelled  = "v1.order.cancelled"
	EventTicketCancelled = "v1.ticket.cancelled"
	EventTicketRefunded  = "v1.ticket.refunded"
	EventTicketRevoked   = "v1.ticket.revoked"
	EventEventPublished  = "v1.event.published"
	EventEventUpdated    = "v1.event.updated"
	EventSessionUpdated  = "v1.session.updated"
	EventSessionCancel   = "v1.session.cancelled"
)

// Bil24 site-facing event types (spec §9.2). `test` is emitted by the
// registration endpoint (feature #507), never by the outbox.
const (
	SiteEventOrderPaid      = "order.paid"
	SiteEventOrderCancelled = "order.cancelled"
	SiteEventTicketRefunded = "ticket.refunded"
	SiteEventEventCreated   = "event.created"
	SiteEventEventChanged   = "event.changed"
	SiteEventEventDeleted   = "event.deleted"
	SiteEventTest           = "test"
)

// siteEventType maps a platform event type onto the Bil24 site event type.
// Returns ("", false) for everything the WordPress sites do not consume.
//
// Two collapses are deliberate. Every terminal ticket state — operator
// cancellation, provider refund, complimentary revocation — is `ticket.refunded`
// because Bil24's vocabulary has exactly one word for "this ticket no longer
// admits". And a session change is an `event.changed` because a Bil24 client
// models the session (actionEvent), not the arena event, as the sellable thing.
func siteEventType(platformEventType string) (string, bool) {
	switch platformEventType {
	case EventOrderPaid:
		return SiteEventOrderPaid, true
	case EventOrderCancelled:
		return SiteEventOrderCancelled, true
	case EventTicketCancelled, EventTicketRefunded, EventTicketRevoked:
		return SiteEventTicketRefunded, true
	case EventEventPublished:
		return SiteEventEventCreated, true
	case EventEventUpdated, EventSessionUpdated:
		return SiteEventEventChanged, true
	case EventSessionCancel:
		return SiteEventEventDeleted, true
	default:
		return "", false
	}
}

// SiteEventTypeFor is siteEventType for callers outside this package, flattened
// to "" for "not consumed by the sites". It exists so the producer packages can
// be tested against the mapping without exporting the dispatcher's internals:
// a catalog producer that emits a type this returns "" for is delivering into
// a void, and that is a defect worth failing a build over.
func SiteEventTypeFor(platformEventType string) string {
	mapped, _ := siteEventType(platformEventType)
	return mapped
}

// Envelope is the top-level webhook document every site receives (spec §9.2).
// Shape-identical to the MACS envelope on purpose: the WordPress plugin reads
// `type` to route and `id` to deduplicate a redelivery.
type Envelope struct {
	ID      int64  `json:"id"`
	Created string `json:"created"`
	Type    string `json:"type"`
	Data    any    `json:"data"`
}

// CancelledOrder is the `order.cancelled` data object: the id the site knows
// the order by, and the status word that voids it. Bil24 sends nothing else —
// a cancelled order has no money and no tickets left to describe.
type CancelledOrder struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// ActionEventRef is one element of the catalog events' data array. The site
// answers it by re-reading the session through the gateway (GET_ALL_ACTIONS),
// so the notification carries the id and nothing more.
type ActionEventRef struct {
	ActionEventID int64 `json:"actionEventId"`
}

// Subscriber is one registered WordPress site: where to deliver and how to
// sign. An empty SigningSecret means the site opted out of verification.
type Subscriber struct {
	CallbackURL   string
	SigningSecret string
}

// Loader is everything the dispatcher needs from the database. Keeping it an
// interface is not ceremony: it is what lets every mapping below be tested
// against a real HTTP receiver without a Postgres.
type Loader interface {
	// SubscriberByChannel returns the active bil24_wp subscriber of a sales
	// channel. ok=false means the channel has no WordPress site — the normal
	// case for arena-native channels, and a skip rather than an error.
	SubscriberByChannel(ctx context.Context, channelID uuid.UUID) (sub Subscriber, ok bool, err error)

	// SubscribersForEvent returns every bil24_wp subscriber of the channels
	// the event is published to. An empty slice is a legitimate answer.
	SubscribersForEvent(ctx context.Context, eventID uuid.UUID) ([]Subscriber, error)

	// Order builds the full Bil24 order (ticketList included) of one order
	// aggregate. (nil, nil) when the order has no exportable tickets.
	Order(ctx context.Context, orderID uuid.UUID) (*Order, error)

	// OrderRef returns the order's integer wire id (orders.system_id) and the
	// sales channel that sold it — the two facts an order.cancelled needs.
	OrderRef(ctx context.Context, orderID uuid.UUID) (systemID int64, channelID uuid.UUID, err error)

	// RefundedTicket builds the ticket.refunded payload for one ticket and
	// returns the sales channel of its order. (nil, ..., nil) when the ticket
	// is not exportable (never issued, or its order never completed).
	RefundedTicket(ctx context.Context, ticketID uuid.UUID) (t *RefundedTicket, channelID uuid.UUID, err error)

	// ActionEventIDs resolves the integer actionEventIds of the given
	// sessions; when sessionIDs is empty it resolves every session of the
	// event, so an `event.created` still names what became sellable.
	ActionEventIDs(ctx context.Context, eventID uuid.UUID, sessionIDs []uuid.UUID) ([]int64, error)
}

// Dispatcher implements outbox.Dispatcher for the Bil24-compatible WordPress
// webhooks.
type Dispatcher struct {
	loader Loader
	client *http.Client
}

// NewDispatcherWithLoader builds a Dispatcher over an arbitrary Loader. The
// production constructor is NewDispatcher (loader.go); this one exists for
// tests and for any future non-Postgres source of the same facts.
func NewDispatcherWithLoader(l Loader) *Dispatcher {
	return &Dispatcher{
		loader: l,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Dispatch implements outbox.Dispatcher.
func (d *Dispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	siteType, ok := siteEventType(ev.EventType)
	if !ok {
		return nil // not a WordPress event; let the outbox mark it processed
	}
	// A nil Dispatcher is what NewDispatcher answers for a nil pool, and it
	// reaches here as a non-nil interface holding a nil pointer — so the guard
	// has to be here, not at the call site.
	if d == nil || d.loader == nil {
		return nil
	}

	switch ev.EventType {
	case EventOrderPaid:
		return d.dispatchOrderPaid(ctx, ev, siteType)
	case EventOrderCancelled:
		return d.dispatchOrderCancelled(ctx, ev, siteType)
	case EventTicketCancelled, EventTicketRefunded, EventTicketRevoked:
		return d.dispatchTicketRefunded(ctx, ev, siteType)
	default:
		return d.dispatchCatalog(ctx, ev, siteType)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-family handlers
// ─────────────────────────────────────────────────────────────────────────────

// dispatchOrderPaid delivers the complete Bil24 order, ticketList included:
// order.paid is the one event that must let a WordPress shop materialise the
// whole sale without calling back.
func (d *Dispatcher) dispatchOrderPaid(ctx context.Context, ev outbox.Event, siteType string) error {
	orderID, ok := payloadUUID(ev.Payload, "order_id")
	if !ok {
		return nil
	}
	channelID, ok := payloadUUID(ev.Payload, "channel_id")
	if !ok {
		// Older rows predate channel_id in the payload; the order knows it.
		_, ch, err := d.loader.OrderRef(ctx, orderID)
		if err != nil {
			return nil
		}
		channelID = ch
	}
	sub, ok, err := d.loader.SubscriberByChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: subscriber for channel %s: %w", channelID, err)
	}
	if !ok {
		return nil
	}

	order, err := d.loader.Order(ctx, orderID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: build order %s: %w", orderID, err)
	}
	if order == nil {
		return nil // nothing issued — no sale the site could record
	}
	return d.deliver(ctx, sub, envelope(ev, siteType, *order))
}

// dispatchOrderCancelled delivers the two-key void notice.
func (d *Dispatcher) dispatchOrderCancelled(ctx context.Context, ev outbox.Event, siteType string) error {
	orderID, ok := payloadUUID(ev.Payload, "order_id")
	if !ok {
		return nil
	}
	systemID, channelID, err := d.loader.OrderRef(ctx, orderID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: order ref %s: %w", orderID, err)
	}
	if payloadChannel, ok := payloadUUID(ev.Payload, "channel_id"); ok {
		channelID = payloadChannel
	}
	sub, ok, err := d.loader.SubscriberByChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: subscriber for channel %s: %w", channelID, err)
	}
	if !ok {
		return nil
	}
	data := CancelledOrder{ID: systemID, Status: StatusCancelled}
	return d.deliver(ctx, sub, envelope(ev, siteType, data))
}

// dispatchTicketRefunded delivers the per-ticket void notice for all three
// terminal ticket transitions.
func (d *Dispatcher) dispatchTicketRefunded(ctx context.Context, ev outbox.Event, siteType string) error {
	ticketID, ok := payloadUUID(ev.Payload, "ticket_id")
	if !ok {
		return nil
	}
	ticket, channelID, err := d.loader.RefundedTicket(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: build refunded ticket %s: %w", ticketID, err)
	}
	if ticket == nil {
		return nil
	}
	sub, ok, err := d.loader.SubscriberByChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: subscriber for channel %s: %w", channelID, err)
	}
	if !ok {
		return nil
	}
	return d.deliver(ctx, sub, envelope(ev, siteType, *ticket))
}

// dispatchCatalog fans an event/session change out to every WordPress site the
// event is published to. The data array names the affected actionEvents; the
// site re-reads them through the gateway, which is why the payload carries no
// catalog content and can therefore never go stale between producer and
// consumer.
func (d *Dispatcher) dispatchCatalog(ctx context.Context, ev outbox.Event, siteType string) error {
	eventID, ok := payloadUUID(ev.Payload, "event_id")
	if !ok {
		return nil
	}
	subs, err := d.loader.SubscribersForEvent(ctx, eventID)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: subscribers for event %s: %w", eventID, err)
	}
	if len(subs) == 0 {
		return nil
	}

	ids, err := d.loader.ActionEventIDs(ctx, eventID, payloadSessionIDs(ev.Payload))
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: action event ids for %s: %w", eventID, err)
	}
	refs := make([]ActionEventRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, ActionEventRef{ActionEventID: id})
	}
	env := envelope(ev, siteType, refs)

	// One failing site must not cost the others their notification, and must
	// still make the outbox retry — so deliver to all, then report the union.
	var failures []error
	for _, sub := range subs {
		if err := d.deliver(ctx, sub, env); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Envelope + delivery
// ─────────────────────────────────────────────────────────────────────────────

// envelope wraps one data object. `created` is the outbox row's occurrence
// time in UTC — the moment the fact became true, not the moment we managed to
// deliver it, so a retry after an outage does not backdate the site's clock.
func envelope(ev outbox.Event, siteType string, data any) Envelope {
	occurred := ev.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	return Envelope{
		ID:      envelopeID(ev),
		Created: occurred.UTC().Format(time.RFC3339),
		Type:    siteType,
		Data:    data,
	}
}

// envelopeID derives a stable positive integer from the outbox row uuid — the
// low 63 bits of its first 8 bytes, the same derivation MACS uses. It must key
// off the EVENT, never off the order or ticket, because a site deduplicates on
// it and one order produces several envelopes.
func envelopeID(ev outbox.Event) int64 {
	if ev.ID == "" {
		return 0
	}
	id, err := uuid.Parse(ev.ID)
	if err != nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(id[:8]) >> 1)
}

// deliver POSTs one envelope to one subscriber. Returns an error on transport
// failure or a non-2xx status so the outbox retries.
func (d *Dispatcher) deliver(ctx context.Context, sub Subscriber, env Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Arena-Event-Type", env.Type)
	if sub.SigningSecret != "" {
		req.Header.Set("X-Arena-Signature", Sign(body, sub.SigningSecret))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("bil24wire dispatcher: post to %s: %w", sub.CallbackURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bil24wire dispatcher: %s returned %d", sub.CallbackURL, resp.StatusCode)
	}
	return nil
}

// Sign is the X-Arena-Signature value for a body: "sha256=" plus the hex
// HMAC-SHA256 under the subscriber's secret. Exported so the registration
// endpoint's `test` ping (feature #507) signs identically — one signature
// scheme, one implementation.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ─────────────────────────────────────────────────────────────────────────────
// Payload helpers
// ─────────────────────────────────────────────────────────────────────────────

// payloadUUID reads a uuid-valued key from an outbox payload. The payload is a
// round-tripped JSON object, so every id arrives as a string.
func payloadUUID(payload map[string]any, key string) (uuid.UUID, bool) {
	s, _ := payload[key].(string)
	if s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, id != uuid.Nil
}

// payloadSessionIDs reads the optional session_ids array, tolerating both the
// producer's []string and JSON's []any. A single session_id key (which
// v1.session.cancelled carries) counts as a one-element list.
func payloadSessionIDs(payload map[string]any) []uuid.UUID {
	var out []uuid.UUID
	appendID := func(v any) {
		s, _ := v.(string)
		if s == "" {
			return
		}
		if id, err := uuid.Parse(s); err == nil && id != uuid.Nil {
			out = append(out, id)
		}
	}
	switch raw := payload["session_ids"].(type) {
	case []any:
		for _, v := range raw {
			appendID(v)
		}
	case []string:
		for _, v := range raw {
			appendID(v)
		}
	}
	if len(out) == 0 {
		appendID(payload["session_id"])
	}
	return out
}

// Compile-time interface guard.
var _ outbox.Dispatcher = (*Dispatcher)(nil)
