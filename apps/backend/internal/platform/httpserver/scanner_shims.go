// scanner_shims.go bridges the *Server god-object to the hscanner sub-package.
// All scanner-domain handlers and outbox publish helpers live in hscanner/;
// these thin delegating methods preserve the unexported *Server method surface
// so mount_scanning.go, checkout_shims.go, tickets_shims.go, sessions.go, and
// the many structural tests in package httpserver compile unchanged.
package httpserver

import (
	"context"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscanner"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─── handler construction ────────────────────────────────────────────────────

// scannerHandler builds an hscanner.Handler from the current *Server state.
// Constructed fresh per request so tests that mutate *Server fields see the
// new value on the next call.
func (s *Server) scannerHandler() *hscanner.Handler {
	return hscanner.New(
		s.barcodeQueries,
		s.feedTokenQueries,
		s.pool,
		s.outboxWriter,
		s.logger,
	)
}

// ─── event-type constant forwarders ──────────────────────────────────────────
// Package-level tests in scanner_143_test.go and scanner_events_catalog_test.go
// reference these constants unqualified; keep them alive here as thin forwarders.

const (
	ScannerEventTicketIssued   = hscanner.ScannerEventTicketIssued
	ScannerEventTicketRevoked  = hscanner.ScannerEventTicketRevoked
	ScannerEventTicketRefunded = hscanner.ScannerEventTicketRefunded
	TicketRefundedEventType    = hscanner.TicketRefundedEventType
	TicketRevokedEventType     = hscanner.TicketRevokedEventType
	SessionCancelledEventType  = hscanner.SessionCancelledEventType

	TicketAggregateType  = hscanner.TicketAggregateType
	SessionAggregateType = hscanner.SessionAggregateType
	ScannerAggregateType = hscanner.ScannerAggregateType

	TicketScannedEventType = hscanner.TicketScannedEventType
)

// ─── helper + type forwarders (used by 293/278/144 test suites) ─────────────

const maxScannerBatchSize = hscanner.MaxScannerBatchSize

func extractBearerToken(headerValue string) string {
	return hscanner.ExtractBearerToken(headerValue)
}

func credentialPrefixForLog(code string) string {
	return hscanner.CredentialPrefixForLog(code)
}

// ─── payload builder forwarders ──────────────────────────────────────────────
// Tests call these builders directly on package httpserver identifiers.

func buildTicketIssuedPayload(t gen.TicketRow) map[string]any {
	return hscanner.BuildTicketIssuedPayload(t)
}

func buildTicketRevokedPayload(ticketID, checkoutSessionID, reason string) map[string]any {
	return hscanner.BuildTicketRevokedPayload(ticketID, checkoutSessionID, reason)
}

func buildTicketRefundedPayload(checkoutSessionID, refundID, currency string, amount int64) map[string]any {
	return hscanner.BuildTicketRefundedPayload(checkoutSessionID, refundID, currency, amount)
}

func buildTicketRefundedV1Payload(ticketID, checkoutSessionID, refundID, currency string, amount int64) map[string]any {
	return hscanner.BuildTicketRefundedV1Payload(ticketID, checkoutSessionID, refundID, currency, amount)
}

func buildTicketRevokedV1Payload(ticketID, complimentaryIssuanceID, reason string) map[string]any {
	return hscanner.BuildTicketRevokedV1Payload(ticketID, complimentaryIssuanceID, reason)
}

func buildSessionCancelledPayload(sessionID, eventID, previousStatus string) map[string]any {
	return hscanner.BuildSessionCancelledPayload(sessionID, eventID, previousStatus)
}

// ─── publish-method shims (used by checkout_shims / tickets_shims / sessions / tests) ──

// publishScannerEvent forwards to hscanner.PublishScannerEvent. Kept as a
// *Server method because scanner_143_test.go and other in-package callers
// invoke it directly.
func (s *Server) publishScannerEvent(ctx context.Context, event outbox.Event) {
	s.scannerHandler().PublishScannerEvent(ctx, event)
}

// publishTicketIssuedEvents forwards to hscanner.PublishTicketIssuedEvents.
// Kept as a *Server method because tickets_shims.go passes it as a func value
// into htickets.New and scanner_143_test.go invokes it on *Server directly.
func (s *Server) publishTicketIssuedEvents(ctx context.Context, tickets []gen.TicketRow) {
	s.scannerHandler().PublishTicketIssuedEvents(ctx, tickets)
}

// publishTicketRefundedEvents forwards to hscanner.PublishTicketRefundedEvents.
// Kept as a *Server method because checkout_shims.go passes it as a func value
// into hcheckout.New.
func (s *Server) publishTicketRefundedEvents(ctx context.Context, checkoutSessionID, refundID, currency string, amount int64) {
	s.scannerHandler().PublishTicketRefundedEvents(ctx, checkoutSessionID, refundID, currency, amount)
}

// publishTicketRefundedV1Events forwards to hscanner.PublishTicketRefundedV1Events.
// Kept as a *Server method because checkout_shims.go passes it as a func value
// into hcheckout.New and scanner_events_catalog_test.go invokes it directly.
func (s *Server) publishTicketRefundedV1Events(ctx context.Context, ticketIDs []string, checkoutSessionID, refundID, currency string, amount int64) {
	s.scannerHandler().PublishTicketRefundedV1Events(ctx, ticketIDs, checkoutSessionID, refundID, currency, amount)
}

// publishTicketRevokedV1Events forwards to hscanner.PublishTicketRevokedV1Events.
// Kept as a *Server method because tickets_shims.go passes it as a func value
// into htickets.New and scanner_events_catalog_test.go invokes it directly.
func (s *Server) publishTicketRevokedV1Events(ctx context.Context, ticketIDs []string, complimentaryIssuanceID, reason string) {
	s.scannerHandler().PublishTicketRevokedV1Events(ctx, ticketIDs, complimentaryIssuanceID, reason)
}

// publishTicketCancelledEvent forwards to hscanner.PublishTicketCancelledEvent
// (AB-49 operator cancellation). Kept as a *Server method because
// tickets_shims.go passes it as a func value into htickets.New.
func (s *Server) publishTicketCancelledEvent(ctx context.Context, ticket gen.TicketRow, reason, refundMode string) {
	s.scannerHandler().PublishTicketCancelledEvent(ctx, ticket, reason, refundMode)
}

// publishSessionCancelledEvent forwards to hscanner.PublishSessionCancelledEvent.
// Kept as a *Server method because catalog_shims.go passes it as a func value
// into hcatalog.New (the session PATCH handler fires it on cancellation) and
// scanner_events_catalog_test.go invokes it on *Server directly.
func (s *Server) publishSessionCancelledEvent(ctx context.Context, sessionID, eventID, previousStatus string) {
	s.scannerHandler().PublishSessionCancelledEvent(ctx, sessionID, eventID, previousStatus)
}

// publishCatalogEvent forwards to hscanner.PublishCatalogEvent (W1-B7c, #506).
// Same shape and same reason as publishSessionCancelledEvent above: the
// catalog write handlers in hcatalog need it, and it is handed to them as a
// func value so hcatalog never imports hscanner.
func (s *Server) publishCatalogEvent(ctx context.Context, eventType, eventID, orgID string, sessionIDs []string) {
	s.scannerHandler().PublishCatalogEvent(ctx, eventType, eventID, orgID, sessionIDs)
}

// ─── scanner-endpoint handler shims ──────────────────────────────────────────

func (s *Server) handleScannerScanEvents(w http.ResponseWriter, r *http.Request) {
	s.scannerHandler().HandleScannerScanEvents(w, r)
}
