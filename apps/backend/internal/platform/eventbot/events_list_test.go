package eventbot

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func evItem(name, state string, next, last string) openapi.EventItem {
	e := openapi.EventItem{Id: uuid.New(), Name: name, SalesState: state, SessionCount: 1}
	if next != "" {
		e.NextSessionAt = ts(next)
		e.FirstSessionAt = ts(next)
	}
	if last != "" {
		e.LastSessionAt = ts(last)
	}
	return e
}

func names(events []openapi.EventItem) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Name)
	}
	return out
}

func TestEventChip(t *testing.T) {
	t.Parallel()
	for state, want := range map[string]string{
		"on_sale": "●", "upcoming": "○", "sold_out": "✕", "archived": "✓", "": "·", "mystery": "·",
	} {
		if got := EventChip(state); got != want {
			t.Errorf("EventChip(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestEventInFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state   string
		running bool
	}{
		{"on_sale", true}, {"upcoming", true}, {"sold_out", true}, {"", true}, {"archived", false},
	}
	for _, c := range cases {
		e := openapi.EventItem{SalesState: c.state}
		if got := EventInFilter(e, evFilterRun); got != c.running {
			t.Errorf("%q in running = %v, want %v", c.state, got, c.running)
		}
		if got := EventInFilter(e, evFilterArc); got != !c.running {
			t.Errorf("%q in archive = %v, want %v", c.state, got, !c.running)
		}
	}
}

func TestFilterEvents_RunningAndArchiveOrder(t *testing.T) {
	t.Parallel()
	events := []openapi.EventItem{
		evItem("far", "on_sale", "2026-12-01T20:00:00Z", "2026-12-01T22:00:00Z"),
		evItem("soon", "upcoming", "2026-10-05T20:00:00Z", "2026-10-05T22:00:00Z"),
		evItem("full", "sold_out", "2026-11-01T20:00:00Z", "2026-11-01T22:00:00Z"),
		evItem("nodate", "on_sale", "", ""),
		evItem("old", "archived", "", "2026-01-01T22:00:00Z"),
		evItem("newer-old", "archived", "", "2026-09-01T22:00:00Z"),
	}
	run := FilterEvents(events, evFilterRun, "")
	if got, want := strings.Join(names(run), ","), "soon,full,far,nodate"; got != want {
		t.Errorf("running = %s, want %s", got, want)
	}
	arc := FilterEvents(events, evFilterArc, "")
	if got, want := strings.Join(names(arc), ","), "newer-old,old"; got != want {
		t.Errorf("archive = %s, want %s", got, want)
	}
}

func TestFilterEvents_SearchIsCaseInsensitiveAndPerWord(t *testing.T) {
	t.Parallel()
	events := []openapi.EventItem{
		evItem("Лебединое озеро", "on_sale", "2026-10-05T20:00:00Z", ""),
		evItem("Swan Lake Ballet", "on_sale", "2026-10-06T20:00:00Z", ""),
		evItem("Jazz Night", "on_sale", "2026-10-07T20:00:00Z", ""),
		evItem("Ёлка для всех", "on_sale", "2026-10-08T20:00:00Z", ""),
		evItem("Old Swan", "archived", "", "2026-01-01T22:00:00Z"),
	}
	for query, want := range map[string]string{
		"swan":         "Swan Lake Ballet",
		"BALLET swan":  "Swan Lake Ballet",
		"lake swan":    "Swan Lake Ballet",
		"ЛЕБЕД":        "Лебединое озеро",
		"елка":         "Ёлка для всех",
		"  jazz   ":    "Jazz Night",
		"nothing here": "",
	} {
		got := strings.Join(names(FilterEvents(events, evFilterRun, query)), ",")
		if got != want {
			t.Errorf("search %q = %q, want %q", query, got, want)
		}
	}
	// The search stays inside the filter: the archived "Old Swan" is not in
	// the running list, and is found in the archive.
	if got := strings.Join(names(FilterEvents(events, evFilterArc, "swan")), ","); got != "Old Swan" {
		t.Errorf("archive search = %q", got)
	}
}

func TestCountByFilter(t *testing.T) {
	t.Parallel()
	run, arc := CountByFilter([]openapi.EventItem{{SalesState: "on_sale"}, {SalesState: "sold_out"}, {SalesState: "archived"}, {}})
	if run != 3 || arc != 1 {
		t.Errorf("CountByFilter = %d, %d", run, arc)
	}
}

func TestEventRowLabel(t *testing.T) {
	t.Parallel()
	e := evItem("Concert", "on_sale", "2026-10-05T20:00:00Z", "2026-10-09T22:00:00Z")
	e.SessionCount = 3
	if got := eventRowLabel(e, evFilterRun); got != "● 05.10 · Concert ×3" {
		t.Errorf("running label = %q", got)
	}
	e.SalesState = "archived"
	if got := eventRowLabel(e, evFilterArc); got != "✓ 09.10 · Concert ×3" {
		t.Errorf("archive label = %q (an archive row is dated by its last session)", got)
	}
	d := openapi.EventItem{Name: "Draft", Status: openapi.EventItemStatusDraft, SalesState: "upcoming"}
	if got := eventRowLabel(d, evFilterRun); got != "○ 📝 Draft" {
		t.Errorf("draft label = %q", got)
	}
	long := openapi.EventItem{Name: strings.Repeat("я", 80), SalesState: "on_sale"}
	if n := len([]byte(ItemCallback("el:o", 4))); n > 64 {
		t.Errorf("callback is %d bytes", n)
	}
	if got := []rune(eventRowLabel(long, evFilterRun)); len(got) > 45 {
		t.Errorf("a long name must be cut, label is %d runes", len(got))
	}
}

func TestNewEventsDialogDefaultsToRunning(t *testing.T) {
	t.Parallel()
	st := newEventsDialog(uuid.New())
	if st.Filter != evFilterRun || st.Page != 1 || st.Query != "" {
		t.Errorf("default state = %+v", st)
	}
}

func TestIsEventsCallback(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"el", "ec", "events", "event", "noop"} {
		if !isEventsCallback(p) {
			t.Errorf("%q must keep the list's dialog", p)
		}
	}
	for _, p := range []string{"home", "wz", "team", "ses", "sample", "org", "lang"} {
		if isEventsCallback(p) {
			t.Errorf("%q must end the list's dialog", p)
		}
	}
}

func TestCutRunes(t *testing.T) {
	t.Parallel()
	if got := cutRunes("абвгд", 3); got != "абв" {
		t.Errorf("cutRunes = %q", got)
	}
	if got := cutRunes("ab", 5); got != "ab" {
		t.Errorf("cutRunes short = %q", got)
	}
}
