package eventbot

// wizard_date_tickets.go — categories per date (U-5, 2026-10-15).
//
// An event created in the bot sells one set of categories on every date
// (Draft.Tickets). An event created elsewhere, one date per import, may sell
// different categories on each date — a children's show with "4–6 years" on
// one day and "from 6 years" on the next. Editing such an event used to load
// the first date's categories and send them, with the first date's tier ids,
// for every date: the import refused the second date (422
// import.invalid_category), and a renamed category closed the old one on
// every date. Now every date of an edited event carries its own categories
// (DraftSession.Tickets); the ticket questions fill a working copy
// (Scratch.Work) for one date or for every date, and the copy is written to
// the date(s) only when the last question is answered.

import "strings"

// tk is the ticket part the ticket questions are filling: the working copy
// while an edited event changes the categories of one date (or of every
// date), else the draft's own.
func (d *Draft) tk() *DraftTickets {
	if d.Scratch.Work != nil {
		return d.Scratch.Work
	}
	return &d.Tickets
}

// ticketsOf is what date i sells: its own categories when it has them (every
// date of an edited event), else the draft's (a new event: one set for all
// dates).
func (d *Draft) ticketsOf(i int) *DraftTickets {
	if i >= 0 && i < len(d.Sessions) && d.Sessions[i].Tickets != nil {
		return d.Sessions[i].Tickets
	}
	return &d.Tickets
}

// cloneTickets copies a ticket part so that two dates never share a slice.
func cloneTickets(t DraftTickets) DraftTickets {
	t.Categories = append([]DraftCategory(nil), t.Categories...)
	t.Schedule = append([]DraftPriceStep(nil), t.Schedule...)
	return t
}

// withoutTierIDs is a copy whose categories are new ones: a date the draft
// adds can never address a category of another date.
func withoutTierIDs(t DraftTickets) DraftTickets {
	t = cloneTickets(t)
	for i := range t.Categories {
		t.Categories[i].TierID = ""
	}
	return t
}

// openTicketsEdit is "Prices and categories" on the card of an edited event:
// with several dates the person first says which date (or every date), with
// one date the questions start at once. "One or several" and "all at once or
// in turn" are fixed once the event exists; the categories are re-entered.
func (d *Draft) openTicketsEdit() {
	d.startEdit("tickets", stTDate)
	d.Scratch.ReturnToSummary = true
	if len(d.Sessions) > 1 {
		return
	}
	if d.Sessions[0].seated() {
		d.startSeatPrices(1)
		return
	}
	d.Step = d.startTickets(1, *d.ticketsOf(0), true)
}

// startTickets opens the ticket questions for date n (1-based), or for every
// date when n is 0, starting from src (the mode stays src's). carry gives a
// re-entered category the tier id of its namesake in src (an existing date);
// a new date starts with no ids. It returns the first question.
func (d *Draft) startTickets(n int, src DraftTickets, carry bool) string {
	src = cloneTickets(src)
	d.Scratch.PrevCats, d.Scratch.PrevSchedule = nil, nil
	if carry {
		d.Scratch.PrevCats, d.Scratch.PrevSchedule = src.Categories, src.Schedule
	}
	d.Scratch.Work = &DraftTickets{Mode: src.Mode}
	d.Scratch.WorkFor, d.Scratch.WorkAll = n, n == 0
	d.Scratch.Cat = DraftCategory{}
	if src.Mode == ModeSingle {
		return stTName
	}
	return stTCatName
}

// ticketsDone ends the ticket questions: an edited event writes the working
// copy to its date(s) and shows the new date's summary (a new date) or the
// card; a new event walks on to the next section.
func (d *Draft) ticketsDone() {
	if d.Scratch.Work != nil {
		d.commitTickets()
		if d.Scratch.Edit == "dates" {
			d.goTo(stSConfirm)
			return
		}
	}
	d.section(stXDescription)
}

// commitTickets writes the working copy to the date it was entered for, or
// to every date. The copy stays in Scratch so "back" can re-enter it.
func (d *Draft) commitTickets() {
	work := cloneTickets(*d.Scratch.Work)
	if d.Scratch.WorkAll {
		for i := range d.Sessions {
			t := cloneTickets(work)
			carryOwnTierIDs(&t, d.Sessions[i].Tickets)
			d.setDateTickets(i, t)
		}
		d.Tickets = cloneTickets(*d.ticketsOf(0))
		return
	}
	i := d.Scratch.WorkFor - 1
	if i < 0 || i >= len(d.Sessions) {
		d.Tickets = work
		return
	}
	d.setDateTickets(i, work)
	if i == 0 {
		// Draft.Tickets mirrors the first date: it is what a copy of the
		// event ("Repeat as new") starts from.
		d.Tickets = cloneTickets(work)
	}
}

// setDateTickets gives date i its categories and marks it for the save. A
// date the draft adds never carries a tier id; a date sold "at once" has as
// many places as its categories together.
func (d *Draft) setDateTickets(i int, t DraftTickets) {
	s := &d.Sessions[i]
	if s.SessionID == "" {
		t = withoutTierIDs(t)
	}
	s.Tickets = &t
	s.Dirty = true
	s.CopiedFrom = ""
	if i < len(d.Saved.Sessions) {
		// A date changed after a save that stopped half way is sent again,
		// with what it has now.
		d.Saved.Sessions[i].Done = false
	}
	if t.Mode == ModeParallel {
		if n := placesOf(t); n > 0 {
			s.Capacity = n
		}
	}
}

// carryOwnTierIDs gives each category of t the tier id of the category of
// the same name in own (a date's categories before the change); the single
// category keeps its id whatever it is now called.
func carryOwnTierIDs(t *DraftTickets, own *DraftTickets) {
	for i := range t.Categories {
		t.Categories[i].TierID = ""
	}
	if own == nil {
		return
	}
	if t.Mode == ModeSingle && len(t.Categories) == 1 && len(own.Categories) == 1 {
		t.Categories[0].TierID = own.Categories[0].TierID
		return
	}
	for i := range t.Categories {
		for _, p := range own.Categories {
			if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(t.Categories[i].Name)) {
				t.Categories[i].TierID = p.TierID
				break
			}
		}
	}
}

// placesOf is the places of a date sold "at once": the sum of its categories.
func placesOf(t DraftTickets) int {
	n := 0
	for _, c := range t.Categories {
		n += c.Places
	}
	return n
}

// afterSessionStep is the question after a date's place and capacity: a date
// added to an edited event first asks about its categories (or, when it has
// them already and only its places changed, shows its summary again);
// otherwise "one more date?".
func (d *Draft) afterSessionStep() string {
	if d.Mode == ModeEdit && d.Cur > 0 && d.Cur < len(d.Sessions) && d.Sessions[d.Cur].SessionID == "" {
		if d.Sessions[d.Cur].Tickets != nil {
			return stSConfirm
		}
		return stSTickets
	}
	return stSMore
}

// fitTicketsToDate makes copied categories fit a date: a price change that
// starts after the date is dropped, and a stage that would sell past the date
// ends on it (the same last day the "own categories" questions allow). Stages
// sold one after another are then re-linked (fitStagesToDate), so the chain
// the import gets is one it accepts. It returns how many were changed.
func fitTicketsToDate(t *DraftTickets, date string) int {
	if date == "" {
		return 0
	}
	n := 0
	kept := t.Schedule[:0]
	for _, s := range t.Schedule {
		if s.From > date {
			n++
			continue
		}
		kept = append(kept, s)
	}
	t.Schedule = kept
	if t.Mode == ModeSequence {
		return n + fitStagesToDate(t, date)
	}
	for i := range t.Categories {
		if c := &t.Categories[i]; c.SellUntil != "" && c.SellUntil > date {
			c.SellUntil = date
			n++
		}
	}
	return n
}

// fitStagesToDate fits stages sold one after another to a date. Moving
// every late end onto the date alone gives a chain the import refuses (422
// import.invalid_sale_window): a stage that opens when the one before it
// ends (a date-only step) and ends that same day has a window that ends
// where it starts. So:
//   - a stage that would end no later than the date-only stage before it
//     never sells and is removed; its count limit goes to the next stage
//     with a limit;
//   - the first stage without a count limit that sells up to the date itself
//     becomes the last one (no end): every stage after it would open only
//     after the date, and is removed;
//   - a single stage left is sold as one category (ModeSingle).
//
// It returns how many stages were changed or removed.
func fitStagesToDate(t *DraftTickets, date string) int {
	cats := t.Categories
	n := 0
	var kept []DraftCategory
	carry := 0
	for i, c := range cats {
		last := i == len(cats)-1
		changed := false
		if !last && c.SellUntil > date {
			c.SellUntil = date
			changed = true
		}
		if !last && len(kept) > 0 {
			prev := kept[len(kept)-1]
			if prev.SellLimit == 0 && prev.SellUntil != "" && c.SellUntil != "" && c.SellUntil <= prev.SellUntil {
				carry += c.SellLimit
				n++
				continue
			}
		}
		if carry > 0 && !last && c.SellLimit > 0 {
			c.SellLimit += carry
			carry = 0
			changed = true
		}
		if !last && c.SellLimit == 0 && c.SellUntil != "" && c.SellUntil >= date {
			c.SellUntil = ""
			kept = append(kept, c)
			n += len(cats) - i // this stage and every one after it
			break
		}
		if changed {
			n++
		}
		kept = append(kept, c)
	}
	if len(kept) == 1 {
		kept[0].SellUntil, kept[0].SellLimit = "", 0
		t.Mode = ModeSingle
	}
	t.Categories = kept
	return n
}

// hasCategories reports whether a ticket part sells anything.
func hasCategories(t DraftTickets) bool { return len(t.Categories) > 0 }

// sameTicketsShape reports whether two dates sell the same categories (by
// name, price, places and ends — tier ids aside), which is when the card
// shows them once instead of date by date.
func sameTicketsShape(a, b DraftTickets) bool {
	if a.Mode != b.Mode || len(a.Categories) != len(b.Categories) || len(a.Schedule) != len(b.Schedule) {
		return false
	}
	for i := range a.Categories {
		x, y := a.Categories[i], b.Categories[i]
		x.TierID, y.TierID = "", ""
		x.Name, y.Name = strings.ToLower(strings.TrimSpace(x.Name)), strings.ToLower(strings.TrimSpace(y.Name))
		if x != y {
			return false
		}
	}
	for i := range a.Schedule {
		if a.Schedule[i] != b.Schedule[i] {
			return false
		}
	}
	return true
}

// ticketsDiffer reports whether the dates of the draft sell different
// categories.
func (d *Draft) ticketsDiffer() bool {
	for i := 1; i < len(d.Sessions); i++ {
		if !sameTicketsShape(*d.ticketsOf(0), *d.ticketsOf(i)) {
			return true
		}
	}
	return false
}

// sendPlan says which dates the save posts. A new event posts every date. An
// edited event posts a date it adds and a date whose categories changed; a
// changed currency touches every date that sells something (it is stored per
// date — a date without categories is left alone, a bundle with none would
// describe nothing); any other change of the event itself (name, poster, …)
// rides on the first date. A date nobody touched is not posted again: a
// re-import would close any of its categories the bundle cannot describe and
// bump its seat version for nothing.
func (d *Draft) sendPlan() []bool {
	out := d.plannedDates()
	if d.Mode != ModeEdit {
		return out
	}
	for _, p := range out {
		if p {
			return out
		}
	}
	if len(out) > 0 {
		out[0] = true
	}
	return out
}

// plannedDates is sendPlan without its fallback: the dates that carry a
// change of their own (an added date, new categories, a new currency).
func (d *Draft) plannedDates() []bool {
	out := make([]bool, len(d.Sessions))
	if d.Mode != ModeEdit {
		for i := range out {
			out[i] = true
		}
		return out
	}
	currency := contains(d.Changed, "currency")
	for i, s := range d.Sessions {
		if s.SessionID == "" || s.Dirty || (currency && hasCategories(*d.ticketsOf(i))) {
			out[i] = true
		}
	}
	return out
}

// dateLabel is how a date is named on a button or in a line: "18.12.2026
// 18:00".
func dateLabel(s DraftSession) string {
	return DisplayDate(s.Date) + " " + s.Time
}
