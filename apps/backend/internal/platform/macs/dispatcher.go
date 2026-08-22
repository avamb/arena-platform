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
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// MACS outbox event type constants — must match the values written by the
// scanner/ticket domain when appending events to outbox_events.
const (
	EventTicketIssued    = "v1.scanner.ticket.issued"
	EventTicketRefunded  = "v1.ticket.refunded"
	EventTicketCancelled = "v1.ticket.cancelled"
	EventTicketRevoked   = "v1.ticket.revoked"
)

// macsEventType maps a platform event type to the corresponding MACS webhook
// event type. Returns ("", false) for event types MACS does not handle.
func macsEventType(platformEventType string) (string, bool) {
	switch platformEventType {
	case EventTicketIssued:
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

// macsOrderPaidData is the data object for order.paid events.
type macsOrderPaidData struct {
	TicketID   int64  `json:"ticketId"`
	SessionID  string `json:"sessionId"`
	CheckoutID string `json:"checkoutId"`
}

// macsTicketRefundedData is the data object for ticket.refunded events.
type macsTicketRefundedData struct {
	TicketID int64  `json:"ticketId"`
	Reason   string `json:"reason"`
}

// buildMACSData constructs the typed data object for the MACS envelope.
func buildMACSData(macsType string, systemTicketID int64, payload map[string]any) any {
	switch macsType {
	case "order.paid":
		sessionID, _ := payload["session_id"].(string)
		checkoutID, _ := payload["checkout_session_id"].(string)
		return macsOrderPaidData{
			TicketID:   systemTicketID,
			SessionID:  sessionID,
			CheckoutID: checkoutID,
		}
	case "ticket.refunded":
		return macsTicketRefundedData{
			TicketID: systemTicketID,
			Reason:   "refunded",
		}
	default:
		return map[string]any{"ticketId": systemTicketID}
	}
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
// For non-MACS event types, returns nil immediately (skip without retry).
// For MACS event types:
//  1. Parses ticket_id and session_id from the payload.
//  2. Resolves the org from the session.
//  3. Looks up the active MACS subscriber for the org.
//  4. Looks up the system_ticket_id for the ticket.
//  5. Builds and POSTs the MACS envelope with HMAC signing.
//
// Returns a non-nil error only for delivery failures (so the outbox retries).
// Missing subscribers, missing system IDs, or malformed payloads return nil
// (no subscriber = nothing to deliver; malformed = cannot be retried anyway).
func (d *Dispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	macsType, ok := macsEventType(ev.EventType)
	if !ok {
		// Not a MACS event — let the outbox mark it processed.
		return nil
	}

	ticketIDStr, _ := ev.Payload["ticket_id"].(string)
	sessionIDStr, _ := ev.Payload["session_id"].(string)
	if ticketIDStr == "" || sessionIDStr == "" {
		// Malformed payload — skip; retrying will not help.
		return nil
	}

	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		return nil // malformed UUID; skip
	}
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		return nil // malformed UUID; skip
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

	// Build the MACS envelope.
	env := macsEnvelope{
		ID:      systemTicketID,
		Created: ev.OccurredAt.UTC().Format("2006-01-02T15:04:05"), // allow:timeformat: MACS envelope requires no-TZ suffix
		Type:    macsType,
		Data:    buildMACSData(macsType, systemTicketID, ev.Payload),
	}

	// Deliver with HMAC signing.
	return d.post(ctx, sub.CallbackURL, sub.SigningSecret, env)
}

// getOrgIDForSession resolves the organization UUID from a session UUID by
// joining sessions → events → org_id.
func (d *Dispatcher) getOrgIDForSession(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	const q = `SELECT e.org_id FROM sessions s JOIN events e ON e.id = s.event_id WHERE s.id = $1`
	var orgID uuid.UUID
	err := d.pool.QueryRow(ctx, q, sessionID).Scan(&orgID)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("macs dispatcher: get org for session %s: %w", sessionID, err)
	}
	return orgID, nil
}

// getMACSSubscriber looks up the active MACS subscriber for the given org.
type macsSubscriberRow struct {
	CallbackURL   string
	SigningSecret string
}

func (d *Dispatcher) getMACSSubscriber(ctx context.Context, orgID uuid.UUID) (macsSubscriberRow, error) {
	const q = `
		SELECT callback_url, signing_secret
		FROM   webhook_subscribers
		WHERE  org_id = $1
		  AND  kind   = 'macs'
		  AND  active = TRUE`
	var sub macsSubscriberRow
	err := d.pool.QueryRow(ctx, q, orgID).Scan(&sub.CallbackURL, &sub.SigningSecret)
	if err != nil {
		return macsSubscriberRow{}, fmt.Errorf("macs dispatcher: get subscriber for org %s: %w", orgID, err)
	}
	return sub, nil
}

// getSystemTicketID fetches the MACS stable integer id for a ticket.
func (d *Dispatcher) getSystemTicketID(ctx context.Context, ticketID uuid.UUID) (int64, error) {
	const q = `SELECT system_ticket_id FROM tickets WHERE id = $1`
	var id int64
	err := d.pool.QueryRow(ctx, q, ticketID).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, fmt.Errorf("ticket %s not found", ticketID)
		}
		return 0, err
	}
	return id, nil
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

	if resp.StatusCode >= 400 {
		return fmt.Errorf("macs dispatcher: server returned %d for %s", resp.StatusCode, callbackURL)
	}
	return nil
}

// Compile-time interface guard.
var _ outbox.Dispatcher = (*Dispatcher)(nil)
