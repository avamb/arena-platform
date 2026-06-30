// scanner_events.go — Bil24-compatible scanner event publishing (feature #143).
//
// Ticket lifecycle events are published to the outbox table whenever a ticket
// changes state. The payload schema follows the Bil24 order/ticket domain model
// so that Bil24-compatible scanner services can consume the events directly.
//
// Events published:
//
//	v1.scanner.ticket.issued   — ticket created after successful checkout
//	v1.scanner.ticket.revoked  — ticket cancelled (e.g. admin action)
//	v1.scanner.ticket.refunded — ticket cancelled as part of a provider refund
//
// In addition to the Bil24-compatible scanner.* events above, the generic
// scanner-relevant outbox events for the webhook event catalog (feature S-1)
// are also emitted from this file:
//
//	v1.ticket.refunded   — per-ticket cancellation on a provider refund
//	v1.ticket.revoked    — per-ticket cancellation on complimentary revocation
//	v1.session.cancelled — session status transition to "cancelled"
//
// Publishing is best-effort: errors are logged but never propagate to the HTTP
// caller. This keeps the ticket issuance and refund webhook paths clean even
// when the outbox writer or database is temporarily unavailable.
//
// Bil24 compatibility note: Bil24 uses two primary order statuses —
//   - "PAID"      → ticket is valid and scannable
//   - "CANCELLED" → ticket is no longer valid; scanners must reject it
//
// These string values are embedded in the bil24_order_status field of every
// scanner event payload so that legacy Bil24 scanner software can consume
// our events without modification.
package httpserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// Event type constants (Bil24-compatible)
// ─────────────────────────────────────────────────────────────────────────────

// Scanner event type identifiers. These string constants are written to the
// outbox event_type column and form the stable webhook contract that scanner
// subscribers filter on.
//
// Naming convention: v1.<domain>.<entity>.<action>
const (
	// ScannerEventTicketIssued is published when a new ticket is created after
	// a successful checkout. Corresponds to Bil24 order status "PAID".
	ScannerEventTicketIssued = "v1.scanner.ticket.issued"

	// ScannerEventTicketRevoked is published when a ticket is cancelled for a
	// reason other than a provider refund (e.g. admin action, expired hold).
	// Corresponds to Bil24 order status "CANCELLED".
	ScannerEventTicketRevoked = "v1.scanner.ticket.revoked"

	// ScannerEventTicketRefunded is published when a payment refund succeeds
	// and the linked tickets are cancelled as a result. Carries refund metadata
	// (amount, currency, refund_id) alongside the Bil24 cancellation signal.
	ScannerEventTicketRefunded = "v1.scanner.ticket.refunded"

	// TicketRefundedEventType is the generic per-ticket cancellation event
	// emitted whenever a provider refund finalizes successfully and the linked
	// tickets are cancelled.  Distinct from ScannerEventTicketRefunded
	// (single-event-per-refund, Bil24-shaped payload): this event is emitted
	// once per cancelled ticket and uses the ticket aggregate.
	TicketRefundedEventType = "v1.ticket.refunded"

	// TicketRevokedEventType is the per-ticket revocation event emitted when
	// a complimentary issuance is revoked.  Differs from the refunded variant
	// because no payment refund is involved.
	TicketRevokedEventType = "v1.ticket.revoked"

	// SessionCancelledEventType is emitted once when a session transitions
	// to the "cancelled" status (regardless of how many tickets are attached).
	SessionCancelledEventType = "v1.session.cancelled"
)

// Generic aggregate-type constants for the webhook event catalog.
const (
	// TicketAggregateType is the aggregate_type column value for ticket-scoped
	// outbox events (v1.ticket.*).  Distinct from ScannerAggregateType, which
	// is reserved for Bil24-shaped scanner.* events.
	TicketAggregateType = "ticket"

	// SessionAggregateType is the aggregate_type column value for
	// session-scoped outbox events (v1.session.*).
	SessionAggregateType = "session"
)

// ScannerAggregateType is the aggregate_type value written to the outbox for
// all scanner-domain events. Outbox dispatchers route events to scanner
// webhook subscribers by matching this aggregate type.
const ScannerAggregateType = "scanner.ticket"

// ─────────────────────────────────────────────────────────────────────────────
// Payload builders
// ─────────────────────────────────────────────────────────────────────────────

// buildTicketIssuedPayload constructs the Bil24-compatible JSON payload for a
// ticket issuance event. The payload includes the full ticket identity, the
// Bil24 order status ("PAID"), and the issuance timestamp in RFC3339 format.
//
// Optional fields (tier_id, holder_email) are omitted when nil so that the
// JSON payload stays minimal for external-platform and guest-list barcodes.
func buildTicketIssuedPayload(t gen.TicketRow) map[string]any {
	payload := map[string]any{
		"ticket_id":           t.ID.String(),
		"checkout_session_id": t.CheckoutSessionID.String(),
		"session_id":          t.SessionID.String(),
		"status":              t.Status, // platform status: "active"
		"bil24_order_status":  "PAID",   // Bil24-compatible status for issued tickets
		"issued_at":           t.IssuedAt.UTC().Format(time.RFC3339),
	}
	if t.TierID != nil {
		payload["tier_id"] = t.TierID.String()
	}
	if t.HolderEmail != nil {
		payload["holder_email"] = *t.HolderEmail
	}
	return payload
}

// buildTicketRevokedPayload constructs the Bil24-compatible payload for a
// generic ticket revocation event (non-refund cancellations).
// reason is a short lower-snake-case string, e.g. "admin_cancel".
func buildTicketRevokedPayload(ticketID, checkoutSessionID, reason string) map[string]any {
	return map[string]any{
		"ticket_id":           ticketID,
		"checkout_session_id": checkoutSessionID,
		"reason":              reason,
		"bil24_order_status":  "CANCELLED",
		"revoked_at":          time.Now().UTC().Format(time.RFC3339),
	}
}

// buildTicketRefundedPayload constructs the Bil24-compatible payload for a
// refund-driven ticket cancellation. It extends the cancellation signal with
// financial context — refund_id, amount in minor units, and currency code —
// so that scanner services can reconcile refund events against order records.
func buildTicketRefundedPayload(checkoutSessionID, refundID, currency string, amount int64) map[string]any {
	return map[string]any{
		"checkout_session_id": checkoutSessionID,
		"refund_id":           refundID,
		"amount":              amount,
		"currency":            currency,
		"bil24_order_status":  "CANCELLED",
		"refunded_at":         time.Now().UTC().Format(time.RFC3339),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Publishing helpers
// ─────────────────────────────────────────────────────────────────────────────

// publishScannerEvent appends a single scanner event to the outbox table using
// a short-lived transaction on the server pool. The call is best-effort: any
// error is logged and the method returns without surfacing the failure to the
// HTTP caller.
//
// Silently no-ops when s.pool or s.outboxWriter is nil (e.g. in tests where
// the outbox pipeline is not wired up, or in environments where the scanner
// integration is disabled).
func (s *Server) publishScannerEvent(ctx context.Context, event outbox.Event) {
	if s.pool == nil || s.outboxWriter == nil {
		return
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		s.logger.Warn("scanner: begin tx for outbox event",
			slog.String("event_type", event.EventType),
			slog.String("error", err.Error()),
		)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.outboxWriter.Append(ctx, tx, event); err != nil {
		s.logger.Warn("scanner: append outbox event",
			slog.String("event_type", event.EventType),
			slog.String("error", err.Error()),
		)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		s.logger.Warn("scanner: commit outbox event",
			slog.String("event_type", event.EventType),
			slog.String("error", err.Error()),
		)
	}
}

// publishTicketIssuedEvents publishes ScannerEventTicketIssued to the outbox
// for each ticket in the slice. Called by issueTicketsForCheckout immediately
// after new tickets are inserted (not during idempotent replay — existing
// tickets have already generated their issued events on first insertion).
func (s *Server) publishTicketIssuedEvents(ctx context.Context, tickets []gen.TicketRow) {
	for _, t := range tickets {
		s.publishScannerEvent(ctx, outbox.Event{
			AggregateType: ScannerAggregateType,
			AggregateID:   t.ID.String(),
			EventType:     ScannerEventTicketIssued,
			Payload:       buildTicketIssuedPayload(t),
		})
	}
}

// publishTicketRefundedEvents publishes ScannerEventTicketRefunded to the
// outbox when a refund webhook confirms that a payment was refunded and the
// linked tickets have been cancelled. Called by handleRefundWebhook on the
// "succeeded" transition after CancelTicketsByCheckoutSession completes.
func (s *Server) publishTicketRefundedEvents(ctx context.Context, checkoutSessionID, refundID, currency string, amount int64) {
	s.publishScannerEvent(ctx, outbox.Event{
		AggregateType: ScannerAggregateType,
		AggregateID:   refundID,
		EventType:     ScannerEventTicketRefunded,
		Payload:       buildTicketRefundedPayload(checkoutSessionID, refundID, currency, amount),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Generic webhook catalog events (feature S-1)
// ─────────────────────────────────────────────────────────────────────────────

// buildTicketRefundedV1Payload constructs the per-ticket payload for the
// generic v1.ticket.refunded webhook event.
func buildTicketRefundedV1Payload(ticketID, checkoutSessionID, refundID, currency string, amount int64) map[string]any {
	return map[string]any{
		"ticket_id":           ticketID,
		"checkout_session_id": checkoutSessionID,
		"refund_id":           refundID,
		"amount":              amount,
		"currency":            currency,
		"refunded_at":         time.Now().UTC().Format(time.RFC3339),
	}
}

// buildTicketRevokedV1Payload constructs the per-ticket payload for the
// generic v1.ticket.revoked webhook event (complimentary revocation path).
func buildTicketRevokedV1Payload(ticketID, complimentaryIssuanceID, reason string) map[string]any {
	payload := map[string]any{
		"ticket_id":  ticketID,
		"reason":     reason,
		"revoked_at": time.Now().UTC().Format(time.RFC3339),
	}
	if complimentaryIssuanceID != "" {
		payload["complimentary_issuance_id"] = complimentaryIssuanceID
	}
	return payload
}

// buildSessionCancelledPayload constructs the payload for v1.session.cancelled.
func buildSessionCancelledPayload(sessionID, eventID, previousStatus string) map[string]any {
	payload := map[string]any{
		"session_id":   sessionID,
		"status":       "cancelled",
		"cancelled_at": time.Now().UTC().Format(time.RFC3339),
	}
	if eventID != "" {
		payload["event_id"] = eventID
	}
	if previousStatus != "" {
		payload["previous_status"] = previousStatus
	}
	return payload
}

// publishTicketRefundedV1Events emits one v1.ticket.refunded outbox event per
// cancelled ticket after a refund finalization succeeds.  Aggregate is "ticket"
// and the aggregate_id is the ticket UUID, so webhook subscribers can fan-out
// by ticket aggregate.  Called by handleRefundWebhook on the "succeeded"
// transition once the linked tickets have been listed.
func (s *Server) publishTicketRefundedV1Events(ctx context.Context, ticketIDs []string, checkoutSessionID, refundID, currency string, amount int64) {
	for _, tid := range ticketIDs {
		s.publishScannerEvent(ctx, outbox.Event{
			AggregateType: TicketAggregateType,
			AggregateID:   tid,
			EventType:     TicketRefundedEventType,
			Payload:       buildTicketRefundedV1Payload(tid, checkoutSessionID, refundID, currency, amount),
		})
	}
}

// publishTicketRevokedV1Events emits one v1.ticket.revoked outbox event per
// ticket revoked as part of a complimentary issuance revocation.  Called from
// handleRevokeComplimentaryIssuance after the revocation transaction commits.
func (s *Server) publishTicketRevokedV1Events(ctx context.Context, ticketIDs []string, complimentaryIssuanceID, reason string) {
	for _, tid := range ticketIDs {
		s.publishScannerEvent(ctx, outbox.Event{
			AggregateType: TicketAggregateType,
			AggregateID:   tid,
			EventType:     TicketRevokedEventType,
			Payload:       buildTicketRevokedV1Payload(tid, complimentaryIssuanceID, reason),
		})
	}
}

// publishSessionCancelledEvent emits a single v1.session.cancelled outbox event
// when a session status transitions to "cancelled".  Called from
// handleUpdateSession after the UPDATE succeeds, but only when the status
// actually changed from a non-cancelled state.
func (s *Server) publishSessionCancelledEvent(ctx context.Context, sessionID, eventID, previousStatus string) {
	s.publishScannerEvent(ctx, outbox.Event{
		AggregateType: SessionAggregateType,
		AggregateID:   sessionID,
		EventType:     SessionCancelledEventType,
		Payload:       buildSessionCancelledPayload(sessionID, eventID, previousStatus),
	})
}
