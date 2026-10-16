package eventbot

// scan_view.go — how the bot says what the door did with a ticket (task M2):
// the order card's ticket line ("✔ entered 01.11.2026 19:32 · MACS · gate 2")
// and the "Not entered" list of a date. The scan rows come from the API
// (openapi.TicketScan); the source is spelled in words, the gate or the
// scanner's own name follows.

import (
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Who reported a scan, as the API spells it.
const (
	scanSourceArena = "arena_scanner"
	scanSourceMACS  = "macs"
	scanAdmitted    = "admitted"
)

// scanSourceKey is the catalog key that names who reported a scan.
func scanSourceKey(source string) string {
	switch source {
	case scanSourceMACS:
		return "bot.ord.scan_src_macs"
	case scanSourceArena:
		return "bot.ord.scan_src_arena_scanner"
	}
	return ""
}

// scanDetail is " · MACS · gate 2": the reporting system and the gate (else
// the scanner's own name). Empty when the scan names neither.
func (b *Bot) scanDetail(loc, source, gate, scanner string) string {
	var out strings.Builder
	if key := scanSourceKey(source); key != "" {
		out.WriteString(" · " + b.texts.T(loc, key, nil))
	}
	switch {
	case strings.TrimSpace(gate) != "":
		out.WriteString(b.texts.T(loc, "bot.ord.scan_gate", map[string]any{"Gate": Esc(strings.TrimSpace(gate))}))
	case strings.TrimSpace(scanner) != "":
		out.WriteString(" · " + Esc(strings.TrimSpace(scanner)))
	}
	return out.String()
}

// firstAdmittedScan is the earliest admitted scan among a ticket's scans
// (the API lists them newest first, up to five). nil when none is listed,
// as for a ticket of the legacy import that carries only used_at.
func firstAdmittedScan(scans []openapi.TicketScan) *openapi.TicketScan {
	for i := len(scans) - 1; i >= 0; i-- {
		if scans[i].Result == scanAdmitted {
			return &scans[i]
		}
	}
	return nil
}

// ticketScans is the ticket's scan list, empty for an older server that does
// not send it.
func ticketScans(t openapi.OrderTicketSummary) []openapi.TicketScan {
	if t.Scans == nil {
		return nil
	}
	return *t.Scans
}

// entryLine is the ticket line's last part. A ticket that was let in says
// when and, when the API listed the admitting scan, who reported it and at
// which gate. A ticket that was never let in but was turned away at a gate
// says so, with that scan's time, so the organizer sees why it is outside.
func (b *Bot) entryLine(loc string, t openapi.OrderTicketSummary, tz string) string {
	scans := ticketScans(t)
	if t.UsedAt != nil {
		out := b.texts.T(loc, "bot.ord.entered", map[string]any{"When": Esc(FormatWhen(*t.UsedAt, tz))})
		if s := firstAdmittedScan(scans); s != nil {
			out += b.scanDetail(loc, s.Source, s.Gate, s.ScannerLabel)
		}
		return out
	}
	if len(scans) > 0 && scans[0].Result != scanAdmitted {
		s := scans[0]
		return b.texts.T(loc, "bot.ord.scan_denied", map[string]any{"When": Esc(FormatWhen(s.ScannedAt, tz))}) +
			b.scanDetail(loc, s.Source, s.Gate, s.ScannerLabel)
	}
	return ""
}
