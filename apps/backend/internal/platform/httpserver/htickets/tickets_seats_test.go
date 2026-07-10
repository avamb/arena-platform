// tickets_seats_test.go — SEAT-C3 (feature #311) response-shape contract.
//
// TicketFromRow is the projection from the DB row into the JSON response
// body returned by GET /v1/checkout/{id}/tickets. This test pins the
// SEAT-C3 additive extension: seated tickets surface seat_key /
// seat_sector / seat_row / seat_number; GA tickets omit them (nil
// pointers → omitted via omitempty).
package htickets

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func sp(s string) *string { return &s }

func baseRow() gen.TicketRow {
	return gen.TicketRow{
		ID:                uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		CheckoutSessionID: uuid.MustParse("22222222-3333-4444-5555-666666666666"),
		SessionID:         uuid.MustParse("33333333-4444-5555-6666-777777777777"),
		Status:            "active",
		IssuedAt:          time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		CreatedAt:         time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
}

// TestSeatC3_TicketFromRow_SeatedTicket verifies that a seated ticket
// projects all four seat_* fields into the response body and that the
// JSON encoding preserves them as top-level string values.
func TestSeatC3_TicketFromRow_SeatedTicket(t *testing.T) {
	row := baseRow()
	row.SeatKey = sp("A|3|12")
	row.SeatSector = sp("A")
	row.SeatRow = sp("3")
	row.SeatNumber = sp("12")

	resp := TicketFromRow(row)
	if got := deref(resp.SeatKey); got != "A|3|12" {
		t.Errorf("SeatKey: got %q want %q", got, "A|3|12")
	}
	if got := deref(resp.SeatSector); got != "A" {
		t.Errorf("SeatSector: got %q want %q", got, "A")
	}
	if got := deref(resp.SeatRow); got != "3" {
		t.Errorf("SeatRow: got %q want %q", got, "3")
	}
	if got := deref(resp.SeatNumber); got != "12" {
		t.Errorf("SeatNumber: got %q want %q", got, "12")
	}

	// JSON contract check: keys are present with string values.
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	for _, kv := range []string{
		`"seat_key":"A|3|12"`,
		`"seat_sector":"A"`,
		`"seat_row":"3"`,
		`"seat_number":"12"`,
	} {
		if !strings.Contains(body, kv) {
			t.Errorf("JSON body missing %q\n---\n%s", kv, body)
		}
	}
}

// TestSeatC3_TicketFromRow_GATicket verifies that a GA ticket has all
// four seat_* pointers nil in the response struct and that the JSON
// encoding omits them entirely (`omitempty` on the tag). Scanner /
// wallet consumers that pre-date SEAT-C3 must see byte-identical
// payloads for GA tickets.
func TestSeatC3_TicketFromRow_GATicket(t *testing.T) {
	resp := TicketFromRow(baseRow())

	if resp.SeatKey != nil || resp.SeatSector != nil ||
		resp.SeatRow != nil || resp.SeatNumber != nil {
		t.Fatalf("expected all seat_* fields nil, got %+v", resp)
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	for _, banned := range []string{
		"seat_key", "seat_sector", "seat_row", "seat_number",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("GA JSON body must not contain %q\n---\n%s", banned, body)
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
