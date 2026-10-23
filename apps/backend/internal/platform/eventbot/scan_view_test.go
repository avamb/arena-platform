package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func scanAt(min int, result, source, gate, label string) openapi.TicketScan {
	return openapi.TicketScan{
		ScannedAt: time.Date(2026, 11, 1, 18, min, 0, 0, time.UTC), Result: result, Source: source, Gate: gate, ScannerLabel: label,
	}
}

func ticketWith(used *time.Time, scans ...openapi.TicketScan) openapi.OrderTicketSummary {
	return openapi.OrderTicketSummary{UsedAt: used, Scans: &scans}
}

func TestEntryLine(t *testing.T) {
	b := ecTestBot(t)
	used := time.Date(2026, 11, 1, 18, 32, 0, 0, time.UTC)
	tz := "Europe/Madrid" // 19:32 on 01.11.2026

	cases := []struct {
		name     string
		loc      string
		ticket   openapi.OrderTicketSummary
		contains []string
		absent   []string
	}{
		{"macs with a gate", "en",
			ticketWith(&used, scanAt(40, "denied", scanSourceArena, "North", ""), scanAt(32, "admitted", scanSourceMACS, "Gate 2", "Scanner 7")),
			[]string{"entered 01.11.2026 19:32", "MACS", "gate Gate 2"}, []string{"turned away", "Scanner 7"}},
		{"russian wording", "ru",
			ticketWith(&used, scanAt(32, "admitted", scanSourceMACS, "2", "")),
			[]string{"01.11.2026 19:32", "МАКС", "вход 2"}, nil},
		{"arena scanner, no gate, falls back to the scanner name", "en",
			ticketWith(&used, scanAt(32, "admitted", scanSourceArena, "", "Door tablet")),
			[]string{"Arena scanner", "Door tablet"}, []string{"gate"}},
		{"legacy: used_at and no scan rows", "en",
			ticketWith(&used), []string{"entered 01.11.2026 19:32"}, []string{"MACS", "Arena scanner", "gate"}},
		{"turned away and never let in", "en",
			ticketWith(nil, scanAt(10, "denied", scanSourceArena, "North", "")),
			[]string{"turned away 01.11.2026 19:10", "North"}, []string{"entered"}},
		{"never scanned", "en", ticketWith(nil), nil, []string{"entered", "turned away"}},
		{"an older server sends no scans at all", "en",
			openapi.OrderTicketSummary{UsedAt: &used}, []string{"entered"}, []string{"MACS"}},
	}
	for _, c := range cases {
		got := b.entryLine(c.loc, c.ticket, tz)
		for _, want := range c.contains {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q lacks %q", c.name, got, want)
			}
		}
		for _, no := range c.absent {
			if strings.Contains(got, no) {
				t.Errorf("%s: %q must not contain %q", c.name, got, no)
			}
		}
	}
	if got := b.entryLine("en", ticketWith(nil), tz); got != "" {
		t.Errorf("a never-scanned ticket prints %q, want nothing", got)
	}
}

func TestFirstAdmittedScan_IsTheEarliestOfTheNewestFirstList(t *testing.T) {
	scans := []openapi.TicketScan{
		scanAt(50, "admitted", scanSourceArena, "late", ""),
		scanAt(40, "denied", scanSourceArena, "x", ""),
		scanAt(32, "admitted", scanSourceMACS, "first", ""),
	}
	if s := firstAdmittedScan(scans); s == nil || s.Gate != "first" {
		t.Errorf("first admitted = %+v", s)
	}
	if s := firstAdmittedScan(scans[1:2]); s != nil {
		t.Errorf("a denied scan alone is not an entry: %+v", s)
	}
}

func TestNotEnteredLine(t *testing.T) {
	b := ecTestBot(t)
	num := int64(1000000501)
	name := "Anna <b>"
	seat := "A / 3 / 12"
	tier := "Parterre"
	it := openapi.SessionTicketItem{
		SystemTicketId: 349, TierName: &tier, SeatLabel: &seat, OrderNumber: &num, BuyerName: &name,
		LastScan: &openapi.TicketScan{
			ScannedAt: time.Date(2026, 11, 1, 18, 10, 0, 0, time.UTC), Result: "denied", Source: scanSourceArena, Gate: "North",
		},
	}
	it.TicketId = uuid.New()
	private := b.notEnteredLine("en", it, "Europe/Madrid", true)
	for _, want := range []string{"<code>349</code>", "№1000000501", "Parterre", "seat A / 3 / 12", "Anna &lt;b&gt;", "turned away 01.11.2026 19:10", "North"} {
		if !strings.Contains(private, want) {
			t.Errorf("private line %q lacks %q", private, want)
		}
	}
	if group := b.notEnteredLine("en", it, "Europe/Madrid", false); strings.Contains(group, "Anna") {
		t.Errorf("a group chat line carries the buyer's name: %q", group)
	}
	// An invitation: no order number, no scan.
	inv := b.notEnteredLine("en", openapi.SessionTicketItem{SystemTicketId: 5}, "", true)
	if !strings.Contains(inv, "invitation") || strings.Contains(inv, "turned away") {
		t.Errorf("invitation line = %q", inv)
	}
}

func TestNotEnteredPages(t *testing.T) {
	for n, want := range map[int64]int{0: 1, 1: 1, 10: 1, 11: 2, 25: 3} {
		if got := notEnteredPages(n); got != want {
			t.Errorf("pages(%d) = %d, want %d", n, got, want)
		}
	}
}
