package horders

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestParseEnteredFilter(t *testing.T) {
	cases := []struct {
		query  string
		wantOK bool
		want   *bool
	}{
		{"", true, nil},
		{"?entered=", true, nil},
		{"?entered=true", true, boolp(true)},
		{"?entered=FALSE", true, boolp(false)},
		{"?entered=maybe", false, nil},
		{"?entered=1", false, nil},
	}
	for _, c := range cases {
		got, ok := parseEnteredFilter(httptest.NewRequest("GET", "/x"+c.query, nil))
		if ok != c.wantOK || (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%q: got %v ok=%v, want %v ok=%v", c.query, got, ok, c.want, c.wantOK)
		}
	}
}

func boolp(b bool) *bool { return &b }

func TestSessionTicketEntry(t *testing.T) {
	at := time.Date(2026, 11, 1, 18, 32, 0, 0, time.UTC)
	order, num := uuid.New(), int64(1000000501)
	label, gate, res, src := "Scanner 7", "Gate 2", "admitted", "macs"
	row := gen.SessionTicketEntryRow{
		TicketID: uuid.New(), SystemTicketID: 349, Status: "active",
		SeatSector: strp("A"), SeatRow: strp("3"), SeatNumber: strp("12"),
		OrderID: &order, OrderNumber: &num, BuyerName: strp("Anna"),
		EnteredAt: &at, LastScannedAt: &at, LastResult: &res, LastSource: &src,
		LastScannerLabel: &label, LastGate: &gate,
	}
	it := sessionTicketEntry(row)
	if !it.Entered || *it.EnteredAt != "2026-11-01T18:32:00Z" || *it.SeatLabel != "A / 3 / 12" ||
		*it.OrderID != order.String() || it.LastScan == nil || it.LastScan.Source != "macs" ||
		it.LastScan.ScannerLabel != "Scanner 7" || it.LastScan.Gate != "Gate 2" {
		t.Errorf("entry = %+v last=%+v", it, it.LastScan)
	}

	// An invitation nobody scanned: no order, no entry, no last scan.
	bare := sessionTicketEntry(gen.SessionTicketEntryRow{TicketID: uuid.New(), Status: "active"})
	if bare.Entered || bare.EnteredAt != nil || bare.LastScan != nil || bare.OrderID != nil || bare.SeatLabel != nil {
		t.Errorf("bare entry = %+v", bare)
	}
}

func strp(s string) *string { return &s }

func TestScanEntryShape(t *testing.T) {
	m := scanEntry(gen.TicketScanRow{
		ScannedAt: time.Date(2026, 11, 1, 18, 32, 0, 0, time.UTC), Result: "denied", Source: "arena_scanner", Gate: "North",
	})
	if m["scanned_at"] != "2026-11-01T18:32:00Z" || m["result"] != "denied" || m["source"] != "arena_scanner" ||
		m["scanner_label"] != "" || m["gate"] != "North" {
		t.Errorf("scan entry = %v", m)
	}
}
