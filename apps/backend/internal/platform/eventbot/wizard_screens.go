package eventbot

import (
	"context"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// Render builds the screen of the draft's current step: the step header,
// the question with its hint, and the buttons — every choice a button, the
// remembered default first (spec 28 §5.0).
func (w *Wizard) Render(ctx context.Context, ws WizSession, d *Draft) (Screen, error) {
	screen, err := w.render(ctx, ws, d)
	if err != nil {
		return screen, err
	}
	// The poster's answer to this question, first among the buttons.
	if label, _, _ := d.hintValue(); label != "" {
		hint := Button{Label: w.texts.T(ws.Locale, "bot.wz.hint_btn", map[string]any{"Value": truncate(label, 40)}), Data: hintButtonData}
		screen.Buttons = append([][]Button{{hint}}, screen.Buttons...)
	}
	return screen, nil
}

func (w *Wizard) render(ctx context.Context, ws WizSession, d *Draft) (Screen, error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	btn := func(key, data string) Button { return Button{Label: t(key, nil), Data: data} }
	nav := func(rows ...[]Button) [][]Button {
		last := []Button{}
		if len(d.History) > 0 {
			last = append(last, btn("bot.wz.back_btn", "back"))
		}
		if d.Mode == ModeEdit && d.Scratch.Edit != "" {
			// Changing one part of an event: the way out is the event's card,
			// not "cancel" (which would read as throwing the whole edit away).
			// Inside a question it leaves the part as it was; once a new date
			// is finished (its summary, "one more date?") the date stays.
			key := "bot.wz.edit_home_btn"
			if d.Scratch.Edit == "dates" && (d.Step == stSConfirm || d.Step == stSMore) {
				key = "bot.wz.edit_to_card_btn"
			}
			last = append(last, btn(key, "e:home"))
		} else {
			last = append(last, btn("bot.wz.cancel_btn", "cancel"))
		}
		return append(rows, last)
	}
	header := func(n int, titleKey string) string {
		if d.Mode == ModeEdit && d.Scratch.Edit != "" {
			return t("bot.wz.edit_step_title", map[string]any{"Title": t("bot.wz.edit_field_"+d.Scratch.Edit, nil)}) + w.ticketsForLine(loc, d)
		}
		return t("bot.wz.step_title", map[string]any{"N": n, "Title": t(titleKey, nil)})
	}

	switch d.Step {
	case stEvName:
		if d.singleEdit() {
			return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.edit_ask_name", map[string]any{"Value": Esc(d.Event.Name)}), Buttons: nav()}, nil
		}
		rows := [][]Button{}
		posterButton := w.posterHints && d.Mode == ModeCreate && d.Event.PosterMediaID == ""
		if posterButton {
			// The one action on this screen is the name; reading a poster is a
			// separate, optional screen behind a button.
			rows = append(rows, []Button{btn("bot.wz.poster_first_btn", "poster")})
		}
		// The text names the button only when it is there.
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_name", map[string]any{"Poster": posterButton}), Buttons: nav(rows...)}, nil

	case stEvPosterAsk:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_poster_first", nil), Buttons: nav()}, nil

	case stEvAge:
		rows := [][]Button{}
		if ws.Defaults.Age != "" && !d.singleEdit() {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.Age}), Data: "keep"}})
		}
		row := []Button{}
		for _, a := range AgeOptions {
			label := a
			if d.singleEdit() && a == d.Event.Age {
				label = "✔ " + a
			}
			row = append(row, Button{Label: label, Data: "age:" + a})
		}
		rows = append(rows, row)
		ask := t("bot.wz.ask_age", nil)
		if d.singleEdit() {
			ask = t("bot.wz.edit_ask_age", map[string]any{"Value": d.Event.Age})
		}
		return Screen{Text: header(1, "bot.wz.title_event") + ask, Buttons: nav(rows...)}, nil

	case stEvPromoter:
		promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return Screen{}, err
		}
		rows := [][]Button{}
		if ws.Defaults.PromoterID != "" && ws.Defaults.PromoterName != "" && !d.singleEdit() && hasRef(promoters, ws.Defaults.PromoterID) {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.PromoterName}), Data: "keep"}})
		}
		orgLabel := t("bot.wz.promoter_org", nil)
		if ws.OrgName != "" {
			orgLabel = truncate(ws.OrgName, 48)
		}
		mark := func(on bool) string {
			if d.singleEdit() && on {
				return "✔ "
			}
			return ""
		}
		rows = append(rows, []Button{{Label: mark(d.Event.PromoterID == "") + orgLabel, Data: "prom:org"}})
		for _, p := range sortedByName(promoters) {
			rows = append(rows, []Button{{Label: mark(p.ID == d.Event.PromoterID) + truncate(p.Name, 48), Data: "prom:" + p.ID}})
		}
		rows = append(rows, []Button{btn("bot.wz.promoter_new", "prom:new")})
		ask := t("bot.wz.ask_promoter", map[string]any{"Org": Esc(orgLabel)})
		if d.singleEdit() {
			now := orgLabel
			if d.Event.PromoterName != "" {
				now = d.Event.PromoterName
			}
			ask = t("bot.wz.edit_ask_promoter", map[string]any{"Value": Esc(now)})
		}
		return Screen{Text: header(1, "bot.wz.title_event") + ask, Buttons: nav(rows...)}, nil

	case stPromoterName:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_promoter_name", nil), Buttons: nav()}, nil

	case stPromoterLegal:
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_promoter_legal", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil

	case stEvPoster:
		if d.singleEdit() {
			state := t("bot.wz.edit_poster_none", nil)
			if d.Event.PosterMediaID != "" {
				state = t("bot.wz.edit_poster_yes", nil)
			}
			return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.edit_ask_poster", map[string]any{"State": state}), Buttons: nav()}, nil
		}
		if d.Event.PosterMediaID != "" {
			// The poster came with the first question: keep it, or send another.
			// An event read back from arena knows only the poster's id, not
			// its size; the question then names no dimensions.
			have := t("bot.wz.ask_poster_have", map[string]any{"W": d.Event.PosterW, "H": d.Event.PosterH})
			if d.Event.PosterW <= 0 || d.Event.PosterH <= 0 {
				have = t("bot.wz.ask_poster_have_nosize", nil)
			}
			return Screen{Text: header(1, "bot.wz.title_event") + have,
				Buttons: nav([]Button{btn("bot.wz.keep_poster_btn", "keep")})}, nil
		}
		return Screen{Text: header(1, "bot.wz.title_event") + t("bot.wz.ask_poster", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil

	case stSDate:
		return w.dateQuestion(loc, d, header(2, "bot.wz.title_when"), t("bot.wz.ask_date", map[string]any{"N": d.Cur + 1}), nav), nil

	case stSTime:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_time", nil),
			Buttons: nav([]Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": d.defaultTime()}), Data: "default"}})}, nil

	case stSSalesEnd:
		s := d.session()
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_sales_end", map[string]any{"Time": s.Time}),
			Buttons: nav(
				[]Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": s.Time}), Data: offsetData("se:", 0)}},
				[]Button{
					{Label: t("bot.wz.sales_end_after_btn", map[string]any{"Min": 30, "Time": clockAt(s.Time, 30)}), Data: offsetData("se:", 30)},
					{Label: t("bot.wz.sales_end_after_h_btn", map[string]any{"Hours": 1, "Time": clockAt(s.Time, 60)}), Data: offsetData("se:", 60)},
				},
				[]Button{btn("bot.wz.other_time_btn", "other")},
			)}, nil

	case stSSalesEndTime:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_sales_end_time", map[string]any{"Time": d.session().Time}),
			Buttons: nav()}, nil

	case stSDoors:
		s := d.session()
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_doors", nil),
			Buttons: nav(
				[]Button{
					{Label: t("bot.wz.doors_before_btn", map[string]any{"Min": 30, "Time": clockAt(s.Time, -30)}), Data: offsetData("dr:", 30)},
					{Label: t("bot.wz.doors_before_h_btn", map[string]any{"Hours": 1, "Time": clockAt(s.Time, -60)}), Data: offsetData("dr:", 60)},
				},
				[]Button{btn("bot.wz.doors_none_btn", "none")},
			)}, nil

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
		// The poster's city names the country first (UXH-02): "Keep: Latvia"
		// above a poster that says Valencia invited a hurried tap on the
		// wrong country. Keep stays, below the poster's answer, unless it is
		// the same country.
		posterCountries := w.posterCountries(d, countries)
		keepIsPoster := false
		for _, c := range posterCountries {
			rows = append(rows, []Button{{Label: t("bot.wz.hint_btn", map[string]any{"Value": truncate(c.Name, 40)}), Data: "country:" + c.ID}})
			keepIsPoster = keepIsPoster || c.ID == ws.Defaults.CountryID
		}
		if ws.Defaults.CountryID != "" && !keepIsPoster {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.CountryName}), Data: "keep"}})
		}
		// A long list goes through regions: the region buttons first, the
		// countries the organization already has venues in right under "keep",
		// and only after a region is pressed, that region's countries.
		if groupByRegion(countries) {
			region := d.Scratch.CountryRegion
			if !validRegion(countries, region) {
				region = ""
			}
			if region == "" {
				for _, c := range sortedByName(countries) {
					if hasVenue[strings.ToUpper(c.ISO2)] {
						rows = append(rows, []Button{{Label: c.Name, Data: "country:" + c.ID}})
					}
				}
				for _, r := range presentRegions(countries) {
					rows = append(rows, []Button{btn(regionKey(r), "region:"+r)})
				}
				return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_region", nil), Buttons: nav(chunk(rows, 2)...)}, nil
			}
			countries = inRegion(countries, region)
			rows = nil
			for _, c := range sortedByName(countries) {
				rows = append(rows, []Button{{Label: c.Name, Data: "country:" + c.ID}})
			}
			rows = chunk(rows, 2)
			rows = append(rows, []Button{btn("bot.wz.regions_back_btn", "region:")})
			return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_country", nil), Buttons: nav(rows...)}, nil
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
		listed := map[string]bool{}
		for _, c := range cities {
			listed[geotz.Normalize(c.Name)] = true
		}
		for _, c := range append(first, rest...) {
			rows = append(rows, []Button{{Label: c.Name, Data: "city:" + c.ID}})
		}
		// The platform's own list holds only cities somebody already used, so
		// the country's biggest cities follow as buttons (they are added to
		// the platform when pressed).
		shown := 0
		for i, name := range geotz.TopCities(s.CountryISO2) {
			if shown >= maxCitySuggestions {
				break
			}
			if listed[geotz.Normalize(name)] {
				continue
			}
			rows = append(rows, []Button{{Label: truncate(name, 40), Data: "city:g:" + strconv.Itoa(i)}})
			shown++
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
		if d.Scratch.DupVenueID != "" {
			rows = append(rows, []Button{{Label: t("bot.wz.venue_dup_btn", map[string]any{"Value": truncate(d.Scratch.DupVenueName, 40)}), Data: "venue:" + d.Scratch.DupVenueID}})
		}
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

	case stSPlan, stSPlanPrice:
		return w.renderSeated(loc, d, header, nav), nil

	case stSPlanAsk, stSPlanFile:
		return w.renderPlanUpload(loc, d, header, nav), nil

	case stVName:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_name", nil), Buttons: nav()}, nil
	case stVAddress:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_address", nil), Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}, nil
	case stVCapacity:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_capacity", nil), Buttons: nav()}, nil
	case stVTz:
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_venue_tz", nil), Buttons: nav(zoneButtons(d)...)}, nil

	case stSCapacity:
		s := d.session()
		rows := [][]Button{}
		text := t("bot.wz.ask_capacity", nil)
		if s.Capacity > 0 {
			rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": s.Capacity}), Data: "keep"}})
			text = t("bot.wz.ask_capacity_known", map[string]any{"Venue": Esc(s.VenueName), "Capacity": s.Capacity})
		}
		return Screen{Text: header(2, "bot.wz.title_when") + text, Buttons: nav(rows...)}, nil

	case stSMore:
		ask := t("bot.wz.ask_more_sessions", nil)
		if d.Mode == ModeEdit {
			// An added date has its own categories: the creation text's "the
			// same prices as the first one" is not true here.
			ask = t("bot.wz.ask_more_sessions_edit", nil)
		}
		return Screen{Text: header(2, "bot.wz.title_when") + ask,
			Buttons: nav([]Button{btn("bot.wz.more_session_btn", "more"), btn("bot.wz.sessions_done_btn", "next")})}, nil

	case stSTickets:
		if d.Cur < 1 || d.Cur > len(d.Sessions) {
			return Screen{Text: d.Step, Buttons: nav()}, nil
		}
		prev := d.Sessions[d.Cur-1]
		ptk := d.ticketsOf(d.Cur - 1)
		rows := [][]Button{}
		if !prev.seated() {
			rows = append(rows, []Button{{Label: t("bot.wz.s_tickets_same_btn", map[string]any{"Prev": dateLabel(prev)}), Data: "st:same"}})
		}
		rows = append(rows, []Button{btn("bot.wz.s_tickets_own_btn", "st:own")})
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.ask_s_tickets", map[string]any{
			"N": d.Cur + 1, "Prev": dateLabel(prev), "List": w.ticketsText(loc, d.currencyOrGuess(), ptk)}),
			Buttons: nav(rows...)}, nil

	case stSConfirm:
		return w.confirmDateScreen(loc, d, header(2, "bot.wz.title_when"), nav), nil

	case stTDate:
		rows := [][]Button{}
		for i, s := range d.Sessions {
			label := dateLabel(s)
			if s.VenueName != "" {
				label += " · " + s.VenueName
			}
			rows = append(rows, []Button{{Label: truncate(label, 56), Data: "td:" + itoa(i+1)}})
		}
		if !d.anySeated() {
			rows = append(rows, []Button{btn("bot.wz.t_date_all_btn", "td:all")})
		}
		now := w.ticketsText(loc, d.currencyOrGuess(), d.ticketsOf(0))
		if d.ticketsDiffer() {
			now = w.ticketsByDate(loc, d)
		}
		// "For all dates" takes the first date's way of selling (one category,
		// at once, one after another) for every date: say so before the press.
		allNote := t("bot.wz.t_date_all_note", map[string]any{"Value": w.modeLabel(loc, d.ticketsOf(0).Mode)})
		if d.anySeated() {
			allNote = t("bot.wz.t_date_seated_note", nil)
		}
		return Screen{Text: clipMessage(header(3, "bot.wz.title_tickets")+now+"\n\n"+t("bot.wz.ask_t_date", nil)+"\n"+allNote, maxMessageRunes), Buttons: nav(rows...)}, nil

	case stTMode:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_mode", nil),
			Buttons: nav([]Button{btn("bot.wz.t_mode_single", "mode:single")}, []Button{btn("bot.wz.t_mode_multi", "mode:multi")})}, nil

	case stTName:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_name", nil),
			Buttons: nav([]Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": t("bot.wz.t_name_default", nil)}), Data: "default"}})}, nil

	case stTPrice:
		text := t("bot.wz.ask_t_price", nil)
		if cur := d.currencyOrGuess(); cur != "" {
			text = t("bot.wz.ask_t_price_cur", map[string]any{"Currency": cur})
		}
		return Screen{Text: header(3, "bot.wz.title_tickets") + text, Buttons: nav()}, nil

	case stTChanges:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_changes", nil),
			Buttons: nav([]Button{btn("bot.wz.t_changes_no", "no"), btn("bot.wz.t_changes_yes", "yes")})}, nil

	case stTChangeDate:
		return w.dateQuestion(loc, d, header(3, "bot.wz.title_tickets"), t("bot.wz.ask_t_change_date", nil), nav), nil

	case stTChangePrice:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_change_price", map[string]any{"Date": DisplayDate(d.Scratch.Step.From)}), Buttons: nav()}, nil

	case stTChangeMore:
		items := []string{}
		for _, s := range d.tk().Schedule {
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
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_name", map[string]any{"N": len(d.tk().Categories) + 1}), Buttons: nav()}, nil
	case stTCatPrice:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_price", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), Buttons: nav()}, nil
	case stTCatPlaces:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_places", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), Buttons: nav()}, nil
	case stTCatLast:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_last", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}),
			Buttons: nav([]Button{btn("bot.wz.t_cat_last_btn", "last")}, []Button{btn("bot.wz.t_cat_next_btn", "next")})}, nil
	case stTCatUntil:
		return w.dateQuestion(loc, d, header(3, "bot.wz.title_tickets"), t("bot.wz.ask_t_cat_until", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), nav), nil

	case stTCatMore:
		return Screen{Text: header(3, "bot.wz.title_tickets") + t("bot.wz.ask_t_cat_more", map[string]any{"List": w.categoriesList(loc, d.currencyOrGuess(), d.tk())}),
			Buttons: nav([]Button{btn("bot.wz.t_cat_more_btn", "more"), btn("bot.wz.t_cat_done_btn", "done")})}, nil

	case stXDescription:
		if d.singleEdit() {
			now := t("bot.wz.edit_desc_none", nil)
			rows := [][]Button{}
			if strings.TrimSpace(d.Event.Description) != "" {
				now = t("bot.wz.edit_desc_now", map[string]any{"Text": descriptionPreview(d.Event, previewRunes)})
				rows = append(rows, []Button{btn("bot.wz.clear_description_btn", "clear")})
			}
			return Screen{Text: header(4, "bot.wz.title_extra") + now + t("bot.wz.edit_ask_description", nil), Buttons: nav(rows...)}, nil
		}
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

	case stEditMenu:
		text := t("bot.wz.edit_card_title", nil) + w.Summary(loc, d)
		changes := d.changes()
		marked := map[string]bool{}
		for _, c := range changes {
			marked[c.Field] = true
		}
		mark := func(field, key string) string {
			if marked[field] {
				return "✎ " + t(key, nil)
			}
			return t(key, nil)
		}
		rows := [][]Button{}
		// A save that stopped half way may leave a date to send with no
		// change listed: "Publish changes" is offered while the save has
		// anything left to post.
		if len(changes) > 0 || d.unsent() {
			// Each change on a line of its own, with what it was and what it
			// becomes, and a button that takes just that one back.
			if len(changes) > 0 {
				text += "\n\n" + t("bot.wz.edit_card_changes", map[string]any{"List": strings.Join(w.changeLines(loc, ws.OrgName, d), "\n")})
			}
			if d.partlySaved() {
				text += "\n\n" + t("bot.wz.edit_card_partly_saved", nil)
			}
			text += "\n\n" + t("bot.wz.edit_card_hint", nil)
			rows = append(rows, []Button{btn("bot.wz.edit_publish_btn", "publish")})
			if d.canUndo() {
				for _, c := range changes {
					label := t("bot.wz.edit_undo_btn", map[string]any{"What": w.undoLabel(loc, d, c.Key)})
					rows = append(rows, []Button{{Label: truncate(label, 56), Data: "e:undo:" + c.Key}})
				}
			}
		} else {
			text += "\n\n" + t("bot.wz.edit_card_clean", nil)
		}
		rows = append(rows,
			[]Button{{Label: mark("name", "bot.wz.edit_b_name"), Data: "e:name"}, {Label: mark("description", "bot.wz.edit_b_desc"), Data: "e:desc"}},
			[]Button{{Label: mark("poster", "bot.wz.edit_b_poster"), Data: "e:poster"}, {Label: mark("age", "bot.wz.edit_b_age"), Data: "e:age"}},
			[]Button{{Label: mark("promoter", "bot.wz.edit_b_promoter"), Data: "e:promoter"}, {Label: mark("currency", "bot.wz.edit_b_currency"), Data: "e:currency"}},
			[]Button{{Label: mark("dates", "bot.wz.edit_b_date"), Data: "e:date"}, {Label: mark("tickets", "bot.wz.edit_b_tickets"), Data: "e:tickets"}},
			[]Button{btn("bot.ses.open_btn", "e:sessions")},
			[]Button{btn("bot.wz.edit_exit_btn", "cancel")},
		)
		// A card listing many dates with their own categories stays under
		// Telegram's message limit.
		return Screen{Text: clipMessage(text, maxMessageRunes), Buttons: rows}, nil

	case stCancel:
		if d.Mode == ModeEdit {
			// Leaving an edit says what the draft holds, so "keep" and
			// "delete" are about something the person can see.
			text := t("bot.wz.cancel_ask_edit", nil) + "\n\n" + t("bot.wz.cancel_draft_empty", nil)
			if lines := w.changeLines(loc, ws.OrgName, d); len(lines) > 0 {
				text = t("bot.wz.cancel_ask_edit", nil) + "\n\n" + t("bot.wz.cancel_draft_has", map[string]any{"List": strings.Join(lines, "\n")})
			}
			return Screen{Text: clipMessage(text, maxMessageRunes), Buttons: [][]Button{
				{btn("bot.wz.cancel_keep_btn", "cancel:keep")},
				{btn("bot.wz.cancel_drop_edit_btn", "cancel:drop")},
				{btn("bot.wz.cancel_continue_btn", "cancel:no")},
			}}, nil
		}
		return Screen{Text: t("bot.wz.cancel_ask_create", nil), Buttons: [][]Button{
			{btn("bot.wz.cancel_continue_btn", "cancel:no")},
			{btn("bot.wz.cancel_keep_btn", "cancel:keep")},
			{btn("bot.wz.cancel_drop_btn", "cancel:drop")},
		}}, nil

	case stPreview:
		return w.previewScreen(loc, ws.OrgName, d), nil

	case stSummary:
		// "Not yet" is a deliberate choice: the screen then must not ask to
		// publish or talk about a «Publish» button it does not show (UXH-03).
		title, footer := "bot.wz.title_summary", "bot.wz.summary_footer"
		primary := btn("bot.wz.publish_btn", "publish")
		if !d.Publish {
			title, footer = "bot.wz.title_summary_draft", "bot.wz.summary_footer_draft"
			primary = btn("bot.wz.save_btn", "publish")
		}
		text := header(5, title) + w.Summary(loc, d) + "\n\n" + t(footer, nil)
		if d.Mode == ModeEdit {
			text = header(5, title) + w.Summary(loc, d) + "\n\n" + t("bot.wz.edit_footer", nil)
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
	if d.ticketsDiffer() {
		// The dates sell different categories: each date on its own line.
		b.WriteString(w.ticketsByDate(loc, d))
	} else {
		b.WriteString(w.ticketsText(loc, cur, d.ticketsOf(0)))
	}
	if len(d.Channels) > 1 {
		names := []string{}
		for _, c := range d.Channels {
			names = append(names, Esc(c.Name))
		}
		b.WriteString("\n" + t("bot.wz.summary_channels", map[string]any{"List": strings.Join(names, ", ")}))
	}
	if d.Mode == ModeEdit {
		// An existing event's sales status is not something the card changes.
		return b.String()
	}
	b.WriteString("\n")
	if d.Publish {
		b.WriteString(t("bot.wz.summary_publish_now", nil))
	} else {
		b.WriteString(t("bot.wz.summary_publish_later", nil))
	}
	return b.String()
}

// ticketsText is one ticket part as a sentence: "One category …", "At once:
// …" or "In stages: …".
func (w *Wizard) ticketsText(loc, cur string, tk *DraftTickets) string {
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	if len(tk.Categories) == 0 {
		// A date read back with no category yet sells nothing.
		return t("bot.wz.summary_no_categories", nil)
	}
	switch tk.Mode {
	case ModeSingle:
		if len(tk.Categories) > 0 {
			c := tk.Categories[0]
			sched := ""
			for _, s := range tk.Schedule {
				sched += ", " + t("bot.wz.summary_schedule_item", map[string]any{"Date": DisplayDate(s.From), "Price": FormatMoney(s.PriceMinor, cur, loc)})
			}
			return t("bot.wz.summary_single", map[string]any{"Name": Esc(c.Name), "Price": FormatMoney(c.PriceMinor, cur, loc), "Schedule": sched})
		}
	case ModeParallel:
		return t("bot.wz.summary_parallel", map[string]any{"List": w.categoriesList(loc, cur, tk)})
	case ModeSequence:
		return t("bot.wz.summary_sequence", map[string]any{"List": w.categoriesList(loc, cur, tk)})
	}
	return ""
}

// ticketsByDate lists the categories date by date: "Categories differ by
// date:" and a line per date.
func (w *Wizard) ticketsByDate(loc string, d *Draft) string {
	cur := d.currencyOrGuess()
	lines := []string{w.texts.T(loc, "bot.wz.summary_tickets_by_date", nil)}
	for i, s := range d.Sessions {
		lines = append(lines, "• "+dateLabel(s)+": "+w.ticketsText(loc, cur, d.ticketsOf(i)))
	}
	return strings.Join(lines, "\n")
}

// ticketsForLine heads a ticket question of an edited event: which date the
// categories are for (or every date), where the person is among the
// questions of one category ("step 2 of 4"), and that nothing changes until
// the last of them is answered.
func (w *Wizard) ticketsForLine(loc string, d *Draft) string {
	if d.Scratch.Work == nil || !isTicketStep(d.Step) {
		return ""
	}
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	what := t("bot.wz.t_for_one", nil)
	switch i := d.Scratch.WorkFor - 1; {
	case d.Scratch.WorkAll:
		what = t("bot.wz.t_for_all", nil)
	case len(d.Sessions) > 1 && i >= 0 && i < len(d.Sessions):
		what = t("bot.wz.t_for_date", map[string]any{"When": dateLabel(d.Sessions[i])})
	}
	n, total := ticketStepNo(d.tk().Mode, d.Step)
	return t("bot.wz.t_progress", map[string]any{"What": what, "N": n, "Total": total}) + "\n" +
		t("bot.wz.t_nothing_yet", nil) + "\n"
}

// ticketStepNo places a ticket question among the questions of one category:
// one category asks its name, its price and its price changes; categories
// sold at once ask name, price, places and "another?"; categories sold one
// after another ask name, price and where the category ends.
func ticketStepNo(mode, step string) (n, total int) {
	switch mode {
	case ModeSingle:
		switch step {
		case stTName:
			return 1, 3
		case stTPrice:
			return 2, 3
		}
		return 3, 3
	case ModeParallel:
		switch step {
		case stTCatName:
			return 1, 4
		case stTCatPrice:
			return 2, 4
		case stTCatPlaces:
			return 3, 4
		}
		return 4, 4
	}
	switch step {
	case stTCatName:
		return 1, 3
	case stTCatPrice:
		return 2, 3
	}
	return 3, 3
}

// modeLabel names a way of selling: one category, at once, one after another.
func (w *Wizard) modeLabel(loc, mode string) string {
	switch mode {
	case ModeParallel:
		return w.texts.T(loc, "bot.wz.t_kind_parallel", nil)
	case ModeSequence:
		return w.texts.T(loc, "bot.wz.t_kind_sequence", nil)
	}
	return w.texts.T(loc, "bot.wz.t_mode_single", nil)
}

// isTicketStep reports whether the step is one of the ticket questions.
func isTicketStep(step string) bool {
	switch step {
	case stTName, stTPrice, stTChanges, stTChangeDate, stTChangePrice, stTChangeMore,
		stTCatName, stTCatPrice, stTCatPlaces, stTCatLast, stTCatUntil, stTCatMore:
		return true
	}
	return false
}

// categoriesList renders "VIP 500 ILS × 20, Standard 250 ILS × 60" or
// "Start 99 ILS 3 tickets or until 30.09 → Regular 199 ILS → Door 249 ILS".
func (w *Wizard) categoriesList(loc, cur string, tk *DraftTickets) string {
	parts := []string{}
	for _, c := range tk.Categories {
		p := Esc(c.Name) + " " + FormatMoney(c.PriceMinor, cur, loc)
		switch tk.Mode {
		case ModeParallel:
			// A category of a seating plan that was read back has no count of its own.
			if c.Places > 0 {
				p += " × " + itoa(c.Places)
			}
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
	if tk.Mode == ModeSequence {
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

// maxZoneButtons caps the zone buttons: Russia alone has two dozen zones, and a
// zone that is not among the first few is reached by typing the city.
const maxZoneButtons = 6

// maxCitySuggestions caps the big-city buttons added under the platform's own
// cities on the city question.
const maxCitySuggestions = 16

// zoneButtons are the zone question's buttons: the zone a city name pointed at
// (the typed one, else the session's own city) first, then the zones of the
// country, the biggest city's zone first. Each button names the zone's
// largest city so a person who does not know IANA names can still choose.
func zoneButtons(d *Draft) [][]Button {
	iso2 := d.Scratch.NewVenue.CountryISO2
	rows := [][]Button{}
	seen := map[string]bool{}
	add := func(zone, mark string) {
		if zone == "" || seen[zone] {
			return
		}
		seen[zone] = true
		label := zone
		if city := geotz.CityOf(iso2, zone); city != "" {
			label = city + " · " + zone
		}
		rows = append(rows, []Button{{Label: truncate(mark+label, 56), Data: "tz:" + zone}})
	}
	if d.Scratch.TzHint != "" {
		add(d.Scratch.TzHint, "✔ ")
	} else {
		add(geotz.ForCity(iso2, d.session().CityName), "✔ ")
	}
	for _, z := range geotz.ZonesFor(iso2) {
		if len(rows) >= maxZoneButtons {
			break
		}
		add(z.ID, "")
	}
	return rows
}
