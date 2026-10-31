package eventbot

// wizard_seated.go — a date on a seating plan (task S3c of spec 40, spec 26
// §4.1).
//
// A date at a venue that has a seating plan is sold by the plan: its places
// are the plan's seats, its categories are the plan's categories and the
// import binds them (event-bundle source=arena, arenaSeatingPlanVersionId,
// task S1b). The wizard asks two things for such a date: which plan (or none:
// free seating) and the price of each plan category. It never touches the
// places: changing the seat layout is the job of the "Seat layout" screens.
//
// A plan may be drawn with fewer categories than the organizer sells (one
// category: every seat one colour). After the plan's prices a new date may
// get extra price categories — name, price, colour — that start with no
// places; the organizer paints plan seats into them after the save (the
// seat map, the "Seat layout" dialog). The bundle sends them as planExtra.
//
// An edited event keeps its seated dates as they are. "Prices and categories"
// on such a date changes only the prices: the categories and the places belong
// to the plan, so the question is the price of each category, nothing more.

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// PlanCategory is one category of a seating plan that has places.
type PlanCategory struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Places int    `json:"places"`
	// GA marks a standing zone of the plan; its places are the zone's capacity.
	GA bool `json:"ga,omitempty"`
}

// PlanRef is the current version of one seating plan of a venue.
type PlanRef struct {
	VersionID  string         `json:"version_id"`
	Name       string         `json:"name"`
	Seats      int            `json:"seats"` // every place of the plan: seats and standing capacity
	Categories []PlanCategory `json:"categories"`
}

// PlanIO lists the seating plans of a venue. It is optional: a wizard whose
// references cannot answer it treats every venue as having no plan.
type PlanIO interface {
	VenuePlans(ctx context.Context, jwt string, orgID uuid.UUID, venueID string) ([]PlanRef, error)
}

// seated reports whether the date is sold by a seating plan.
func (s DraftSession) seated() bool { return s.PlanVersionID != "" }

// anySeated reports whether any date of the draft is sold by a plan.
func (d *Draft) anySeated() bool {
	for _, s := range d.Sessions {
		if s.seated() {
			return true
		}
	}
	return false
}

// allDatesOwnTickets reports whether every date carries categories of its
// own (a draft of seated dates only), so the shared ticket questions have
// nothing to ask.
func (d *Draft) allDatesOwnTickets() bool {
	if len(d.Sessions) == 0 {
		return false
	}
	for _, s := range d.Sessions {
		if s.Tickets == nil || len(s.Tickets.Categories) == 0 {
			return false
		}
	}
	return true
}

// venuePlans reads the plans of a venue; an unreadable list is no plans, so a
// failing lookup never blocks a general-admission event.
func (w *Wizard) venuePlans(ctx context.Context, ws WizSession, venueID string) []PlanRef {
	pio, ok := w.refs.(PlanIO)
	if !ok || venueID == "" {
		return nil
	}
	plans, err := pio.VenuePlans(ctx, ws.JWT, ws.OrgID, venueID)
	if err != nil {
		return nil
	}
	return plans
}

// seatedTickets are the categories of a plan, still unpriced.
func seatedTickets(p PlanRef) *DraftTickets {
	t := &DraftTickets{Mode: ModeParallel}
	for _, c := range p.Categories {
		t.Categories = append(t.Categories, DraftCategory{Name: c.Name, Places: c.Places, PlanIndex: c.Index, GA: c.GA})
	}
	return t
}

// offerPlans moves the date to the plan question when its venue has plans.
// It reports whether it did.
func (w *Wizard) offerPlans(ctx context.Context, ws WizSession, d *Draft) bool {
	s := d.session()
	plans := w.venuePlans(ctx, ws, s.VenueID)
	if len(plans) == 0 {
		return false
	}
	d.Scratch.Plans = plans
	d.goTo(stSPlan)
	return true
}

// startSeatPrices opens the price questions of a seated date n (1-based) of an
// edited event, one category after another.
func (d *Draft) startSeatPrices(n int) {
	work := cloneTickets(*d.ticketsOf(n - 1))
	d.Scratch.SeatWork = &work
	d.Scratch.SeatFor, d.Scratch.SeatIdx = n, 0
	d.goTo(stSPlanPrice)
}

// applySeated handles the answers of the two seated steps.
func (w *Wizard) applySeated(loc string, d *Draft, text, data string) (string, error) {
	t := func(key string, vars map[string]any) string { return w.texts.T(loc, key, vars) }
	switch d.Step {
	case stSPlan:
		s := d.session()
		if data == "plan:none" {
			if s.seated() {
				s.PlanVersionID, s.PlanName, s.Tickets = "", "", nil
			}
			d.goTo(stSCapacity)
			return "", nil
		}
		raw, ok := strings.CutPrefix(data, "plan:")
		if !ok {
			return "", nil
		}
		i, err := strconv.Atoi(raw)
		if err != nil || i < 0 || i >= len(d.Scratch.Plans) {
			return "", nil
		}
		p := d.Scratch.Plans[i]
		s.PlanVersionID, s.PlanName, s.Capacity = p.VersionID, p.Name, p.Seats
		d.Scratch.SeatWork = seatedTickets(p)
		d.Scratch.SeatFor, d.Scratch.SeatIdx = 0, 0
		d.goTo(stSPlanPrice)
		return "", nil

	case stSPlanPrice:
		work := d.Scratch.SeatWork
		if work == nil || d.Scratch.SeatIdx < 0 || d.Scratch.SeatIdx >= len(work.Categories) {
			return "", nil
		}
		c := &work.Categories[d.Scratch.SeatIdx]
		if data == "keep" && d.Scratch.SeatFor > 0 {
			// the stored price stays
		} else {
			p, ok := ParsePriceMinor(text)
			if !ok {
				return t("bot.wz.err_price", nil), nil
			}
			c.PriceMinor = p
		}
		d.Scratch.SeatIdx++
		if d.Scratch.SeatIdx < len(work.Categories) {
			return "", nil
		}
		done := *work
		forDate := d.Scratch.SeatFor
		d.Scratch.SeatWork, d.Scratch.SeatFor, d.Scratch.SeatIdx = nil, 0, 0
		if forDate > 0 {
			// A seated date of an edited event: the prices change, the places
			// stay what the plan makes them.
			i := forDate - 1
			capacity := d.Sessions[i].Capacity
			d.setDateTickets(i, cloneTickets(done))
			d.Sessions[i].Capacity = capacity
			d.nextSectionAfterSeatPrices()
			return "", nil
		}
		// A new date: its plan prices are set, extra categories may follow.
		d.Scratch.SeatWork = &done
		d.goTo(stSPlanExtra)
		return "", nil

	case stSPlanExtra:
		work := d.Scratch.SeatWork
		if work == nil {
			return "", nil
		}
		switch data {
		case "extra:add":
			if len(work.Categories) >= maxSeatCategories {
				return "", nil
			}
			d.Scratch.SeatExtra = &DraftCategory{Extra: true}
			d.goTo(stSPlanExtraName)
			return "", nil
		case "extra:done":
			return w.finishSeatedDate(loc, d)
		}
		return "", nil

	case stSPlanExtraName:
		work, ex := d.Scratch.SeatWork, d.Scratch.SeatExtra
		if work == nil || ex == nil {
			return "", nil
		}
		name := strings.TrimSpace(text)
		if name == "" {
			return t("bot.wz.err_plan_extra_name", nil), nil
		}
		for _, c := range work.Categories {
			if strings.EqualFold(strings.TrimSpace(c.Name), name) {
				return t("bot.wz.err_plan_extra_dup", map[string]any{"Name": Esc(name)}), nil
			}
		}
		ex.Name = truncate(name, 60)
		d.goTo(stSPlanExtraPrice)
		return "", nil

	case stSPlanExtraPrice:
		ex := d.Scratch.SeatExtra
		if ex == nil {
			return "", nil
		}
		p, ok := ParsePriceMinor(text)
		if !ok {
			return t("bot.wz.err_price", nil), nil
		}
		ex.PriceMinor = p
		d.goTo(stSPlanExtraColor)
		return "", nil

	case stSPlanExtraColor:
		work, ex := d.Scratch.SeatWork, d.Scratch.SeatExtra
		if work == nil || ex == nil {
			return "", nil
		}
		hex, ok := strings.CutPrefix(data, "color:")
		if !ok || colorEmoji(hex) == "" {
			return "", nil
		}
		ex.Color = hex
		work.Categories = append(work.Categories, *ex)
		d.Scratch.SeatExtra = nil
		// Back to the list as it was: "back" from there must not land on the
		// name, price and colour of a category that is already added.
		d.returnTo(stSPlanExtra)
		return "", nil
	}
	return "", nil
}

// returnTo goes back to the last visit of step, dropping the steps after it
// from the history; without one it is goTo.
func (d *Draft) returnTo(step string) {
	for i := len(d.History) - 1; i >= 0; i-- {
		if d.History[i] == step {
			d.History = d.History[:i]
			d.Step = step
			d.clearDateDialog()
			return
		}
	}
	d.goTo(step)
}

// maxSeatCategories is the most categories a seated date may have: the
// seat layouts cut a hall into at most fifteen (the seat map's palette).
const maxSeatCategories = 15

// finishSeatedDate ends the price questions of a new seated date: the plan's
// categories and the extra ones become the date's own categories.
func (w *Wizard) finishSeatedDate(loc string, d *Draft) (string, error) {
	work := d.Scratch.SeatWork
	if work == nil {
		return "", nil
	}
	done := cloneTickets(*work)
	d.Scratch.SeatWork, d.Scratch.SeatExtra, d.Scratch.SeatFor, d.Scratch.SeatIdx = nil, nil, 0, 0
	s := d.session()
	s.Tickets = &done
	extra := 0
	for _, c := range done.Categories {
		if c.Extra {
			extra++
		}
	}
	vars := map[string]any{"Plan": Esc(s.PlanName), "Seats": s.Capacity, "Count": len(done.Categories), "Extra": extra}
	note := w.texts.T(loc, "bot.wz.plan_done_note", vars)
	if extra > 0 {
		note = w.texts.T(loc, "bot.wz.plan_done_extra_note", vars)
	}
	more, err := w.finishSession(loc, d)
	if err != nil {
		return "", err
	}
	return note + "\n" + more, nil
}

// seatColors are the colours an extra category of a seated date can take on
// the seat map, as the square emoji the colour buttons show.
var seatColors = []struct{ Hex, Emoji string }{
	{"#dc2626", "🟥"}, {"#ea580c", "🟧"}, {"#ca8a04", "🟨"}, {"#16a34a", "🟩"},
	{"#2563eb", "🟦"}, {"#7c3aed", "🟪"}, {"#92400e", "🟫"}, {"#374151", "⬛"},
}

// colorEmoji is the square of a seat-map colour; "" for any other colour.
func colorEmoji(hex string) string {
	for _, c := range seatColors {
		if c.Hex == hex {
			return c.Emoji
		}
	}
	return ""
}

// nextSectionAfterSeatPrices ends the price questions of a seated date.
func (d *Draft) nextSectionAfterSeatPrices() {
	if d.Scratch.Edit == "dates" {
		d.goTo(stSConfirm) // the new date's summary, as after any change of its categories
		return
	}
	d.section(stXDescription)
}

// renderSeated draws the two seated steps.
func (w *Wizard) renderSeated(loc string, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) Screen {
	t := func(key string, vars map[string]any) string { return w.texts.T(loc, key, vars) }
	switch d.Step {
	case stSPlan:
		s := d.session()
		rows := [][]Button{}
		for i, p := range d.Scratch.Plans {
			rows = append(rows, []Button{{
				Label: truncate(t("bot.wz.plan_btn", map[string]any{"Name": p.Name, "Seats": p.Seats}), 56),
				Data:  "plan:" + strconv.Itoa(i),
			}})
		}
		rows = append(rows, []Button{{Label: t("bot.wz.plan_none_btn", nil), Data: "plan:none"}})
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_plan", map[string]any{"Venue": Esc(s.VenueName)}), Buttons: nav(rows...)}
	case stSPlanPrice:
		work := d.Scratch.SeatWork
		if work == nil || d.Scratch.SeatIdx < 0 || d.Scratch.SeatIdx >= len(work.Categories) {
			return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.t_nothing_yet", nil), Buttons: nav()}
		}
		c := work.Categories[d.Scratch.SeatIdx]
		cur := d.currencyOrGuess()
		rows := [][]Button{}
		if d.Scratch.SeatFor > 0 {
			rows = append(rows, []Button{{
				Label: t("bot.wz.keep_btn", map[string]any{"Value": FormatMoney(c.PriceMinor, cur, loc)}),
				Data:  "keep",
			}})
		}
		return Screen{
			Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_plan_price", map[string]any{
				"Name": Esc(c.Name), "N": d.Scratch.SeatIdx + 1, "Total": len(work.Categories), "Places": c.Places, "Currency": cur,
			}),
			Buttons: nav(rows...),
		}

	case stSPlanExtra:
		work := d.Scratch.SeatWork
		if work == nil {
			return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.t_nothing_yet", nil), Buttons: nav()}
		}
		cur := d.currencyOrGuess()
		text := t("bot.wz.ask_plan_extra", map[string]any{"List": w.categoriesList(loc, cur, work)})
		planCats := 0
		for _, c := range work.Categories {
			if !c.Extra {
				planCats++
			}
		}
		if planCats == 1 && len(work.Categories) == 1 {
			text = t("bot.wz.plan_one_category_hint", nil) + "\n\n" + text
		}
		rows := [][]Button{}
		if len(work.Categories) < maxSeatCategories {
			rows = append(rows, []Button{{Label: t("bot.wz.plan_extra_add_btn", nil), Data: "extra:add"}})
		}
		rows = append(rows, []Button{{Label: t("bot.wz.plan_extra_done_btn", nil), Data: "extra:done"}})
		return Screen{Text: header(3, "bot.wz.title_tickets") + text, Buttons: nav(rows...)}

	case stSPlanExtraName:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_plan_extra_name", nil), Buttons: nav()}

	case stSPlanExtraPrice, stSPlanExtraColor:
		ex := d.Scratch.SeatExtra
		if ex == nil {
			return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.t_nothing_yet", nil), Buttons: nav()}
		}
		if d.Step == stSPlanExtraPrice {
			return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_plan_extra_price", map[string]any{
				"Name": Esc(ex.Name), "Currency": d.currencyOrGuess(),
			}), Buttons: nav()}
		}
		line := []Button{}
		rows := [][]Button{}
		for _, c := range seatColors {
			line = append(line, Button{Label: c.Emoji, Data: "color:" + c.Hex})
			if len(line) == 4 {
				rows, line = append(rows, line), []Button{}
			}
		}
		if len(line) > 0 {
			rows = append(rows, line)
		}
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_plan_extra_color", map[string]any{"Name": Esc(ex.Name)}), Buttons: nav(rows...)}
	}
	return Screen{Text: d.Step, Buttons: nav()}
}
