// scanner_143_test.go — unit tests for Bil24-compatible scanner webhook events
// (feature #143).
//
// Tests cover:
//
//	Step 1:  Event type constants have correct Bil24-compatible string values
//	Step 2:  buildTicketIssuedPayload — required fields and Bil24 status
//	Step 3:  buildTicketIssuedPayload — optional field handling (tier_id, holder_email)
//	Step 4:  buildTicketRefundedPayload — financial context (amount, currency)
//	Step 5:  buildTicketRevokedPayload — required fields
//	Step 6:  publishScannerEvent no-ops gracefully when pool or outboxWriter is nil
//	Step 7:  Server has outboxWriter field (compile-time wiring check)
//	Step 8:  Source-code wiring — tickets.go calls publishTicketIssuedEvents
//	Step 9:  Source-code wiring — refunds.go calls publishTicketRefundedEvents
//	Step 10: Bil24 order status vocabulary (PAID|CANCELLED only)
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Event type constants
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_EventTypeIssuedCorrect(t *testing.T) {
	if ScannerEventTicketIssued != "v1.scanner.ticket.issued" {
		t.Errorf("ScannerEventTicketIssued = %q; want %q",
			ScannerEventTicketIssued, "v1.scanner.ticket.issued")
	}
}

func TestScanner143_EventTypeRevokedCorrect(t *testing.T) {
	if ScannerEventTicketRevoked != "v1.scanner.ticket.revoked" {
		t.Errorf("ScannerEventTicketRevoked = %q; want %q",
			ScannerEventTicketRevoked, "v1.scanner.ticket.revoked")
	}
}

func TestScanner143_EventTypeRefundedCorrect(t *testing.T) {
	if ScannerEventTicketRefunded != "v1.scanner.ticket.refunded" {
		t.Errorf("ScannerEventTicketRefunded = %q; want %q",
			ScannerEventTicketRefunded, "v1.scanner.ticket.refunded")
	}
}

func TestScanner143_AggregateTypeCorrect(t *testing.T) {
	if ScannerAggregateType != "scanner.ticket" {
		t.Errorf("ScannerAggregateType = %q; want %q",
			ScannerAggregateType, "scanner.ticket")
	}
}

func TestScanner143_EventTypesHaveVersionPrefix(t *testing.T) {
	for _, et := range []string{
		ScannerEventTicketIssued,
		ScannerEventTicketRevoked,
		ScannerEventTicketRefunded,
	} {
		if !strings.HasPrefix(et, "v1.") {
			t.Errorf("event type %q must start with v1. (Bil24-compat versioning)", et)
		}
	}
}

func TestScanner143_EventTypesAreDotSeparated(t *testing.T) {
	for _, et := range []string{
		ScannerEventTicketIssued,
		ScannerEventTicketRevoked,
		ScannerEventTicketRefunded,
	} {
		if strings.Count(et, ".") < 2 {
			t.Errorf("event type %q must have at least 3 dot-separated segments", et)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: buildTicketIssuedPayload — required fields
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_TicketIssuedPayloadRequiredFields(t *testing.T) {
	now := time.Now().UTC()
	row := gen.TicketRow{
		ID:                uuid.New(),
		CheckoutSessionID: uuid.New(),
		SessionID:         uuid.New(),
		Status:            "active",
		IssuedAt:          now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	payload := buildTicketIssuedPayload(row)
	for _, field := range []string{
		"ticket_id", "checkout_session_id", "session_id",
		"status", "bil24_order_status", "issued_at",
	} {
		if _, ok := payload[field]; !ok {
			t.Errorf("buildTicketIssuedPayload: missing required field %q", field)
		}
	}
}

func TestScanner143_TicketIssuedPayloadBil24StatusIsPAID(t *testing.T) {
	now := time.Now().UTC()
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	if payload["bil24_order_status"] != "PAID" {
		t.Errorf("bil24_order_status = %v; want PAID (Bil24 status for issued/active tickets)",
			payload["bil24_order_status"])
	}
}

func TestScanner143_TicketIssuedPayloadStatusIsActive(t *testing.T) {
	now := time.Now().UTC()
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	if payload["status"] != "active" {
		t.Errorf("status = %v; want active", payload["status"])
	}
}

func TestScanner143_TicketIssuedPayloadIDsAreStrings(t *testing.T) {
	now := time.Now().UTC()
	id, csID, sID := uuid.New(), uuid.New(), uuid.New()
	row := gen.TicketRow{
		ID: id, CheckoutSessionID: csID, SessionID: sID,
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	if payload["ticket_id"] != id.String() {
		t.Errorf("ticket_id = %v; want %s", payload["ticket_id"], id.String())
	}
	if payload["checkout_session_id"] != csID.String() {
		t.Errorf("checkout_session_id = %v; want %s", payload["checkout_session_id"], csID.String())
	}
	if payload["session_id"] != sID.String() {
		t.Errorf("session_id = %v; want %s", payload["session_id"], sID.String())
	}
}

func TestScanner143_TicketIssuedPayloadIssuedAtIsRFC3339(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	s, ok := payload["issued_at"].(string)
	if !ok {
		t.Fatalf("issued_at is not a string: %T", payload["issued_at"])
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("issued_at %q is not valid RFC3339: %v", s, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: buildTicketIssuedPayload — optional fields
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_TicketIssuedPayloadOptionalFieldsPresent(t *testing.T) {
	now := time.Now().UTC()
	tierID := uuid.New()
	email := "buyer@example.com"
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		TierID: &tierID, HolderEmail: &email,
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	if payload["tier_id"] != tierID.String() {
		t.Errorf("tier_id = %v; want %s", payload["tier_id"], tierID.String())
	}
	if payload["holder_email"] != email {
		t.Errorf("holder_email = %v; want %s", payload["holder_email"], email)
	}
}

func TestScanner143_TicketIssuedPayloadOptionalFieldsAbsentWhenNil(t *testing.T) {
	now := time.Now().UTC()
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		TierID: nil, HolderEmail: nil,
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payload := buildTicketIssuedPayload(row)
	if _, ok := payload["tier_id"]; ok {
		t.Error("tier_id must be absent when nil")
	}
	if _, ok := payload["holder_email"]; ok {
		t.Error("holder_email must be absent when nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: buildTicketRefundedPayload — financial context
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_TicketRefundedPayloadRequiredFields(t *testing.T) {
	payload := buildTicketRefundedPayload(uuid.New().String(), uuid.New().String(), "USD", 5000)
	for _, field := range []string{
		"checkout_session_id", "refund_id", "amount", "currency",
		"bil24_order_status", "refunded_at",
	} {
		if _, ok := payload[field]; !ok {
			t.Errorf("buildTicketRefundedPayload: missing field %q", field)
		}
	}
}

func TestScanner143_TicketRefundedPayloadFinancialContext(t *testing.T) {
	csID := uuid.New().String()
	refundID := uuid.New().String()
	payload := buildTicketRefundedPayload(csID, refundID, "EUR", 12050)
	if payload["amount"] != int64(12050) {
		t.Errorf("amount = %v; want 12050", payload["amount"])
	}
	if payload["currency"] != "EUR" {
		t.Errorf("currency = %v; want EUR", payload["currency"])
	}
	if payload["refund_id"] != refundID {
		t.Errorf("refund_id = %v; want %s", payload["refund_id"], refundID)
	}
	if payload["checkout_session_id"] != csID {
		t.Errorf("checkout_session_id = %v; want %s", payload["checkout_session_id"], csID)
	}
}

func TestScanner143_TicketRefundedPayloadBil24StatusIsCANCELLED(t *testing.T) {
	payload := buildTicketRefundedPayload(uuid.New().String(), uuid.New().String(), "RUB", 100000)
	if payload["bil24_order_status"] != "CANCELLED" {
		t.Errorf("bil24_order_status = %v; want CANCELLED", payload["bil24_order_status"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: buildTicketRevokedPayload — required fields
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_TicketRevokedPayloadRequiredFields(t *testing.T) {
	payload := buildTicketRevokedPayload(uuid.New().String(), uuid.New().String(), "admin_cancel")
	for _, field := range []string{
		"ticket_id", "checkout_session_id", "reason",
		"bil24_order_status", "revoked_at",
	} {
		if _, ok := payload[field]; !ok {
			t.Errorf("buildTicketRevokedPayload: missing field %q", field)
		}
	}
}

func TestScanner143_TicketRevokedPayloadBil24StatusIsCANCELLED(t *testing.T) {
	payload := buildTicketRevokedPayload(uuid.New().String(), uuid.New().String(), "admin_cancel")
	if payload["bil24_order_status"] != "CANCELLED" {
		t.Errorf("bil24_order_status = %v; want CANCELLED", payload["bil24_order_status"])
	}
}

func TestScanner143_TicketRevokedPayloadReasonPropagated(t *testing.T) {
	payload := buildTicketRevokedPayload(uuid.New().String(), uuid.New().String(), "expired_hold")
	if payload["reason"] != "expired_hold" {
		t.Errorf("reason = %v; want expired_hold", payload["reason"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: publishScannerEvent graceful degradation
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_PublishNoOpWhenPoolNil(t *testing.T) {
	co := &captureOutboxWriter143{}
	s := &Server{
		pool:         nil,
		outboxWriter: co,
		logger:       slog.Default(),
	}
	s.publishScannerEvent(context.Background(), outbox.Event{
		AggregateType: ScannerAggregateType,
		AggregateID:   uuid.New().String(),
		EventType:     ScannerEventTicketIssued,
		Payload:       map[string]any{"test": true},
	})
	if len(co.events) != 0 {
		t.Errorf("outbox must not be called when pool is nil; got %d call(s)", len(co.events))
	}
}

func TestScanner143_PublishNoOpWhenOutboxNil(_ *testing.T) {
	// Should not panic; dbDownPool.BeginTx returns error → early return.
	s := &Server{
		pool:         &dbDownPool{},
		outboxWriter: nil,
		logger:       slog.Default(),
	}
	s.publishScannerEvent(context.Background(), outbox.Event{
		AggregateType: ScannerAggregateType,
		AggregateID:   uuid.New().String(),
		EventType:     ScannerEventTicketIssued,
		Payload:       map[string]any{"test": true},
	})
}

func TestScanner143_PublishNoOpWhenBothNil(_ *testing.T) {
	s := &Server{
		pool:         nil,
		outboxWriter: nil,
		logger:       slog.Default(),
	}
	s.publishScannerEvent(context.Background(), outbox.Event{
		AggregateType: ScannerAggregateType,
		AggregateID:   uuid.New().String(),
		EventType:     ScannerEventTicketIssued,
		Payload:       map[string]any{"test": true},
	})
}

func TestScanner143_PublishTicketIssuedNoOpWithDownPool(_ *testing.T) {
	// publishTicketIssuedEvents silently skips all events when BeginTx fails.
	now := time.Now().UTC()
	s := &Server{
		pool:         &dbDownPool{},
		outboxWriter: &captureOutboxWriter143{},
		logger:       slog.Default(),
	}
	tickets := []gen.TicketRow{
		{
			ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
			Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}
	// Must not panic even when BeginTx returns error.
	s.publishTicketIssuedEvents(context.Background(), tickets)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Server wiring (compile-time check)
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_ServerHasOutboxWriterField(_ *testing.T) {
	// Compile-time guard: if outboxWriter is removed from Server, this won't compile.
	s := &Server{}
	var w = s.outboxWriter // nil satisfies the interface
	_ = w
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8-9: Source-code wiring checks
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_TicketsGoCallsPublishTicketIssuedEvents(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "publishTicketIssuedEvents") {
		t.Error("tickets.go must call publishTicketIssuedEvents after ticket insertion (feature #143)")
	}
}

func TestScanner143_RefundsGoCallsPublishTicketRefundedEvents(t *testing.T) {
	content := findFileByName(t, "refunds.go")
	if !strings.Contains(content, "publishTicketRefundedEvents") {
		t.Error("refunds.go must call publishTicketRefundedEvents when refund webhook succeeds (feature #143)")
	}
}

func TestScanner143_ScannerEventsGoExists(t *testing.T) {
	content := findFileByName(t, "scanner_events.go")
	if content == "" {
		t.Fatal("scanner_events.go not found or empty")
	}
}

func TestScanner143_ScannerEventsGoDefinesAllPayloadBuilders(t *testing.T) {
	content := findFileByName(t, "scanner_events.go")
	for _, fn := range []string{
		"buildTicketIssuedPayload",
		"buildTicketRevokedPayload",
		"buildTicketRefundedPayload",
	} {
		if !strings.Contains(content, fn) {
			t.Errorf("scanner_events.go must define %s", fn)
		}
	}
}

func TestScanner143_ScannerEventsGoIteratesAllTickets(t *testing.T) {
	content := findFileByName(t, "scanner_events.go")
	if !strings.Contains(content, "for _, t := range tickets") {
		t.Error("publishTicketIssuedEvents must iterate over all tickets with 'for _, t := range tickets'")
	}
}

func TestScanner143_ScannerEventsGoPublishHooksUsePool(t *testing.T) {
	content := findFileByName(t, "scanner_events.go")
	if !strings.Contains(content, "BeginTx") {
		t.Error("publishScannerEvent must use pool.BeginTx for transactional outbox writes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Bil24 order status vocabulary
// ─────────────────────────────────────────────────────────────────────────────

func TestScanner143_Bil24OrderStatusVocabulary(t *testing.T) {
	// Bil24 uses exactly two primary order statuses: PAID and CANCELLED.
	// Every scanner event payload must use one of these two values — no others.
	validStatuses := map[string]bool{"PAID": true, "CANCELLED": true}
	now := time.Now().UTC()
	row := gen.TicketRow{
		ID: uuid.New(), CheckoutSessionID: uuid.New(), SessionID: uuid.New(),
		Status: "active", IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	payloads := map[string]map[string]any{
		"issued":   buildTicketIssuedPayload(row),
		"revoked":  buildTicketRevokedPayload(uuid.New().String(), uuid.New().String(), "refund"),
		"refunded": buildTicketRefundedPayload(uuid.New().String(), uuid.New().String(), "RUB", 100000),
	}
	for name, payload := range payloads {
		status, ok := payload["bil24_order_status"].(string)
		if !ok {
			t.Errorf("%s payload: bil24_order_status is not a string (got %T)", name, payload["bil24_order_status"])
			continue
		}
		if !validStatuses[status] {
			t.Errorf("%s payload: bil24_order_status = %q; must be PAID or CANCELLED (Bil24 vocabulary)", name, status)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers local to scanner tests
// ─────────────────────────────────────────────────────────────────────────────

// captureOutboxWriter143 records outbox.Append calls without requiring a real DB.
// Implements outbox.Writer for use in unit tests.
type captureOutboxWriter143 struct {
	events []outbox.Event
}

func (c *captureOutboxWriter143) Append(_ context.Context, _ pgx.Tx, event outbox.Event) error {
	c.events = append(c.events, event)
	return nil
}
