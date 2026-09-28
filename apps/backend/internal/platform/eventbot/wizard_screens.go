package eventbot

import (
	"context"
	"strconv"
	"strings"
)

// Render builds the screen of the draft's current step: the step header,
// the question with its hint, and the buttons — every choice a button, the
// remembered default first (spec 28 §5.0).
func (w *Wizard) Render(ctx context.Context, ws WizSession, d *Draft) (Screen, error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	btn := func(key, data string) Button { return Button{Label: t(key, nil), Data: data} }
	nav := func(rows ...[]Button) [][]Button {
		last := []Button{}
		if len(d.History) > 0 {
			last = append(last, btn("bot.wz.back_btn", "back"))
		}
		last = append(last, btn("bot.wz.cancel_btn", "cancel"))
		return append(rows, last)
	}
	header := func(n int, titleKey string) string {
		return t("bot.wz.step_title", map[string]any{"N": n, "Title": t(titleKey, nil)})
	}

	switch d.Step {
	case stEvName:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_name", nil), Buttons: nav()}, nil

	case stEvAge:
		rows := [][]Button{}
		if ws.Defaults.Age != "" {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.Age}), Data: "keep"}})
		}
		row := []Button{}
		for _, a := range AgeOptions {
			row = append(row, Button{Label: a, Data: "age:" + a})
		}
		rows = append(rows, row)
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_age", nil), Buttons: nav(rows...)}, nil

	case stEvPromoter:
		promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		rows := [][]Button{}
		if ws.Defaults.PromoterID != "" && ws.Defaults.PromoterName != "" {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.PromoterName}), Data: "keep"}})
		}
		rows = append(rows, []Button{btn("bot.wz.promoter_org", "prom:org")})
		for _, p := range sortedByName(promoters) {
			rows = append(rows, []Button{{Label: truncate(p.Name, 48), Data: "prom:" + p.ID}})
		}
		rows = append(rows, []Button{btn("bot.wz.promoter_new", "prom:new")})
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_promoter", nil), Buttons: nav(rows...)}, nil

	case stPromoterName:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_promoter_name", nil), Buttons: nav()}, nil

	case stPromoterLegal:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_promoter_legal", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil

	case stEvPoster:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_poster", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil

	case stSDate:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_date", map[string]any{"N": d.Cur + 1}), Buttons: nav()}, nil

	case stSTime:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_time", nil),
			Buttons: nav([]Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": "20:00"}), Data: "default"}})}, nil

	case stSSame:
		prev := d.Sessions[d.Cur-1]
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_same_venue", map[string]any{
			"N": d.Cur + 1, "Venue": Esc(prev.VenueName), "City": Esc(prev.CityName)}),
			Buttons: nav([]Button{btn("bot.wz.same_venue_btn", "same"), btn("bot.wz.other_venue_btn", "other")})}, nil

	case stSCountry:
		countries, err := w.refs.Countries(ctx, ws.JWT, loc)
		if err != nil {
			return Screen{}, err
		}
		venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		hasVenue := map[string]bool{}
		for _, v := range venues {
			hasVenue[strings.ToUpper(v.CountryISO2)] = true
		}
		rows := [][]Button{}
		if ws.Defaults.CountryID != "" {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.CountryName}), Data: "keep"}})
		}
		first, rest := []RefItem{}, []RefItem{}
		for _, c := range sortedByName(countries) {
			if hasVenue[strings.ToUpper(c.ISO2)] {
				first = append(first, c)
			} else {
				rest = append(rest, c)
			}
		}
		for _, c := range append(first, rest...) {
			rows = append(rows, []Button{{Label: c.Name, Data: "country:" + c.ID}})
		}
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_country", nil), Buttons: nav(chunk(rows, 2)...)}, nil

	case stSCity:
		s := d.session()
		cities, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, loc)
		if err != nil {
			return Screen{}, err
		}
		venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		withVenue := map[string]bool{}
		for _, v := range venues {
			withVenue[v.CityID] = true
		}
		rows := [][]Button{}
		if ws.Defaults.CityID != "" && ws.Defaults.CountryID == s.CountryID {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.CityName}), Data: "keep"}})
		}
		first, rest := []RefItem{}, []RefItem{}
		for _, c := range sortedByName(cities) {
			if withVenue[c.ID] {
				first = append(first, c)
			} else {
				rest = append(rest, c)
			}
		}
		if len(rest) > 24 {
			rest = rest[:24]
		}
		for _, c := range append(first, rest...) {
			rows = append(rows, []Button{{Label: c.Name, Data: "city:" + c.ID}})
		}
		rows = chunk(rows, 2)
		rows = append(rows, []Button{btn("bot.wz.city_other", "city:new")})
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_city", nil), Buttons: nav(rows...)}, nil

	case stCityName:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_city_name", nil), Buttons: nav()}, nil

	case stSVenue:
		s := d.session()
		venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		rows := [][]Button{}
		if ws.Defaults.VenueID != "" && ws.Defaults.CityID == s.CityID {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.VenueName}), Data: "keep"}})
		}
		for _, v := range venues {
			if v.CityID == s.CityID {
				rows = append(rows, []Button{{Label: truncate(v.Name, 48), Data: "venue:" + v.ID}})
			}
		}
		rows = append(rows, []Button{btn("bot.wz.venue_new", "venue:new")})
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue", nil), Buttons: nav(rows...)}, nil

	case stVName:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_name", nil), Buttons: nav()}, nil
	case stVAddress:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_address", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil
	case stVCapacity:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_capacity", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil
	case stVTz:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_tz", nil), Buttons: nav()}, nil

	case stSCapacity:
		s := d.session()
		rows := [][]Button{}
		if s.Capacity > 0 {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": s.Capacity}), Data: "keep"}})
		}
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_capacity", nil), Buttons: nav(rows...)}, nil

	case stSMore:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_more_sessions", nil),
			Buttons: nav([]Button{btn("bot.wz.more_session_btn", "more"), btn("bot.wz.sessions_done_btn", "next")})}, nil

	case stTMode:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_mode", nil),
			Buttons: nav([]Button{btn("bot.wz.t_mode_single", "mode:single")}, []Button{btn("bot.wz.t_mode_multi", "mode:multi")})}, nil

	case stTName:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_name", nil),
			Buttons: nav([]Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": t("bot.wz.t_name_default", nil)}), Data: "default"}})}, nil

	case stTPrice:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_price", nil), Buttons: nav()}, nil

	case stTChanges:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_changes", nil),
			Buttons: nav([]Button{btn("bot.wz.t_changes_no", "no"), btn("bot.wz.t_changes_yes", "yes")})}, nil

	case stTChangeDate:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_change_date", nil), Buttons: nav()}, nil

	case stTChangePrice:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_change_price", map[string]any{"Date": DisplayDate(d.Scratch.Step.From)}), Buttons: nav()}, nil

	case stTChangeMore:
		items := []string{}
		for _, s := range d.Tickets.Schedule {
			items = append(items, t("bot.wz.summary_schedule_item", map[string]any{"Date": DisplayDate(s.From), "Price": FormatMoney(s.PriceMinor, d.currencyOrGuess(), loc)}))
		}
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_change_more", map[string]any{"List": strings.Join(items, ", ")}),
			Buttons: nav([]Button{btn("bot.wz.t_change_more_btn", "more"), btn("bot.wz.t_change_done_btn", "done")})}, nil

	case stTKind:
		text := header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_kind", nil) +
			"\n\n• " + t("bot.wz.t_kind_parallel", nil) + " — " + t("bot.wz.t_kind_parallel_hint", nil) +
			"\n• " + t("bot.wz.t_kind_sequence", nil) + " — " + t("bot.wz.t_kind_sequence_hint", nil)
		return Screen{Text: text, Buttons: nav([]Button{btn("bot.wz.t_kind_parallel", "kind:parallel")}, []Button{btn("bot.wz.t_kind_sequence", "kind:sequence")})}, nil

	case stTCatName:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_name", map[string]any{"N": len(d.Tickets.Categories) + 1}), Buttons: nav()}, nil
	case stTCatPrice:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_price", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), Buttons: nav()}, nil
	case stTCatPlaces:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_places", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), Buttons: nav()}, nil
	case stTCatUntil:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_until", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}),
			Buttons: nav([]Button{btn("bot.wz.t_cat_until_skip", "skip")})}, nil
	case stTCatLimit:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_limit", nil), Buttons: nav([]Button{btn("bot.wz.t_cat_limit_skip", "skip")})}, nil

	case stTCatMore:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_more", map[string]any{"List": w.categoriesList(loc, d)}),
			Buttons: nav([]Button{btn("bot.wz.t_cat_more_btn", "more"), btn("bot.wz.t_cat_done_btn", "done")})}, nil

	case stXDescription:
		return Screen{Text: header(4, "bot.wz.title_extra") + t("bot.wz.ask_description", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil

	case stXCurrency:
		guess := d.currencyOrGuess()
		rows := [][]Button{}
		if guess != "" {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": guess}), Data: "cur:" + guess}})
		}
		common := []Button{}
		for _, c := range []string{"EUR", "CZK", "ILS", "HUF", "PLN", "USD"} {
			if c != guess {
				common = append(common, Button{Label: c, Data: "cur:" + c})
			}
		}
		rows = append(rows, common[:3], common[3:])
		return Screen{Text: header(4, "bot.wz.title_extra") + t("bot.wz.ask_currency", nil), Buttons: nav(rows...)}, nil

	case stXChannels:
		channels, err := w.refs.Channels(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		chosen := map[string]bool{}
		for _, c := range d.Channels {
			chosen[c.ID] = true
		}
		rows := [][]Button{}
		for _, c := range channels {
			mark := "☐ "
			if chosen[c.ID] {
				mark = "☑ "
			}
			rows = append(rows, []Button{{Label: mark + truncate(c.Name, 44), Data: "ch:" + c.ID}})
		}
		rows = append(rows, []Button{btn("bot.wz.channels_done_btn", "done")})
		return Screen{Text: header(4, "bot.wz.title_extra") + t("bot.wz.ask_channels", nil), Buttons: nav(rows...)}, nil

	case stXPublish:
		return Screen{Text: header(4, "bot.wz.title_extra") + t("bot.wz.ask_publish", nil),
			Buttons: nav([]Button{btn("bot.wz.publish_now_btn", "pub:now")}, []Button{btn("bot.wz.publish_later_btn", "pub:later")})}, nil

	case stSummary:
		text := header(5, "bot.wz.title_summary") + w.Summary(loc, d) + "\n\n" + t("bot.wz.summary_footer", nil)
		primary := btn("bot.wz.publish_btn", "publish")
		if !d.Publish {
			primary = btn("bot.wz.save_btn", "publish")
		}
		if d.Mode == ModeEdit {
			text = header(5, "bot.wz.title_summary") + w.Summary(loc, d) + "\n\n" + t("bot.wz.edit_footer", nil)
			primary = btn("bot.wz.save_changes_btn", "publish")
		}
		rows := [][]Button{
			{primary},
			{btn("bot.wz.edit_event_btn", "edit:event"), btn("bot.wz.edit_when_btn", "edit:when")},
			{btn("bot.wz.edit_tickets_btn", "edit:tickets"), btn("bot.wz.edit_extra_btn", "edit:extra")},
			{btn("bot.wz.cancel_btn", "cancel")},
		}
		return Screen{Text: text, Buttons: rows}, nil
	}
	return Screen{Text: d.Step, Buttons: nav()}, nil
}

// Summary is the one-paragraph recap of the draft (logic doc §2.6).
func (w *Wizard) Summary(loc string, d *Draft) string {
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	cur := d.currencyOrGuess()
	promoter, poster := "", ""
	if d.Event.PromoterName != "" {
		promoter = t("bot.wz.summary_promoter", map[string]any{"Name": Esc(d.Event.PromoterName)})
	}
	if d.Event.PosterMediaID == "" {
		poster = t("bot.wz.summary_no_poster", nil)
	}
	var b strings.Builder
	b.WriteString(t("bot.wz.summary_event", map[string]any{"Name": Esc(d.Event.Name), "Age": d.Event.Age, "Promoter": promoter, "Poster": poster}))
	b.WriteString("\n")
	sessions := []string{}
	for _, s := range d.Sessions {
		sessions = append(sessions, t("bot.wz.summary_session", map[string]any{
			"When": DisplayDate(s.Date) + " " + s.Time, "Venue": Esc(s.VenueName), "Capacity": s.Capacity,
		}))
	}
	b.WriteString(t("bot.wz.summary_sessions", map[string]any{"N": len(d.Sessions), "List": strings.Join(sessions, "; ")}))
	b.WriteString("\n")
	switch d.Tickets.Mode {
	case ModeSingle:
		if len(d.Tickets.Categories) > 0 {
			c := d.Tickets.Categories[0]
			sched := ""
			for _, s := range d.Tickets.Schedule {
				sched += ", " + t("bot.wz.summary_schedule_item", map[string]any{"Date": DisplayDate(s.From), "Price": FormatMoney(s.PriceMinor, cur, loc)})
			}
			b.WriteString(t("bot.wz.summary_single", map[string]any{"Name": Esc(c.Name), "Price": FormatMoney(c.PriceMinor, cur, loc), "Schedule": sched}))
		}
	case ModeParallel:
		b.WriteString(t("bot.wz.summary_parallel", map[string]any{"List": w.categoriesList(loc, d)}))
	case ModeSequence:
		b.WriteString(t("bot.wz.summary_sequence", map[string]any{"List": w.categoriesList(loc, d)}))
	}
	if len(d.Channels) > 1 {
		names := []string{}
		for _, c := range d.Channels {
			names = append(names, Esc(c.Name))
		}
		b.WriteString("\n" + t("bot.wz.summary_channels", map[string]any{"List": strings.Join(names, ", ")}))
	}
	b.WriteString("\n")
	if d.Publish {
		b.WriteString(t("bot.wz.summary_publish_now", nil))
	} else {
		b.WriteString(t("bot.wz.summary_publish_later", nil))
	}
	return b.String()
}

// categoriesList renders "VIP 500 ILS × 20, Standard 250 ILS × 60" or
// "Start 99 ILS 3 tickets or until 30.09 → Regular 199 ILS → Door 249 ILS".
func (w *Wizard) categoriesList(loc string, d *Draft) string {
	cur := d.currencyOrGuess()
	parts := []string{}
	for _, c := range d.Tickets.Categories {
		p := Esc(c.Name) + " " + FormatMoney(c.PriceMinor, cur, loc)
		switch d.Tickets.Mode {
		case ModeParallel:
			p += " × " + itoa(c.Places)
		case ModeSequence:
			ends := []string{}
			if c.SellLimit > 0 {
				ends = append(ends, itoa(c.SellLimit)+" ×")
			}
			if c.SellUntil != "" {
				ends = append(ends, "→ "+DisplayDate(c.SellUntil))
			}
			if len(ends) > 0 {
				p += " (" + strings.Join(ends, " / ") + ")"
			}
		}
		parts = append(parts, p)
	}
	if d.Tickets.Mode == ModeSequence {
		return strings.Join(parts, " → ")
	}
	return strings.Join(parts, ", ")
}

// currencyOrGuess is the chosen currency, else the first date's country
// currency, else "".
func (d *Draft) currencyOrGuess() string {
	if d.Currency != "" {
		return d.Currency
	}
	for _, s := range d.Sessions {
		if s.Currency != "" {
			return strings.ToUpper(s.Currency)
		}
	}
	return ""
}

func chunk(rows [][]Button, per int) [][]Button {
	out := [][]Button{}
	var cur []Button
	for _, r := range rows {
		if len(r) != 1 {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			out = append(out, r)
			continue
		}
		cur = append(cur, r[0])
		if len(cur) == per {
			out = append(out, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
