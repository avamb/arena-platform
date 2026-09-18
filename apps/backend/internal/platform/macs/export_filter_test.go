// export_filter_test.go — unit tests for the ?tickets= export modes
// (owner decision 2026-09-18, see export_filter.go).
package macs

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

func TestParseExportMode(t *testing.T) {
	cases := []struct {
		raw    string
		want   ExportMode
		wantOK bool
	}{
		{"", ExportModeAll, true},
		{"all", ExportModeAll, true},
		{"valid", ExportModeValid, true},
		{"revoked", ExportModeRevoked, true},
		{"bogus", "", false},
		{"ALL", "", false}, // case-sensitive: only the documented lowercase values
	}
	for _, c := range cases {
		got, ok := ParseExportMode(c.raw)
		if ok != c.wantOK {
			t.Errorf("ParseExportMode(%q) ok = %v, want %v", c.raw, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseExportMode(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// twoTicketOrder builds one checkout session with two tickets: one active,
// one in the given terminal status with the given refund/cancel timestamps.
func twoTicketOrder(status string, refundDate, cancelledAt *time.Time) []orderexport.Row {
	active := baseRow()
	active.TicketID = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	active.SystemTicketID = 2001
	active.Ordinal = 1

	revoked := baseRow()
	revoked.TicketID = uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	revoked.SystemTicketID = 2002
	revoked.Ordinal = 2
	revoked.TicketStatus = status
	revoked.RefundDate = refundDate
	revoked.CancelledAt = cancelledAt

	return []orderexport.Row{active, revoked}
}

func TestFilterOrders_AllModeIsUnfiltered(t *testing.T) {
	orders := orderexport.Build(twoTicketOrder("cancelled", nil, nil))
	out := filterOrders(orders, ExportModeAll, nil)
	if len(out) != 1 || len(out[0].Tickets) != 2 {
		t.Fatalf("ExportModeAll must pass every ticket through unchanged, got %+v", out)
	}
}

func TestFilterOrders_ValidModeDropsTerminalTickets(t *testing.T) {
	orders := orderexport.Build(twoTicketOrder("cancelled", nil, nil))
	out := filterOrders(orders, ExportModeValid, nil)
	if len(out) != 1 || len(out[0].Tickets) != 1 {
		t.Fatalf("expected 1 order with 1 valid ticket, got %+v", out)
	}
	if out[0].Tickets[0].PlatformStatus != "active" {
		t.Fatalf("expected the surviving ticket to be active, got %q", out[0].Tickets[0].PlatformStatus)
	}
}

func TestFilterOrders_ValidModeOmitsOrderWithNoValidTickets(t *testing.T) {
	row := baseRow()
	row.TicketStatus = "revoked"
	orders := orderexport.Build([]orderexport.Row{row})
	out := filterOrders(orders, ExportModeValid, nil)
	if len(out) != 0 {
		t.Fatalf("expected the order to be omitted entirely, got %+v", out)
	}
}

func TestFilterOrders_RevokedModeKeepsOnlyTerminalTickets(t *testing.T) {
	orders := orderexport.Build(twoTicketOrder("revoked", nil, nil))
	out := filterOrders(orders, ExportModeRevoked, nil)
	if len(out) != 1 || len(out[0].Tickets) != 1 {
		t.Fatalf("expected 1 order with 1 revoked ticket, got %+v", out)
	}
	if out[0].Tickets[0].PlatformStatus != "revoked" {
		t.Fatalf("expected the surviving ticket to be revoked, got %q", out[0].Tickets[0].PlatformStatus)
	}
}

func TestFilterOrders_RevokedModeOmitsOrderWithNoRevokedTickets(t *testing.T) {
	row := baseRow() // active only
	orders := orderexport.Build([]orderexport.Row{row})
	out := filterOrders(orders, ExportModeRevoked, nil)
	if len(out) != 0 {
		t.Fatalf("expected the order to be omitted entirely, got %+v", out)
	}
}

func TestFilterOrders_RevokedSince_UsesRefundDateWhenPresent(t *testing.T) {
	cutoff := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)
	after := cutoff.Add(time.Hour)

	// Refunded before the cutoff: excluded.
	oldOrders := orderexport.Build(twoTicketOrder("cancelled", &before, nil))
	if out := filterOrders(oldOrders, ExportModeRevoked, &cutoff); len(out) != 0 {
		t.Fatalf("expected a refund before the cutoff to be excluded, got %+v", out)
	}

	// Refunded after the cutoff: included.
	newOrders := orderexport.Build(twoTicketOrder("cancelled", &after, nil))
	out := filterOrders(newOrders, ExportModeRevoked, &cutoff)
	if len(out) != 1 || len(out[0].Tickets) != 1 {
		t.Fatalf("expected a refund after the cutoff to be included, got %+v", out)
	}
}

func TestFilterOrders_RevokedSince_FallsBackToCancelledAtWithoutRefund(t *testing.T) {
	cutoff := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	after := cutoff.Add(time.Hour)

	// A "none" refund-mode cancellation: no refund_date, but cancelled_at is
	// set and after the cutoff — must still be picked up.
	orders := orderexport.Build(twoTicketOrder("cancelled", nil, &after))
	out := filterOrders(orders, ExportModeRevoked, &cutoff)
	if len(out) != 1 || len(out[0].Tickets) != 1 {
		t.Fatalf("expected the cancelled-without-refund ticket to be included, got %+v", out)
	}
}

func TestFilterOrders_RevokedSince_ExcludesWhenNoTimestampAtAll(t *testing.T) {
	cutoff := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// Neither refund_date nor cancelled_at set: cannot prove it changed
	// since the cutoff, so it is excluded rather than guessed at.
	orders := orderexport.Build(twoTicketOrder("revoked", nil, nil))
	out := filterOrders(orders, ExportModeRevoked, &cutoff)
	if len(out) != 0 {
		t.Fatalf("expected the timestamp-less revoked ticket to be excluded, got %+v", out)
	}
}
