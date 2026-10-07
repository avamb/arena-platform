package eventwatch

import (
	"fmt"
	"strings"
	"testing"
)

func tier(id, name string, price int64) TierSnap {
	return TierSnap{ID: id, Name: name, Mode: "fixed", Price: price, Currency: "EUR"}
}

func session(id, start string, tiers ...TierSnap) SessionSnap {
	if tiers == nil {
		tiers = []TierSnap{}
	}
	return SessionSnap{ID: id, Start: start, TZ: "Europe/Madrid", Tiers: tiers}
}

func baseSnapshot() Snapshot {
	return Snapshot{
		Name:   "Concert in Madrid",
		Poster: "poster-1",
		Sessions: []SessionSnap{
			session("s1", "2027-01-15T19:00:00Z", tier("t1", "General", 2500), tier("t2", "VIP", 4000)),
		},
	}
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func TestDigest_IgnoresTheVenueZoneButNotWhatABuyerSees(t *testing.T) {
	a := baseSnapshot()
	b := baseSnapshot()
	b.Sessions[0].TZ = "Europe/Prague"
	if Digest(a) != Digest(b) {
		t.Error("a venue zone change must not change the digest")
	}
	for name, mutate := range map[string]func(*Snapshot){
		"name":   func(s *Snapshot) { s.Name = "Other" },
		"poster": func(s *Snapshot) { s.Poster = "poster-2" },
		"date":   func(s *Snapshot) { s.Sessions[0].Start = "2027-01-16T19:00:00Z" },
		"price":  func(s *Snapshot) { s.Sessions[0].Tiers[0].Price = 3000 },
		"cancel": func(s *Snapshot) { s.Sessions[0].Cancelled = true },
	} {
		c := baseSnapshot()
		mutate(&c)
		if Digest(a) == Digest(c) {
			t.Errorf("a %s change must change the digest", name)
		}
	}
}

func TestDiff_ReportsWhatTheOperatorAskedFor(t *testing.T) {
	old := baseSnapshot()

	cur := baseSnapshot()
	cur.Name = "Concert in Madrid, second night"
	cur.Poster = "poster-2"
	cur.Sessions[0].Start = "2027-01-16T19:00:00Z"
	cur.Sessions[0].Tiers[0].Price = 2750

	lines := Diff(old, cur)
	for _, want := range []string{
		"Name: «Concert in Madrid» → «Concert in Madrid, second night»",
		"Poster: replaced",
		"Date moved: Fri 15 Jan 2027, 20:00 → Sat 16 Jan 2027, 20:00", // Madrid is UTC+1 in January
		"Price «General»: 25.00 EUR → 27.50 EUR",
	} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q in:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if hasLine(lines, "VIP") {
		t.Errorf("an unchanged category must not be mentioned:\n%s", strings.Join(lines, "\n"))
	}
}

func TestDiff_PosterAddedAndRemoved(t *testing.T) {
	none, with := baseSnapshot(), baseSnapshot()
	none.Poster = ""
	if got := Diff(none, with); !hasLine(got, "Poster: added") {
		t.Errorf("added: %v", got)
	}
	if got := Diff(with, none); !hasLine(got, "Poster: removed") {
		t.Errorf("removed: %v", got)
	}
}

func TestDiff_DatesAddedCancelledRemovedAndReopened(t *testing.T) {
	old := baseSnapshot()
	old.Sessions = append(old.Sessions, session("s2", "2027-02-01T19:00:00Z", tier("t3", "General", 2500)))

	cur := baseSnapshot()
	cur.Sessions[0].Cancelled = true // s1 cancelled
	// s2 removed; s3 added with its own prices, which must not be listed twice
	cur.Sessions = append(cur.Sessions, session("s3", "2027-03-01T19:00:00Z", tier("t4", "General", 9900)))

	lines := Diff(old, cur)
	for _, want := range []string{"Date cancelled: Fri 15 Jan 2027, 20:00", "Date removed: Mon 01 Feb 2027, 20:00", "Date added: Mon 01 Mar 2027, 20:00"} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q in:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if hasLine(lines, "99.00") || hasLine(lines, "Category added") {
		t.Errorf("the prices of an added date belong to 'Date added':\n%s", strings.Join(lines, "\n"))
	}

	back := Diff(cur, old)
	if !hasLine(back, "Date back on: Fri 15 Jan 2027, 20:00") {
		t.Errorf("reopened: %v", back)
	}
}

func TestDiff_CategoriesAddedRemovedRenamedAndPriceWindows(t *testing.T) {
	old := baseSnapshot()
	cur := baseSnapshot()
	cur.Sessions[0].Tiers = []TierSnap{
		tier("t1", "General admission", 2500),                                  // renamed only
		{ID: "t5", Name: "Early", Mode: "fixed", Price: 1500, Currency: "EUR"}, // added
	} // VIP removed

	lines := Diff(old, cur)
	for _, want := range []string{"Category renamed: «General» → «General admission»", "Category added «Early»: 15.00 EUR", "Category removed «VIP»"} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q in:\n%s", want, strings.Join(lines, "\n"))
		}
	}

	stepped := baseSnapshot()
	stepped.Sessions[0].Tiers[0].Windows = []WindowSnap{{From: "2027-01-01T00:00:00Z", Price: 3000}}
	if got := Diff(baseSnapshot(), stepped); !hasLine(got, "Price «General»: 25.00 EUR → 25.00 EUR · 30.00 EUR from 01 Jan 2027") {
		t.Errorf("price window: %v", got)
	}

	free := baseSnapshot()
	free.Sessions[0].Tiers[0] = TierSnap{ID: "t1", Name: "General", Mode: "free", Currency: "EUR"}
	if got := Diff(baseSnapshot(), free); !hasLine(got, "25.00 EUR → free") {
		t.Errorf("free: %v", got)
	}
}

func TestDiff_SameLineFromSeveralDatesIsShownOnceWithACount(t *testing.T) {
	old, cur := Snapshot{Name: "Tour"}, Snapshot{Name: "Tour"}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("s%d", i)
		start := fmt.Sprintf("2027-02-0%dT19:00:00Z", i)
		old.Sessions = append(old.Sessions, session(id, start, tier("t"+id, "General", 2000)))
		cur.Sessions = append(cur.Sessions, session(id, start, tier("t"+id, "General", 2200)))
	}
	lines := Diff(old, cur)
	if len(lines) != 1 || !strings.Contains(lines[0], "Price «General»: 20.00 EUR → 22.00 EUR (× 4 dates)") {
		t.Errorf("want one counted line, got %v", lines)
	}
}

func TestDiff_IsCapped(t *testing.T) {
	old, cur := Snapshot{Name: "Tour"}, Snapshot{Name: "Tour"}
	for i := 1; i <= 15; i++ {
		id := fmt.Sprintf("s%d", i)
		old.Sessions = append(old.Sessions, session(id, fmt.Sprintf("2027-03-%02dT19:00:00Z", i)))
		cur.Sessions = append(cur.Sessions, session(id, fmt.Sprintf("2027-04-%02dT19:00:00Z", i)))
	}
	lines := Diff(old, cur)
	if len(lines) != maxChangeLines+1 || !strings.Contains(lines[len(lines)-1], "…and 5 more change(s)") {
		t.Errorf("want %d lines and a tail, got %d: %v", maxChangeLines, len(lines), lines[len(lines)-1])
	}
}

func TestDiff_NothingChangedMeansNoMessage(t *testing.T) {
	if got := Diff(baseSnapshot(), baseSnapshot()); len(got) != 0 {
		t.Errorf("want nothing, got %v", got)
	}
	e := Event{OrgName: "Acme", Snapshot: baseSnapshot()}
	if got := ChangedText(baseSnapshot(), e, "https://tickets.test"); got != "" {
		t.Errorf("no difference must give no text, got %q", got)
	}
}

func TestNewEventText_NamesWhatTheOperatorNeedsToJudgeIt(t *testing.T) {
	e := Event{
		OrgName: "Acme <Events>", Slug: "concert", PageSlug: "acme",
		Snapshot: baseSnapshot(),
	}
	text := NewEventText(e, "https://tickets.test/")
	for _, want := range []string{
		"🆕 <b>New event published</b> — Acme &lt;Events&gt;",
		"<b>Concert in Madrid</b>",
		"📅 Fri 15 Jan 2027, 20:00",
		"🎫 General 25.00 EUR · VIP 40.00 EUR",
		"🖼 poster: yes",
		"🔗 https://tickets.test/acme/concert",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	e.Snapshot.Poster = ""
	if !strings.Contains(NewEventText(e, ""), "🖼 poster: none") || strings.Contains(NewEventText(e, ""), "🔗") {
		t.Error("no poster and no base address: say 'none' and add no link")
	}
}

func TestNewEventText_ListsAtMostFiveDates(t *testing.T) {
	e := Event{OrgName: "Acme", Snapshot: Snapshot{Name: "Tour"}}
	for i := 1; i <= 8; i++ {
		e.Snapshot.Sessions = append(e.Snapshot.Sessions, session(fmt.Sprintf("s%d", i), fmt.Sprintf("2027-05-%02dT19:00:00Z", i)))
	}
	text := NewEventText(e, "")
	if strings.Count(text, "📅") != maxListedDates+1 || !strings.Contains(text, "…and 3 more date(s)") {
		t.Errorf("want %d dates and a tail:\n%s", maxListedDates, text)
	}
}

func TestChangedText_EscapesAndLinks(t *testing.T) {
	old := baseSnapshot()
	cur := baseSnapshot()
	cur.Name = "A & B <script>"
	e := Event{OrgName: "Acme", Slug: "concert", PageSlug: "acme", Snapshot: cur}
	text := ChangedText(old, e, "https://tickets.test")
	for _, want := range []string{"✏️ <b>Event changed</b> — Acme", "<b>A &amp; B &lt;script&gt;</b>", "• Name: «Concert in Madrid» → «A &amp; B &lt;script&gt;»", "🔗 https://tickets.test/acme/concert"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<script>") {
		t.Errorf("a name must never reach the message unescaped:\n%s", text)
	}
}

func TestLink(t *testing.T) {
	e := Event{PageSlug: "acme", Slug: "concert"}
	if got := Link("https://t.test/", e); got != "https://t.test/acme/concert" {
		t.Errorf("got %q", got)
	}
	for name, bad := range map[string]Event{"no page": {Slug: "concert"}, "no event slug": {PageSlug: "acme"}} {
		if Link("https://t.test", bad) != "" {
			t.Errorf("%s: want no link", name)
		}
	}
	if Link("", e) != "" {
		t.Error("no base address: want no link")
	}
}
