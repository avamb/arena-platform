// scanner_events_catalog_test.go — feature S-1 webhook event catalog payload
// builder tests.  Covers v1.ticket.refunded, v1.ticket.revoked, and
// v1.session.cancelled payloads; the per-event-type event_type constants; and
// the publish helpers' no-op behaviour when the outbox writer is nil.
package httpserver

import (
	"context"
	"testing"
	"time"
)

func TestCatalog_EventTypeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{TicketRefundedEventType, "v1.ticket.refunded"},
		{TicketRevokedEventType, "v1.ticket.revoked"},
		{SessionCancelledEventType, "v1.session.cancelled"},
		{TicketAggregateType, "ticket"},
		{SessionAggregateType, "session"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("constant = %q, want %q", c.got, c.want)
		}
	}
}

func TestBuildTicketRefundedV1Payload_AllFieldsPresent(t *testing.T) {
	t.Parallel()
	p := buildTicketRefundedV1Payload(
		"tic-1", "cs-1", "rf-1", "EUR", 12345,
	)
	for _, k := range []string{"ticket_id", "checkout_session_id", "refund_id",
		"amount", "currency", "refunded_at"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload missing key %q", k)
		}
	}
	if p["ticket_id"] != "tic-1" {
		t.Errorf("ticket_id = %v, want tic-1", p["ticket_id"])
	}
	if p["amount"].(int64) != 12345 {
		t.Errorf("amount = %v, want 12345", p["amount"])
	}
	if p["currency"] != "EUR" {
		t.Errorf("currency = %v, want EUR", p["currency"])
	}
	// refunded_at must be RFC3339 in UTC ("Z" suffix).
	ts, _ := p["refunded_at"].(string)
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("refunded_at not RFC3339: %v", err)
	}
}

func TestBuildTicketRevokedV1Payload_IncludesIssuanceIDWhenSet(t *testing.T) {
	t.Parallel()
	p := buildTicketRevokedV1Payload("tic-1", "iss-1", "complimentary_revocation")
	if p["complimentary_issuance_id"] != "iss-1" {
		t.Errorf("complimentary_issuance_id = %v, want iss-1", p["complimentary_issuance_id"])
	}
	if p["reason"] != "complimentary_revocation" {
		t.Errorf("reason = %v, want complimentary_revocation", p["reason"])
	}
	if p["ticket_id"] != "tic-1" {
		t.Errorf("ticket_id = %v, want tic-1", p["ticket_id"])
	}
	if _, ok := p["revoked_at"]; !ok {
		t.Error("revoked_at missing")
	}
}

func TestBuildTicketRevokedV1Payload_OmitsBlankIssuanceID(t *testing.T) {
	t.Parallel()
	p := buildTicketRevokedV1Payload("tic-1", "", "some_reason")
	if _, ok := p["complimentary_issuance_id"]; ok {
		t.Error("complimentary_issuance_id must be omitted when blank")
	}
}

func TestBuildSessionCancelledPayload_FullAndMinimal(t *testing.T) {
	t.Parallel()

	full := buildSessionCancelledPayload("sess-1", "evt-1", "scheduled")
	if full["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want sess-1", full["session_id"])
	}
	if full["event_id"] != "evt-1" {
		t.Errorf("event_id = %v, want evt-1", full["event_id"])
	}
	if full["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", full["status"])
	}
	if full["previous_status"] != "scheduled" {
		t.Errorf("previous_status = %v, want scheduled", full["previous_status"])
	}
	if _, ok := full["cancelled_at"]; !ok {
		t.Error("cancelled_at missing")
	}

	minimal := buildSessionCancelledPayload("sess-2", "", "")
	if _, ok := minimal["event_id"]; ok {
		t.Error("event_id must be omitted when blank")
	}
	if _, ok := minimal["previous_status"]; ok {
		t.Error("previous_status must be omitted when blank")
	}
}

// Publish helpers must be safe to call when the server has no outbox writer
// wired (e.g. unit-test servers without a database pool).  publishScannerEvent
// already no-ops in that case; we exercise each new publisher to lock that in.
func TestPublishCatalogEvents_NoOpWithoutOutbox(t *testing.T) {
	t.Parallel()

	s := &Server{} // pool and outboxWriter both nil
	ctx := context.Background()

	// None of these should panic or block.
	s.publishTicketRefundedV1Events(ctx, []string{"tic-1", "tic-2"}, "cs-1", "rf-1", "EUR", 100)
	s.publishTicketRevokedV1Events(ctx, []string{"tic-1"}, "iss-1", "complimentary_revocation")
	s.publishSessionCancelledEvent(ctx, "sess-1", "evt-1", "scheduled")

	// Empty ticket-id slices must not iterate or panic either.
	s.publishTicketRefundedV1Events(ctx, nil, "cs-1", "rf-1", "EUR", 0)
	s.publishTicketRevokedV1Events(ctx, nil, "", "")
}
