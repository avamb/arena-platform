package salesnotify

import (
	"strings"
	"testing"
)

// The events-bot link (spec 28 §10 step 7) is appended only when a bot is
// configured, once, at the very end, and survives an "@" in the setting.
func TestEventsBotFooter(t *testing.T) {
	if got := EventsBotFooter(""); got != "" {
		t.Fatalf("empty username must yield no footer, got %q", got)
	}
	got := EventsBotFooter("@ArenaEventsCentrBot")
	if !strings.Contains(got, `href="https://t.me/ArenaEventsCentrBot"`) || !strings.Contains(got, "@ArenaEventsCentrBot") || strings.Contains(got, "@@") {
		t.Fatalf("footer = %q", got)
	}

	d := NewDispatcher(nil, nil, nil)
	if d.withFooter("sale") != "sale" {
		t.Fatal("no bot configured: text must stay as it is")
	}
	d.WithEventsBot("ArenaEventsCentrBot")
	text := d.withFooter(FormatSale(Sale{OrgName: "Vino&Co", EventName: "Quiz", Currency: "ILS", Total: 100}))
	if !strings.HasSuffix(text, EventsBotFooter("ArenaEventsCentrBot")) || strings.Count(text, "t.me/") != 1 {
		t.Fatalf("sale with footer:\n%s", text)
	}
	if !strings.Contains(text, "New sale") {
		t.Fatalf("the sale body must stay first:\n%s", text)
	}
}
