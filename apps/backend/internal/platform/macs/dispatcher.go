// dispatcher.go implements the outbox.Dispatcher for MACS webhook delivery.
//
// AB-50c — MACS webhooks + HMAC signing + outbox delivery.
//
// The MACSDispatcher listens for ticket lifecycle events from the outbox and
// posts MACS-shaped JSON envelopes to the per-org MACS webhook subscriber.
// It only handles the event types the MACS system cares about; all others
// are silently skipped (returning nil so the outbox row is marked processed).
package macs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// MACS outbox event type constants — must match the values written by the
// scanner/ticket domain when appending events to outbox_events.
const (
	// EventOrderPaid is the order-aggregate "the sale is complete" event
	// (spec §9.1, feature #488). Since W1-Ma (spec §10 M1) it is the
	// PRIMARY source of the MACS `order.paid` webhook: one envelope per
	// order carrying the whole ticketList.
	EventOrderPaid       = "v1.order.paid"
	EventTicketIssued    = "v1.scanner.ticket.issued"
	EventTicketRefunded  = "v1.ticket.refunded"
	EventTicketCancelled = "v1.ticket.cancelled"
	EventTicketRevoked   = "v1.ticket.revoked"
)

// macsEventType maps a platform event type to the corresponding MACS webhook
// event type. Returns ("", false) for event types MACS does not handle.
//
// v1.scanner.ticket.issued still maps to order.paid, but only tickets issued
// WITHOUT an order aggregate (complimentary) are actually delivered through
// it — see Dispatch.
func macsEventType(platformEventType string) (string, bool) {
	switch platformEventType {
	case EventOrderPaid, EventTicketIssued:
		return "order.paid", true
	case EventTicketRefunded, EventTicketCancelled, EventTicketRevoked:
		return "ticket.refunded", true
	default:
		return "", false
	}
}

// macsEnvelope is the top-level MACS webhook payload.
type macsEnvelope struct {
	ID      int64  `json:"id"`
	Created string `json:"created"`
	Type    string `json:"type"`
	Data    any    `json:"data"`
}

// macsEventData is the `data` object of every envelope: the SAME Ticket
// shape as the JSON export (MACS's Pydantic Ticket model requires id,
// seatId, barcode and actionEvent{id,cityName,venueName,actionName,
// actionLegalOwner,showTime}; a bare {ticketId} is rejected with 422).
// orderId lets the receiver correlate order.paid events of one order.
type macsEventData struct {
	Ticket
	OrderID int64  `json:"orderId"`
	Reason  string `json:"reason,omitempty"`
}

// Dispatcher implements outbox.Dispatcher for MACS webhook delivery.
// It queries the database for the org-level MACS subscriber and the ticket's
// system_ticket_id, then POSTs an MACS-shaped envelope to the subscriber's
// callback URL with HMAC-SHA256 signing.
type Dispatcher struct {
	pool   *pgxpool.Pool
	client *http.Client
}

// NewDispatcher creates a new MACS Dispatcher backed by the given pool.
// Uses a 10-second HTTP client timeout for delivery.
func NewDispatcher(pool *pgxpool.Pool) *Dispatcher {
	return &Dispatcher{
		pool:   pool,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Dispatch implements outbox.Dispatcher.
//
// Routing (spec §10 M1):
//   - v1.order.paid          → order.paid with the WHOLE order (ticketList).
//   - v1.scanner.ticket.issued → order.paid with a synthetic single-ticket
//     order, but ONLY for tickets that carry no order aggregate
//     (complimentary); ordinary sales are covered by v1.order.paid and are
//     skipped here so MACS never sees the same sale twice.
//   - v1.ticket.{refunded,cancelled,revoked} → ticket.refunded (unchanged).
//   - anything else → nil (skip without retry).
//
// Returns a non-nil error only for delivery failures (so the outbox retries).
// Missing subscribers, missing system IDs, or malformed payloads return nil
// (no subscriber = nothing to deliver; malformed = cannot be retried anyway).
func (d *Dispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	switch ev.EventType {
	case EventOrderPaid:
		return d.dispatchOrderPaid(ctx, ev)
	case EventTicketIssued:
		return d.dispatchComplimentaryPaid(ctx, ev)
	case EventTicketRefunded, EventTicketCancelled, EventTicketRevoked:
		return d.dispatchTicketRefunded(ctx, ev)
	default:
		// Not a MACS event — let the outbox mark it processed.
		return nil
	}
}

// dispatchOrderPaid delivers one order.paid envelope for a whole order
// aggregate: data = the MACS Order {id, status:"PAID", ticketList:[…]}
// projected by orderexport (spec §10 M1).
func (d *Dispatcher) dispatchOrderPaid(ctx context.Context, ev outbox.Event) error {
	orderIDStr, _ := ev.Payload["order_id"].(string)
	if orderIDStr == "" {
		return nil // malformed payload — retrying will not help
	}
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		return nil
	}

	orgID, err := d.resolveOrgID(ctx, ev.Payload)
	if err != nil {
		return nil // cannot route
	}
	sub, err := d.getMACSSubscriber(ctx, orgID)
	if err != nil {
		return nil // no subscriber registered for this org
	}

	order, err := QueryAndBuildOrder(ctx, d.pool, orderID)
	if err != nil {
		return fmt.Errorf("macs dispatcher: build order %s: %w", orderID, err)
	}
	if order == nil {
		// Nothing exportable (unpaid, or no tickets issued) — nothing MACS
		// could store.
		return nil
	}

	return d.post(ctx, sub.CallbackURL, sub.SigningSecret, macsEnvelope{
		ID:      envelopeID(ev, order.ID),
		Created: ev.OccurredAt.UTC().Format(time.RFC3339),
		Type:    "order.paid",
		Data:    *order,
	})
}

// dispatchComplimentaryPaid delivers order.paid for a ticket issued WITHOUT an
// order aggregate (complimentary): a synthetic order of exactly one ticket.
// Tickets that do belong to an order are skipped — v1.order.paid covers them.
func (d *Dispatcher) dispatchComplimentaryPaid(ctx context.Context, ev outbox.Event) error {
	ticketIDStr, _ := ev.Payload["ticket_id"].(string)
	if ticketIDStr == "" {
		return nil
	}
	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		return nil
	}

	hasOrder, err := d.ticketHasOrder(ctx, ticketID)
	if err != nil {
		return nil // ticket unknown; nothing to route
	}
	if hasOrder {
		// The order aggregate publishes v1.order.paid for this sale.
		return nil
	}

	sessionID, err := d.resolveSessionID(ctx, ev.Payload, ticketID)
	if err != nil {
		return nil
	}
	orgID, err := d.getOrgIDForSession(ctx, sessionID)
	if err != nil {
		return nil
	}
	sub, err := d.getMACSSubscriber(ctx, orgID)
	if err != nil {
		return nil
	}

	// QueryAndBuildTicket returns the ticket together with its order header,
	// and — because the projection is scoped to this one ticket — that order
	// already carries exactly this ticket in ticketList.
	ticket, order, err := QueryAndBuildTicket(ctx, d.pool, ticketID)
	if err != nil {
		return fmt.Errorf("macs dispatcher: build ticket %s: %w", ticketID, err)
	}
	if ticket == nil || order == nil {
		return nil
	}

	return d.post(ctx, sub.CallbackURL, sub.SigningSecret, macsEnvelope{
		ID:      envelopeID(ev, order.ID),
		Created: ev.OccurredAt.UTC().Format(time.RFC3339),
		Type:    "order.paid",
		Data:    *order,
	})
}

// dispatchTicketRefunded delivers ticket.refunded — the per-ticket envelope
// whose data is the MACS Ticket shape. Unchanged by M1.
func (d *Dispatcher) dispatchTicketRefunded(ctx context.Context, ev outbox.Event) error {
	const macsType = "ticket.refunded"

	ticketIDStr, _ := ev.Payload["ticket_id"].(string)
	if ticketIDStr == "" {
		// Malformed payload — skip; retrying will not help.
		return nil
	}
	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		return nil // malformed UUID; skip
	}
	// session_id travels in v1.ticket.cancelled / ticket.issued payloads but
	// NOT in v1.ticket.refunded (provider webhook) or v1.ticket.revoked
	// (complimentary) — resolve it from the ticket so those two refund
	// sources reach MACS too (pass-6 review finding).
	sessionID, err := d.resolveSessionID(ctx, ev.Payload, ticketID)
	if err != nil {
		return nil // ticket unknown; nothing to route
	}

	// Resolve org from session.
	orgID, err := d.getOrgIDForSession(ctx, sessionID)
	if err != nil {
		// If the session is not found we can't route; skip.
		return nil
	}

	// Look up active MACS subscriber for org.
	sub, err := d.getMACSSubscriber(ctx, orgID)
	if err != nil {
		// No subscriber registered for this org; nothing to do.
		return nil
	}

	// Get MACS stable integer id for the ticket.
	systemTicketID, err := d.getSystemTicketID(ctx, ticketID)
	if err != nil {
		// Missing system_ticket_id is a data integrity issue; return error to retry.
		return fmt.Errorf("macs dispatcher: get system_ticket_id for %s: %w", ticketID, err)
	}

	// data = the export Ticket shape (one builder, one contract).
	ticket, order, err := QueryAndBuildTicket(ctx, d.pool, ticketID)
	if err != nil {
		return fmt.Errorf("macs dispatcher: build ticket %s: %w", ticketID, err)
	}
	if ticket == nil {
		// Not exportable (order not completed) — nothing MACS could store.
		return nil
	}
	data := macsEventData{Ticket: *ticket, OrderID: order.ID}
	// Whatever the platform's terminal state, at the door it is refunded.
	data.HolderStatus = StatusRefunded
	if reason, ok := ev.Payload["reason"].(string); ok {
		data.Reason = reason
	}

	// Build the MACS envelope. id must be unique per EVENT (MACS exposes
	// /_wh/reprocess/{id}); derive it from the outbox row id, never from
	// the ticket id which repeats across order.paid / ticket.refunded.
	env := macsEnvelope{
		ID:      envelopeID(ev, systemTicketID),
		Created: ev.OccurredAt.UTC().Format(time.RFC3339),
		Type:    macsType,
		Data:    data,
	}

	// Deliver with HMAC signing.
	return d.post(ctx, sub.CallbackURL, sub.SigningSecret, env)
}

// resolveOrgID routes an order-scoped event to its organization: the payload's
// own org_id when present (spec §9.1 always carries it), otherwise the org of
// the event session.
func (d *Dispatcher) resolveOrgID(ctx context.Context, payload map[string]any) (uuid.UUID, error) {
	if s, _ := payload["org_id"].(string); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			return id, nil
		}
	}
	if s, _ := payload["session_id"].(string); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			return d.getOrgIDForSession(ctx, id)
		}
	}
	return uuid.UUID{}, fmt.Errorf("macs dispatcher: payload carries no routable org")
}

// ticketHasOrder reports whether the ticket is linked to an order aggregate
// (tickets.order_id, migration 0092). Complimentary tickets have none.
func (d *Dispatcher) ticketHasOrder(ctx context.Context, ticketID uuid.UUID) (bool, error) {
	const q = `SELECT order_id IS NOT NULL FROM tickets WHERE id = $1`
	var hasOrder bool
	if err := d.pool.QueryRow(ctx, q, ticketID).Scan(&hasOrder); err != nil {
		return false, err
	}
	return hasOrder, nil
}

// getOrgIDForSession resolves the organization UUID from a session UUID
// using the gen querier (GetSessionOrgContext).
func (d *Dispatcher) getOrgIDForSession(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	row, err := gen.New(d.pool).GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("macs dispatcher: get org for session %s: %w", sessionID, err)
	}
	return row.OrgID, nil
}

// getMACSSubscriber looks up the active MACS subscriber for the given org.
type macsSubscriberRow struct {
	CallbackURL   string
	SigningSecret string
}

func (d *Dispatcher) getMACSSubscriber(ctx context.Context, orgID uuid.UUID) (macsSubscriberRow, error) {
	q := gen.New(d.pool)
	row, err := q.GetMACSSubscriberByOrg(ctx, orgID)
	if err != nil {
		return macsSubscriberRow{}, fmt.Errorf("macs dispatcher: get subscriber for org %s: %w", orgID, err)
	}
	return macsSubscriberRow{
		CallbackURL:   row.CallbackURL,
		SigningSecret: row.SigningSecret,
	}, nil
}

// getSystemTicketID fetches the MACS stable integer id for a ticket
// using the gen querier (GetSystemIDForTicket).
func (d *Dispatcher) getSystemTicketID(ctx context.Context, ticketID uuid.UUID) (int64, error) {
	row, err := gen.New(d.pool).GetSystemIDForTicket(ctx, ticketID)
	if err != nil {
		return 0, fmt.Errorf("macs dispatcher: get system_ticket_id for %s: %w", ticketID, err)
	}
	return row.SystemTicketID, nil
}

// post serialises env as JSON, signs it with HMAC-SHA256 using signingSecret,
// and POSTs it to callbackURL. Returns an error on network failure or non-2xx
// response so the outbox retries delivery.
func (d *Dispatcher) post(ctx context.Context, callbackURL string, signingSecret string, env macsEnvelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("macs dispatcher: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("macs dispatcher: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if signingSecret != "" {
		mac := hmac.New(sha256.New, []byte(signingSecret))
		mac.Write(body)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-MACS-Signature", sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("macs dispatcher: http post to %s: %w", callbackURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Contract: success is HTTP 200 (2xx); a redirect is not a delivery.
		return fmt.Errorf("macs dispatcher: server returned %d for %s", resp.StatusCode, callbackURL)
	}
	// Spec §10 M2: 2xx alone is NOT a delivery. MACS answers 200 with a body
	// {"status":"OK"} on success and {"status":"Error"} when it refused the
	// payload (class-lops-macs.php:134-137); the latter must surface as an
	// error so the outbox retries instead of silently dropping the event.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAckBodyBytes))
	if err != nil {
		return fmt.Errorf("macs dispatcher: read ack body from %s: %w", callbackURL, err)
	}
	return checkAck(callbackURL, respBody)
}

// maxAckBodyBytes caps how much of the receiver's answer is read before the
// {"status":"OK"} check — the ack is a few dozen bytes.
const maxAckBodyBytes = 64 << 10

// checkAck enforces the body-level success contract (spec §10 M2). Anything
// that is not a JSON object with status "OK" is a delivery failure.
func checkAck(callbackURL string, body []byte) error {
	var ack struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return fmt.Errorf("macs dispatcher: %s answered 2xx with a non-JSON body %q; want {\"status\":\"OK\"}",
			callbackURL, truncateForError(body))
	}
	if !strings.EqualFold(ack.Status, "OK") {
		return fmt.Errorf("macs dispatcher: %s answered 2xx with body %q; want {\"status\":\"OK\"}",
			callbackURL, truncateForError(body))
	}
	return nil
}

// truncateForError keeps error messages (and therefore outbox last_error
// rows) bounded.
func truncateForError(body []byte) string {
	const limit = 256
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit]) + "…"
}

// resolveSessionID returns payload.session_id when present, else the
// ticket's session from the database.
func (d *Dispatcher) resolveSessionID(ctx context.Context, payload map[string]any, ticketID uuid.UUID) (uuid.UUID, error) {
	if s, _ := payload["session_id"].(string); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			return id, nil
		}
	}
	const q = `SELECT session_id FROM tickets WHERE id = $1`
	var sessionID uuid.UUID
	if err := d.pool.QueryRow(ctx, q, ticketID).Scan(&sessionID); err != nil {
		return uuid.UUID{}, err
	}
	return sessionID, nil
}

// envelopeID derives a per-event integer id from the outbox row id (a
// UUID) — the low 63 bits of its first 8 bytes — so every envelope is
// unique and replayable via /_wh/reprocess/{id}. Falls back to the ticket
// system id when the event carries no id (unit tests).
func envelopeID(ev outbox.Event, fallback int64) int64 {
	if ev.ID == "" {
		return fallback
	}
	if id, err := uuid.Parse(ev.ID); err == nil {
		return eventIntID(id)
	}
	return fallback
}

// Compile-time interface guard.
var _ outbox.Dispatcher = (*Dispatcher)(nil)
