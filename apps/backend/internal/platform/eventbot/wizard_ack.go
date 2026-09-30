package eventbot

import (
	"context"
	"strings"
)

// Apply advances the draft by one answer (see apply) and, when the answer
// was accepted silently, prefixes the next question with a one-line
// confirmation of what was just chosen — "✔ Name: Wine tasting" — so the
// person always sees what the bot understood before answering the next
// question. A step that already explains itself (an error, a poster
// accepted, a date added) keeps its own note; "back" and "cancel" confirm
// nothing.
func (w *Wizard) Apply(ctx context.Context, ws WizSession, d *Draft, in WizInput) (string, error) {
	before := d.Step
	note, err := w.apply(ctx, ws, d, in)
	if err != nil || note != "" || d.Step == before {
		return note, err
	}
	switch strings.TrimSpace(in.Data) {
	case "back", "cancel", "cancel:no", "cancel:keep", "e:home":
		return note, nil
	}
	return w.ack(ws, before, d), nil
}

// ack renders the confirmation line for the step just answered, or "" when
// the value is not final yet (a new promoter, city or venue being typed in).
func (w *Wizard) ack(ws WizSession, step string, d *Draft) string {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	line := func(labelKey, value string) string {
		if value == "" {
			return ""
		}
		return t("bot.wz.ack", map[string]any{"Label": t(labelKey, nil), "Value": value})
	}
	cur := d.currencyOrGuess()
	s := d.session()
	lastCat := func() *DraftCategory {
		if len(d.Tickets.Categories) == 0 {
			return nil
		}
		return &d.Tickets.Categories[len(d.Tickets.Categories)-1]
	}
	switch step {
	case stEvName:
		return line("bot.wz.ack_name", Esc(d.Event.Name))
	case stEvAge:
		return line("bot.wz.ack_age", d.Event.Age)
	case stEvPromoter, stPromoterLegal:
		if d.Step == stPromoterName || d.Step == stPromoterLegal {
			return ""
		}
		if d.Event.PromoterID == "" && d.Event.PromoterName == "" {
			// The organization itself, by its own name — "your organization"
			// meant nothing to the first organizers who saw it (2026-09-29).
			if ws.OrgName != "" {
				return line("bot.wz.ack_promoter", Esc(ws.OrgName))
			}
			return line("bot.wz.ack_promoter", t("bot.wz.ack_promoter_org", nil))
		}
		return line("bot.wz.ack_promoter", Esc(d.Event.PromoterName))
	case stEvPoster:
		if d.Event.PosterMediaID == "" {
			return line("bot.wz.ack_poster", t("bot.wz.ack_poster_none", nil))
		}
		return ""
	case stSDate:
		if s == nil {
			return ""
		}
		return line("bot.wz.ack_date", DisplayDate(s.Date))
	case stSTime:
		if s == nil {
			return ""
		}
		return line("bot.wz.ack_time", s.Time)
	case stSCountry:
		if s == nil {
			return ""
		}
		return line("bot.wz.ack_country", Esc(s.CountryName))
	case stSCity, stCityName:
		if s == nil || d.Step == stCityName {
			return ""
		}
		return line("bot.wz.ack_city", Esc(s.CityName))
	case stSVenue, stVName, stVAddress, stVCapacity, stVTz:
		switch d.Step {
		case stVName, stVAddress, stVCapacity, stVTz:
			return ""
		}
		if s == nil {
			return ""
		}
		// A venue arena already knows the size of says so at once, so the
		// "keep 220" button on the next question is not a mystery number.
		if s.Capacity > 0 {
			return line("bot.wz.ack_venue", t("bot.wz.ack_venue_cap", map[string]any{"Venue": Esc(s.VenueName), "Capacity": s.Capacity}))
		}
		return line("bot.wz.ack_venue", Esc(s.VenueName))
	case stSCapacity:
		if s == nil || s.Capacity <= 0 {
			return ""
		}
		return line("bot.wz.ack_capacity", itoa(s.Capacity))
	case stTName:
		if c := lastCat(); c != nil {
			return line("bot.wz.ack_category", Esc(c.Name))
		}
	case stTPrice:
		if c := lastCat(); c != nil {
			return line("bot.wz.ack_price", FormatMoney(c.PriceMinor, cur, loc))
		}
	case stTChangePrice:
		if n := len(d.Tickets.Schedule); n > 0 {
			st := d.Tickets.Schedule[n-1]
			return line("bot.wz.ack_step", t("bot.wz.summary_schedule_item", map[string]any{"Date": DisplayDate(st.From), "Price": FormatMoney(st.PriceMinor, cur, loc)}))
		}
	case stTCatName:
		return line("bot.wz.ack_category", Esc(d.Scratch.Cat.Name))
	case stTCatPrice:
		return line("bot.wz.ack_price", FormatMoney(d.Scratch.Cat.PriceMinor, cur, loc))
	case stTCatPlaces:
		if c := lastCat(); c != nil && c.Places > 0 {
			return line("bot.wz.ack_places", itoa(c.Places))
		}
	case stTCatLast:
		if d.Step == stTCatUntil {
			return ""
		}
		if c := lastCat(); c != nil {
			return line("bot.wz.ack_category", t("bot.wz.ack_category_last", map[string]any{"Name": Esc(c.Name)}))
		}
	case stTCatUntil:
		c := lastCat()
		if c == nil {
			return ""
		}
		until := ""
		if c.SellUntil != "" {
			until = DisplayDate(c.SellUntil)
		}
		limit := ""
		if c.SellLimit > 0 {
			limit = itoa(c.SellLimit)
		}
		return t("bot.wz.ack_end", map[string]any{"Name": Esc(c.Name), "Until": until, "Limit": limit})
	case stXDescription:
		if strings.TrimSpace(d.Event.Description) == "" {
			return line("bot.wz.ack_description", t("bot.wz.ack_none", nil))
		}
		return line("bot.wz.ack_description", t("bot.wz.ack_saved", nil))
	case stXCurrency:
		return line("bot.wz.ack_currency", d.Currency)
	case stXChannels:
		names := make([]string, 0, len(d.Channels))
		for _, c := range d.Channels {
			names = append(names, Esc(c.Name))
		}
		return line("bot.wz.ack_channels", strings.Join(names, ", "))
	}
	return ""
}
