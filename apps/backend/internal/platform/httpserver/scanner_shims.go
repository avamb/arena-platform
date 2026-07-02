// scanner_shims.go bridges the *Server god-object to the hscanner sub-package.
// All scanner-domain handlers and outbox publish helpers live in hscanner/;
// these thin delegating methods preserve the unexported *Server method surface
// so mount_scanning.go, checkout_shims.go, tickets_shims.go, sessions.go, and
// the many structural tests in package httpserver compile unchanged.
//
// The scanner rate limiter (scannerRateLimiter / newScannerRateLimiter /
// serverScannerRL) is kept live in this file — the tests directly mutate the
// package-level var to inject deterministic limits, and inspect the ipLimit /
// sessionLimit fields via source-grep. The scannerRateLimiter type satisfies
// the narrower hscanner.RateLimiter interface via the CheckIP / CheckSession
// wrapper methods below.
package httpserver

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscanner"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─── scanner rate limiter (kept in package httpserver for tests) ──────────────
// The tests in scanner_snapshot_144_test.go swap the package-level serverScannerRL
// var and grep the source for the "scannerRateLimiter" / "ipLimit" / "sessionLimit"
// identifiers, so we keep both the type and the var live here.

// scannerRateLimiter is a simple in-memory rate limiter for scanner endpoints.
// It tracks per-IP and per-session request counts with 1-minute windows.
// The limiter is safe for concurrent use.
type scannerRateLimiter struct {
	mu           sync.Mutex
	ipLimit      int
	sessionLimit int
	ips          map[string]*rateLimiterWindow
	sessions     map[string]*rateLimiterWindow
}

// newScannerRateLimiter creates a rate limiter for scanner endpoints.
// ipLimit is the max requests per IP per minute; sessionLimit is the max per session.
func newScannerRateLimiter(ipLimit, sessionLimit int) *scannerRateLimiter {
	return &scannerRateLimiter{
		ipLimit:      ipLimit,
		sessionLimit: sessionLimit,
		ips:          make(map[string]*rateLimiterWindow),
		sessions:     make(map[string]*rateLimiterWindow),
	}
}

// check is a generic rate-limit check against a window map.
func (rl *scannerRateLimiter) check(m map[string]*rateLimiterWindow, key string, limit int) bool {
	now := time.Now()
	w, ok := m[key]
	if !ok || now.After(w.resetAt) {
		m[key] = &rateLimiterWindow{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	w.count++
	return w.count <= limit
}

// checkIP returns true when the IP is within the per-IP rate limit.
func (rl *scannerRateLimiter) checkIP(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.ips, ip, rl.ipLimit)
}

// checkSession returns true when the session is within the per-session rate limit.
func (rl *scannerRateLimiter) checkSession(sessionID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.sessions, sessionID, rl.sessionLimit)
}

// CheckIP / CheckSession are exported adapter methods so *scannerRateLimiter
// satisfies the hscanner.RateLimiter interface. They forward to the private
// implementations above unchanged.
func (rl *scannerRateLimiter) CheckIP(ip string) bool { return rl.checkIP(ip) }

// CheckSession forwards to the unexported checkSession helper.
func (rl *scannerRateLimiter) CheckSession(sessionID string) bool { return rl.checkSession(sessionID) }

// serverScannerRL is the package-level rate limiter shared across all scanner
// snapshot/validate requests. Tests may swap this out temporarily to inject
// deterministic limits.
var serverScannerRL = newScannerRateLimiter(600, 300)

// ─── handler construction ────────────────────────────────────────────────────

// scannerHandler builds an hscanner.Handler from the current *Server state and
// the (possibly test-swapped) package-level rate limiter. Constructed fresh
// per request so any concurrent test that mutates serverScannerRL sees the new
// value on the next call.
func (s *Server) scannerHandler() *hscanner.Handler {
	return hscanner.New(
		s.barcodeQueries,
		s.feedTokenQueries,
		s.pool,
		s.outboxWriter,
		serverScannerRL,
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

type snapshotBarcodeResponse = hscanner.SnapshotBarcodeResponse
type validateBarcodeResponse = hscanner.ValidateBarcodeResponse

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

// publishSessionCancelledEvent forwards to hscanner.PublishSessionCancelledEvent.
// Kept as a *Server method because catalog_shims.go passes it as a func value
// into hcatalog.New (the session PATCH handler fires it on cancellation) and
// scanner_events_catalog_test.go invokes it on *Server directly.
func (s *Server) publishSessionCancelledEvent(ctx context.Context, sessionID, eventID, previousStatus string) {
	s.scannerHandler().PublishSessionCancelledEvent(ctx, sessionID, eventID, previousStatus)
}

// ─── scanner-endpoint handler shims ──────────────────────────────────────────

func (s *Server) handleScannerSnapshot(w http.ResponseWriter, r *http.Request) {
	s.scannerHandler().HandleScannerSnapshot(w, r)
}

func (s *Server) handleScannerValidate(w http.ResponseWriter, r *http.Request) {
	s.scannerHandler().HandleScannerValidate(w, r)
}

func (s *Server) handleScannerScanEvents(w http.ResponseWriter, r *http.Request) {
	s.scannerHandler().HandleScannerScanEvents(w, r)
}
