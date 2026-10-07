package eventwatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// maxChangeLines bounds a "changed" message: a rescheduled tour of twenty
// dates must not become a wall of text.
const maxChangeLines = 10

// maxListedDates bounds the dates a "new event" message lists.
const maxListedDates = 5

// humanTimeLayout is how a session start reads in a chat message — the same
// layout the sales messages use.
const humanTimeLayout = "Mon 02 Jan 2006, 15:04"

func esc(s string) string { return opsalert.EscapeHTML(s) }

func money(amount int64, currency string) string {
	return opsalert.FormatMinorUnits(amount, strings.TrimSpace(currency))
}

// when renders a stored RFC 3339 start in the venue's own zone (UTC, labelled,
// when the zone is unknown), the way the buyer's ticket shows it.
func when(rfc3339, tz string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	if tz != "" {
		if loc, lerr := time.LoadLocation(tz); lerr == nil {
			// allow:timeformat: human-readable Telegram text, not a wire timestamp
			return t.In(loc).Format(humanTimeLayout)
		}
	}
	// allow:timeformat: human-readable Telegram text, not a wire timestamp
	return t.UTC().Format(humanTimeLayout) + " UTC"
}

// shortDay renders the day of a stored RFC 3339 time for a price window.
func shortDay(rfc3339, tz string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	if tz != "" {
		if loc, lerr := time.LoadLocation(tz); lerr == nil {
			t = t.In(loc)
		}
	}
	// allow:timeformat: human-readable Telegram text, not a wire timestamp
	return t.Format("02 Jan 2006")
}

// tierPrice says what a category costs: its price and, when it has them, the
// price windows that replace it from a given day.
func tierPrice(t TierSnap, tz string) string {
	switch t.Mode {
	case "free":
		return "free"
	case "pwyw":
		return "pay what you want"
	}
	out := money(t.Price, t.Currency)
	for _, w := range t.Windows {
		out += fmt.Sprintf(" · %s from %s", money(w.Price, t.Currency), shortDay(w.From, tz))
	}
	return out
}

// Link is the buyers' page of an event: <base>/<page slug>/<event slug>. Empty
// when the base address or either slug is not known.
func Link(base string, e Event) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || e.PageSlug == "" || e.Slug == "" {
		return ""
	}
	return base + "/" + e.PageSlug + "/" + e.Slug
}

// liveSessions are the dates that are still on, in time order.
func liveSessions(s Snapshot) []SessionSnap {
	var out []SessionSnap
	for _, ses := range s.Sessions {
		if !ses.Cancelled {
			out = append(out, ses)
		}
	}
	return out
}

// NewEventText is the message for an event published for the first time: its
// name, its dates, what the categories cost, whether it has a poster and the
// buyers' link.
func NewEventText(e Event, base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🆕 <b>New event published</b> — %s\n\n", esc(e.OrgName))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(e.Snapshot.Name))

	dates := liveSessions(e.Snapshot)
	if len(dates) == 0 {
		b.WriteString("📅 no dates yet\n")
	}
	for i, d := range dates {
		if i == maxListedDates {
			fmt.Fprintf(&b, "📅 …and %d more date(s)\n", len(dates)-maxListedDates)
			break
		}
		fmt.Fprintf(&b, "📅 %s\n", esc(when(d.Start, d.TZ)))
	}
	// The categories of the first date stand for the event.
	if len(dates) > 0 && len(dates[0].Tiers) > 0 {
		parts := make([]string, 0, len(dates[0].Tiers))
		for _, t := range dates[0].Tiers {
			parts = append(parts, fmt.Sprintf("%s %s", esc(t.Name), esc(tierPrice(t, dates[0].TZ))))
		}
		fmt.Fprintf(&b, "🎫 %s\n", strings.Join(parts, " · "))
	}
	if e.Snapshot.Poster != "" {
		b.WriteString("🖼 poster: yes\n")
	} else {
		b.WriteString("🖼 poster: none\n")
	}
	if link := Link(base, e); link != "" {
		fmt.Fprintf(&b, "\n🔗 %s", esc(link))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ChangedText is the message for a published event that changed: what changed,
// as before → after lines. It is "" when nothing a buyer sees differs, which
// the caller treats as "record it and say nothing".
func ChangedText(old Snapshot, e Event, base string) string {
	lines := Diff(old, e.Snapshot)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✏️ <b>Event changed</b> — %s\n\n", esc(e.OrgName))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(e.Snapshot.Name))
	for _, l := range lines {
		fmt.Fprintf(&b, "• %s\n", l)
	}
	if link := Link(base, e); link != "" {
		fmt.Fprintf(&b, "\n🔗 %s", esc(link))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Diff lists what differs between the snapshot announced last and the current
// one: the name, the poster, the dates (added, moved, cancelled, removed) and
// the prices of categories a date already had. Each line is HTML-escaped. The
// same line coming from several dates is shown once with a count.
func Diff(old, cur Snapshot) []string {
	var order []string
	count := map[string]int{}
	add := func(line string) {
		if count[line] == 0 {
			order = append(order, line)
		}
		count[line]++
	}

	if old.Name != cur.Name {
		add(fmt.Sprintf("Name: «%s» → «%s»", esc(old.Name), esc(cur.Name)))
	}
	switch {
	case old.Poster == cur.Poster:
	case old.Poster == "":
		add("Poster: added")
	case cur.Poster == "":
		add("Poster: removed")
	default:
		add("Poster: replaced")
	}

	oldBy := map[string]SessionSnap{}
	for _, s := range old.Sessions {
		oldBy[s.ID] = s
	}
	curBy := map[string]SessionSnap{}
	for _, s := range cur.Sessions {
		curBy[s.ID] = s
	}

	for _, s := range cur.Sessions {
		o, existed := oldBy[s.ID]
		if !existed {
			add(fmt.Sprintf("Date added: %s", esc(when(s.Start, s.TZ))))
			continue
		}
		if o.Start != s.Start {
			add(fmt.Sprintf("Date moved: %s → %s", esc(when(o.Start, s.TZ)), esc(when(s.Start, s.TZ))))
		}
		if !o.Cancelled && s.Cancelled {
			add(fmt.Sprintf("Date cancelled: %s", esc(when(o.Start, s.TZ))))
		}
		if o.Cancelled && !s.Cancelled {
			add(fmt.Sprintf("Date back on: %s", esc(when(s.Start, s.TZ))))
		}
		// Prices of a date that existed before: a new date's prices are part of
		// "Date added" and a new event's are in its own message.
		oldTier := map[string]TierSnap{}
		for _, t := range o.Tiers {
			oldTier[t.ID] = t
		}
		seen := map[string]bool{}
		for _, t := range s.Tiers {
			seen[t.ID] = true
			ot, had := oldTier[t.ID]
			if !had {
				add(fmt.Sprintf("Category added «%s»: %s", esc(t.Name), esc(tierPrice(t, s.TZ))))
				continue
			}
			if before, after := tierPrice(ot, s.TZ), tierPrice(t, s.TZ); before != after {
				add(fmt.Sprintf("Price «%s»: %s → %s", esc(t.Name), esc(before), esc(after)))
			} else if ot.Name != t.Name {
				add(fmt.Sprintf("Category renamed: «%s» → «%s»", esc(ot.Name), esc(t.Name)))
			}
		}
		for _, t := range o.Tiers {
			if !seen[t.ID] {
				add(fmt.Sprintf("Category removed «%s»", esc(t.Name)))
			}
		}
	}
	for _, o := range old.Sessions {
		if _, still := curBy[o.ID]; !still {
			add(fmt.Sprintf("Date removed: %s", esc(when(o.Start, o.TZ))))
		}
	}

	lines := make([]string, 0, len(order))
	for _, l := range order {
		if n := count[l]; n > 1 {
			l = fmt.Sprintf("%s (× %d dates)", l, n)
		}
		lines = append(lines, l)
	}
	if len(lines) > maxChangeLines {
		more := len(lines) - maxChangeLines
		lines = append(lines[:maxChangeLines], fmt.Sprintf("…and %d more change(s)", more))
	}
	return lines
}
