// scanner_events_seats_test.go — SEAT-C3 (feature #311) contract test for
// the ticket lifecycle webhook payload.
//
// Pins the additive-only extension to v1.scanner.ticket.issued:
//
//   - Seated tickets (SeatKey / SeatSector / SeatRow / SeatNumber all
//     non-nil) surface as top-level string keys seat_key / seat_sector /
//     seat_row / seat_number.
//   - GA tickets (all four nil) omit all four keys entirely so scanner
//     subscribers that don't understand seating keep working unchanged.
//   - Every other key present on the pre-#311 payload (ticket_id,
//     checkout_session_id, session_id, status, bil24_order_status,
//     issued_at, and the optional tier_id / holder_email) must remain
//     unchanged in name and shape — the change is strictly additive.
package hscanner

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func strPtr(s string) *string { return &s }

func baseTicketRow() gen.TicketRow {
	return gen.TicketRow{
		ID:                uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		CheckoutSessionID: uuid.MustParse("22222222-3333-4444-5555-666666666666"),
		SessionID:         uuid.MustParse("33333333-4444-5555-6666-777777777777"),
		Status:            "active",
		IssuedAt:          time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestBuildTicketIssuedPayload_GATicket_OmitsSeatFields(t *testing.T) {
	got := BuildTicketIssuedPayload(baseTicketRow())

	// Core keys must all be present unchanged.
	for _, k := range []string{
		"ticket_id", "checkout_session_id", "session_id",
		"status", "bil24_order_status", "issued_at",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("GA payload missing required key %q", k)
		}
	}
	// SEAT-C3 fields must all be absent for GA tickets.
	for _, k := range []string{
		"seat_key", "seat_sector", "seat_row", "seat_number",
	} {
		if v, ok := got[k]; ok {
			t.Errorf("GA payload must not contain %q (got %v)", k, v)
		}
	}
}

func TestBuildTicketIssuedPayload_SeatedTicket_IncludesSeatFields(t *testing.T) {
	tr := baseTicketRow()
	tr.SeatKey = strPtr("A|3|12")
	tr.SeatSector = strPtr("A")
	tr.SeatRow = strPtr("3")
	tr.SeatNumber = strPtr("12")

	got := BuildTicketIssuedPayload(tr)

	// Additive keys must be present with their string values.
	want := map[string]string{
		"seat_key":    "A|3|12",
		"seat_sector": "A",
		"seat_row":    "3",
		"seat_number": "12",
	}
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("seated payload missing key %q", k)
			continue
		}
		gs, ok := g.(string)
		if !ok {
			t.Errorf("seated payload key %q: expected string, got %T", k, g)
			continue
		}
		if gs != v {
			t.Errorf("seated payload key %q: got %q want %q", k, gs, v)
		}
	}
	// Core keys must still be present — the extension must be additive
	// only, never a rename or removal.
	for _, k := range []string{
		"ticket_id", "checkout_session_id", "session_id",
		"status", "bil24_order_status", "issued_at",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("seated payload missing pre-SEAT-C3 key %q", k)
		}
	}
}

// TestBuildTicketIssuedPayload_PartialSeatFields_IndependentlyEmitted
// documents the intentional per-field omit behaviour: each seat_* key
// is guarded by its own nil check, so a partially-populated ticket row
// only emits the non-nil subset. In practice IssueTicketsForCheckout
// always populates all four together (or none), but this test pins the
// per-field contract so future callers can rely on it.
func TestBuildTicketIssuedPayload_PartialSeatFields_IndependentlyEmitted(t *testing.T) {
	tr := baseTicketRow()
	tr.SeatSector = strPtr("B")
	// SeatKey, SeatRow, SeatNumber intentionally left nil.

	got := BuildTicketIssuedPayload(tr)

	if v, ok := got["seat_sector"]; !ok || v.(string) != "B" {
		t.Errorf("expected seat_sector=B in payload, got %v (present=%v)", v, ok)
	}
	for _, banned := range []string{"seat_key", "seat_row", "seat_number"} {
		if _, ok := got[banned]; ok {
			t.Errorf("unexpected key %q present when its source was nil", banned)
		}
	}
}
