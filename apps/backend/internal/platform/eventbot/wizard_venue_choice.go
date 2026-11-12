package eventbot

// wizard_venue_choice.go — where the event is: a country, a city, a venue,
// chosen in that order (spec 64 section 2.1.1).
//
// The country comes from the full world list, so a venue may stand in a country
// the platform did not know yet (country_choice.go). A city of a country the
// platform knows is a platform city, found or made by name as before. A city of
// a country outside the platform is only a NAME that waits in the draft: the
// venue is made with country_iso2 + city_name in one call, and the server then
// makes the country and the city in the venue's own transaction, so a person
// who stops half way leaves nothing behind. The venues of a city come from the
// venue directory (own first, then verified venues of other organizations).

import (
	"context"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// wizCountryScheme is what a country press sends back in the wizard.
var wizCountryScheme = countryScheme{
	Pick:   func(iso2 string) string { return "country:" + iso2 },
	Region: func(region string) string { return "region:" + region },
	Page:   func(_ string, page int) string { return "page:" + strconv.Itoa(page) },
}

func (w *Wizard) countryTexts(loc string) countryTexts {
	return countryTexts{
		Regions: func(r string) string { return w.texts.T(loc, regionKey(r), nil) },
		Back:    w.texts.T(loc, "bot.wz.regions_back_btn", nil),
		Prev:    w.texts.T(loc, "bot.btn_prev", nil),
		Next:    w.texts.T(loc, "bot.btn_next", nil),
	}
}

// directory reads the venue directory; a failure is only logged by the caller
// choosing a fallback, never shown: the directory is a help on top of the lists.
func (w *Wizard) directory(ctx context.Context, ws WizSession, country, cityID, q string) (VenueDir, bool) {
	dir, err := w.refs.VenueDirectory(ctx, ws.JWT, ws.OrgID, country, cityID, q, ws.Locale)
	return dir, err == nil
}

// sameCountry reports whether the remembered defaults are about the session's
// country: by code when both have one, else by the reference id.
func sameCountry(def Defaults, s *DraftSession) bool {
	if def.CountryISO2 != "" && s.CountryISO2 != "" {
		return strings.EqualFold(def.CountryISO2, s.CountryISO2)
	}
	return def.CountryID == s.CountryID
}

// setCountry puts a world country into the date and forgets what depended on
// the old one. A country outside the reference has no CountryID: its code,
// name and currency are enough until a venue is made there.
func (d *Draft) setCountry(c WorldCountry) {
	s := d.session()
	s.CountryID, s.CountryName, s.CountryISO2, s.Currency = c.CountryID, c.Name, c.ISO2, c.Currency
	s.CityID, s.CityName, s.VenueID, s.VenueName, s.Timezone = "", "", "", "", ""
	d.Scratch.CountryRegion, d.Scratch.CountryPage, d.Scratch.CountryMatches = "", 0, nil
}

// ─── country ─────────────────────────────────────────────────────────────────

func (w *Wizard) applyCountry(ctx context.Context, ws WizSession, d *Draft, text, data string) (string, error) {
	list, err := w.refs.WorldCountries(ctx, ws.JWT, ws.Locale)
	if err != nil {
		return "", err
	}
	if rg, ok := strings.CutPrefix(data, "region:"); ok {
		// A region button opens that region's countries; an empty one is the way
		// back to the regions (and out of a list of matches).
		d.Scratch.CountryRegion, d.Scratch.CountryPage, d.Scratch.CountryMatches = "", 0, nil
		if list.validWorldRegion(rg) {
			d.Scratch.CountryRegion, d.Scratch.CountryPage = rg, 1
		}
		return "", nil
	}
	if n, ok := strings.CutPrefix(data, "page:"); ok {
		d.Scratch.CountryPage = ParsePage(n)
		return "", nil
	}
	var chosen *WorldCountry
	pick := func(match func(WorldCountry) bool) {
		for i := range list.Countries {
			if match(list.Countries[i]) {
				chosen = &list.Countries[i]
				return
			}
		}
	}
	switch {
	case data == "keep":
		def := ws.Defaults
		pick(func(c WorldCountry) bool {
			return (def.CountryISO2 != "" && strings.EqualFold(c.ISO2, def.CountryISO2)) || (def.CountryID != "" && c.CountryID == def.CountryID)
		})
	case strings.HasPrefix(data, "country:"):
		// A code; a button of an older message carries the reference id.
		token := strings.TrimPrefix(data, "country:")
		pick(func(c WorldCountry) bool {
			return strings.EqualFold(c.ISO2, token) || (c.CountryID != "" && c.CountryID == token)
		})
	case data == "" && text != "":
		matches := matchCountries(list.Countries, text, maxCountryMatches)
		if len(matches) == 0 {
			d.Scratch.CountryMatches = nil
			return d.reject("err_country_match", w.texts.T(ws.Locale, "bot.geo.no_match", map[string]any{"Name": Esc(truncate(text, 40))})), nil
		}
		d.Scratch.CountryRegion, d.Scratch.CountryPage = "", 0
		d.Scratch.CountryMatches = isoCodes(matches)
		return "", nil
	}
	if chosen == nil {
		return "", nil
	}
	d.setCountry(*chosen)
	d.goTo(stSCity)
	return "", nil
}

func (w *Wizard) renderCountry(ctx context.Context, ws WizSession, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) (Screen, error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	list, err := w.refs.WorldCountries(ctx, ws.JWT, loc)
	if err != nil {
		return Screen{}, err
	}
	dir, _ := w.directory(ctx, ws, "", "", "")
	venues := map[string]int{}
	for _, c := range dir.Countries {
		venues[c.ISO2] = c.Venues
	}
	rows := [][]Button{}
	// The poster's city names the country first (UXH-02): "Keep: Latvia" above a
	// poster that says Valencia invited a hurried tap on the wrong country. Keep
	// stays, below the poster's answer, unless it is the same country.
	asRefs := make([]RefItem, 0, len(list.Countries))
	for _, c := range list.Countries {
		asRefs = append(asRefs, RefItem{ID: c.ISO2, Name: c.Name, ISO2: c.ISO2, Currency: c.Currency})
	}
	// Defaults saved before the world list may name the country by id only.
	keepISO := strings.ToUpper(ws.Defaults.CountryISO2)
	for _, c := range list.Countries {
		if keepISO == "" && ws.Defaults.CountryID != "" && c.CountryID == ws.Defaults.CountryID {
			keepISO = c.ISO2
		}
	}
	keepIsPoster := false
	for _, c := range w.posterCountries(d, asRefs) {
		rows = append(rows, []Button{{Label: t(d.Hints.buttonKey(), map[string]any{"Value": truncate(c.Name, 40)}), Data: "country:" + c.ISO2}})
		keepIsPoster = keepIsPoster || strings.EqualFold(c.ISO2, keepISO)
	}
	if (ws.Defaults.CountryID != "" || ws.Defaults.CountryISO2 != "") && !keepIsPoster {
		rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.CountryName}), Data: "keep"}})
	}
	view := countryView{
		List: list, Quick: dir.QuickPick, Region: d.Scratch.CountryRegion, Page: d.Scratch.CountryPage,
		Matches: d.Scratch.CountryMatches, Venues: venues,
	}
	shown, kind := countryRows(view, wizCountryScheme, w.countryTexts(loc))
	rows = append(rows, shown...)
	ask := "bot.wz.ask_region"
	switch kind {
	case "region":
		ask = "bot.wz.ask_country"
	case "matches":
		ask = "bot.geo.matches"
	}
	return Screen{Text: header(2, "bot.wz.title_when") + t(ask, nil), Buttons: nav(rows...)}, nil
}

// ─── city ────────────────────────────────────────────────────────────────────

// outsideReference reports whether the date's country is one the platform does
// not have yet: no cities exist there, the city is only a name.
func (s *DraftSession) outsideReference() bool { return s.CountryID == "" }

// waitCity keeps a city name in the draft without creating anything.
func (d *Draft) waitCity(name string) {
	s := d.session()
	s.CityID, s.CityName = "", strings.TrimSpace(name)
	s.VenueID, s.VenueName, s.Timezone = "", "", ""
	d.enterVenue()
}

// pickCity puts a platform city into the date.
func (d *Draft) pickCity(id, name string) {
	s := d.session()
	s.CityID, s.CityName = id, name
	s.VenueID, s.VenueName, s.Timezone = "", "", ""
	d.enterVenue()
}

// enterVenue moves to the venue question with a fresh list.
func (d *Draft) enterVenue() {
	d.Scratch.VenuePage, d.Scratch.VenueQuery = 0, ""
	d.goTo(stSVenue)
}

func (w *Wizard) applyCity(ctx context.Context, ws WizSession, d *Draft, text, data string) error {
	loc := ws.Locale
	s := d.session()
	if data == "city:new" {
		d.goTo(stCityName)
		return nil
	}
	if idx, ok := strings.CutPrefix(data, "city:g:"); ok {
		// One of the country's biggest cities, offered from the embedded data:
		// it is found or added by name like a typed one.
		n, err := strconv.Atoi(idx)
		top := geotz.TopCities(s.CountryISO2)
		if err != nil || n < 0 || n >= len(top) {
			return nil
		}
		return w.cityByName(ctx, ws, d, top[n])
	}
	if data != "" {
		id := strings.TrimPrefix(data, "city:")
		if data == "keep" {
			id = ws.Defaults.CityID
		}
		if id == "" {
			return nil
		}
		if dir, ok := w.directory(ctx, ws, s.CountryISO2, "", ""); ok {
			for _, c := range dir.Cities {
				if c.ID == id {
					d.pickCity(c.ID, c.Name)
					return nil
				}
			}
		}
		if s.outsideReference() {
			return nil
		}
		cities, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, loc)
		if err != nil {
			return err
		}
		for _, c := range cities {
			if c.ID == id {
				d.pickCity(c.ID, c.Name)
				return nil
			}
		}
		return nil
	}
	// A city typed straight onto the question: the list holds only the cities
	// somebody already used, so most organizers' city is not in it.
	if name := strings.TrimSpace(text); name != "" {
		return w.cityByName(ctx, ws, d, name)
	}
	return nil
}

// cityByName takes a city given by name. In a country of the platform a listed
// city of that name is picked, any other is added (the API finds an existing
// city of that name before it creates one). In a country outside the platform
// the name waits and nothing is created.
func (w *Wizard) cityByName(ctx context.Context, ws WizSession, d *Draft, name string) error {
	s := d.session()
	if s.outsideReference() {
		d.waitCity(name)
		return nil
	}
	want := geotz.Normalize(name)
	if dir, ok := w.directory(ctx, ws, s.CountryISO2, "", ""); ok {
		for _, c := range dir.Cities {
			if geotz.Normalize(c.Name) == want {
				d.pickCity(c.ID, c.Name)
				return nil
			}
		}
	}
	cities, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, ws.Locale)
	if err != nil {
		return err
	}
	for _, c := range cities {
		if geotz.Normalize(c.Name) == want {
			d.pickCity(c.ID, c.Name)
			return nil
		}
	}
	c, err := w.refs.CreateCity(ctx, ws.JWT, ws.OrgID, s.CountryID, name, ws.Locale)
	if err != nil {
		return err
	}
	d.pickCity(c.ID, c.Name)
	return nil
}

// maxDirectoryCities caps the directory's cities on the city question.
const maxDirectoryCities = 24

func (w *Wizard) renderCity(ctx context.Context, ws WizSession, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) (Screen, error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	s := d.session()
	rows := [][]Button{}
	if ws.Defaults.CityID != "" && sameCountry(ws.Defaults, s) && !s.outsideReference() {
		rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.CityName}), Data: "keep"}})
	}
	listed := map[string]bool{}
	ask := t("bot.wz.ask_city", nil)
	if !s.outsideReference() {
		// The cities with venues lead, with their number of venues.
		if dir, ok := w.directory(ctx, ws, s.CountryISO2, "", ""); ok {
			for i, c := range dir.Cities {
				if i >= maxDirectoryCities {
					break
				}
				listed[c.ID] = true
				rows = append(rows, []Button{{Label: truncate(c.Name, 34) + " · " + strconv.Itoa(c.Venues), Data: "city:" + c.ID}})
			}
		}
		cities, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, loc)
		if err != nil {
			return Screen{}, err
		}
		rest := []RefItem{}
		for _, c := range sortedByName(cities) {
			if !listed[c.ID] {
				rest = append(rest, c)
			}
		}
		if len(rest) > 24 {
			rest = rest[:24]
		}
		for _, c := range rest {
			rows = append(rows, []Button{{Label: c.Name, Data: "city:" + c.ID}})
		}
		for _, c := range cities {
			listed[geotz.Normalize(c.Name)] = true
		}
	} else {
		ask = t("bot.wz.ask_city_outside", map[string]any{"Country": Esc(s.CountryName)})
	}
	// The platform's own list holds only cities somebody already used, so the
	// country's biggest cities follow as buttons (in a country of the platform
	// they are added when pressed, elsewhere the name only waits).
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
	if !s.outsideReference() {
		rows = append(rows, []Button{{Label: t("bot.wz.city_other", nil), Data: "city:new"}})
	}
	return Screen{Text: header(2, "bot.wz.title_when") + ask, Buttons: nav(rows...)}, nil
}

// ─── venue ───────────────────────────────────────────────────────────────────

// venueChoices lists the venues of the date's city that the organization may
// use: the directory's, own first. Without the directory the organization's own
// venues and the verified shared ones are listed, as before it existed.
func (w *Wizard) venueChoices(ctx context.Context, ws WizSession, s *DraftSession, q string) ([]DirVenue, error) {
	if s.CityID == "" {
		return nil, nil
	}
	if dir, ok := w.directory(ctx, ws, "", s.CityID, q); ok {
		return dir.Venues, nil
	}
	own, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
	if err != nil {
		return nil, err
	}
	var out []DirVenue
	want := strings.ToLower(q)
	add := func(v VenueRef, own bool) {
		if v.CityID == s.CityID && strings.Contains(strings.ToLower(v.Name), want) {
			out = append(out, DirVenue{ID: v.ID, Name: v.Name, CityID: v.CityID, CountryISO2: v.CountryISO2,
				Timezone: v.Timezone, Capacity: v.Capacity, Own: own, Verified: !own})
		}
	}
	for _, v := range own {
		add(v, true)
	}
	for _, v := range w.sharedVenues(ctx, ws, s.CityID, "") {
		if !hasVenueID(own, v.ID) {
			add(v, false)
		}
	}
	return out, nil
}

func (w *Wizard) applyVenue(ctx context.Context, ws WizSession, d *Draft, text, data string) error {
	s := d.session()
	d.Scratch.DupVenueID, d.Scratch.DupVenueName = "", ""
	switch {
	case data == "venue:new":
		nv := VenueCreate{CountryID: s.CountryID, CountryISO2: s.CountryISO2, CityID: s.CityID, Lang: ws.Locale}
		if s.CityID == "" {
			nv.CityName = s.CityName
		}
		d.Scratch.NewVenue = nv
		d.goTo(stVName)
		return nil
	case strings.HasPrefix(data, "page:"):
		d.Scratch.VenuePage = ParsePage(strings.TrimPrefix(data, "page:"))
		return nil
	case data == "q:clear":
		d.Scratch.VenueQuery, d.Scratch.VenuePage = "", 1
		return nil
	case data == "" && text != "":
		// Typed text searches the venues of the city by name.
		if s.CityID != "" {
			d.Scratch.VenueQuery, d.Scratch.VenuePage = truncate(text, 60), 1
		}
		return nil
	}
	id := strings.TrimPrefix(data, "venue:")
	if data == "keep" {
		id = ws.Defaults.VenueID
	}
	if id == "" {
		return nil
	}
	venues, err := w.venueChoices(ctx, ws, s, "")
	if err != nil {
		return err
	}
	if !hasDirVenue(venues, id) && d.Scratch.VenueQuery != "" {
		// Pressed on a page of search results: the list without the search may
		// not hold it (it is capped).
		if venues, err = w.venueChoices(ctx, ws, s, d.Scratch.VenueQuery); err != nil {
			return err
		}
	}
	for _, dv := range venues {
		if dv.ID == id {
			w.pickVenue(ctx, ws, d, dv.Ref())
			return nil
		}
	}
	// A venue of the organization in another city or in a waiting city: the one a
	// refused "new venue" already is (same name) is offered as a button.
	if own, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID); err == nil {
		for _, v := range own {
			if v.ID == id {
				w.pickVenue(ctx, ws, d, v)
				return nil
			}
		}
	}
	return nil
}

// pickVenue puts an existing venue into the date and moves on to its seating
// plan question, or to the places.
func (w *Wizard) pickVenue(ctx context.Context, ws WizSession, d *Draft, v VenueRef) {
	s := d.session()
	s.VenueID, s.VenueName, s.Timezone = v.ID, v.Name, v.Timezone
	if s.Capacity == 0 {
		s.Capacity = v.Capacity
	}
	if s.seated() {
		// Back to the venue question and a venue chosen again: the plan of the
		// first one does not belong to this one.
		s.PlanVersionID, s.PlanName, s.Tickets = "", "", nil
	}
	d.Scratch.PlanAfter = planAfterCapacity
	d.Scratch.NoPlanVenueID = ""
	if w.offerPlans(ctx, ws, d) {
		return
	}
	d.Scratch.NoPlanVenueID = v.ID
	d.goTo(stSCapacity)
}

func hasDirVenue(venues []DirVenue, id string) bool {
	for _, v := range venues {
		if v.ID == id {
			return true
		}
	}
	return false
}

func (w *Wizard) renderVenue(ctx context.Context, ws WizSession, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) (Screen, error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	s := d.session()
	rows := [][]Button{}
	if d.Scratch.DupVenueID != "" {
		rows = append(rows, []Button{{Label: t("bot.wz.venue_dup_btn", map[string]any{"Value": truncate(d.Scratch.DupVenueName, 40)}), Data: "venue:" + d.Scratch.DupVenueID}})
	}
	if s.CityID == "" {
		// A city that waits by name has no venues yet: the only way on is a new one.
		rows = append(rows, []Button{{Label: t("bot.wz.venue_add_btn", nil), Data: "venue:new"}})
		return Screen{Text: header(2, "bot.wz.title_when") + t("bot.wz.venue_in_new_city", map[string]any{"City": Esc(s.CityName)}), Buttons: nav(rows...)}, nil
	}
	if ws.Defaults.VenueID != "" && ws.Defaults.CityID == s.CityID {
		rows = append(rows, []Button{{Label: t("bot.wz.keep_btn", map[string]any{"Value": ws.Defaults.VenueName}), Data: "keep"}})
	}
	q := d.Scratch.VenueQuery
	venues, err := w.venueChoices(ctx, ws, s, q)
	if err != nil {
		return Screen{}, err
	}
	text := t("bot.wz.ask_venue", nil)
	if q != "" {
		text = t("bot.wz.venue_search", map[string]any{"City": Esc(s.CityName), "Q": Esc(q)})
		if len(venues) == 0 {
			text = t("bot.wz.venue_search_none", map[string]any{"Q": Esc(q)})
		}
	}
	pages := max(1, (len(venues)+listPageSize-1)/listPageSize)
	page := clampPage(d.Scratch.VenuePage, pages)
	from := (page - 1) * listPageSize
	to := from + listPageSize
	if to > len(venues) {
		to = len(venues)
	}
	shared := false
	for _, v := range venues[from:to] {
		label := truncate(v.Name, 40)
		if !v.Own && v.Verified {
			label = sharedVenueMark + truncate(v.Name, 38)
			shared = true
		}
		if v.HasPlan {
			label += " · " + t("bot.wz.venue_tag_plan", nil)
		}
		rows = append(rows, []Button{{Label: label, Data: "venue:" + v.ID}})
	}
	if row := pagerButtons(page, pages, t("bot.btn_prev", nil), t("bot.btn_next", nil), func(n int) string { return "page:" + strconv.Itoa(n) }); row != nil {
		rows = append(rows, row)
	}
	if shared {
		text += "\n\n" + t("bot.wz.venue_shared_note", nil)
	}
	if q != "" {
		rows = append(rows, []Button{{Label: t("bot.wz.venue_all_btn", nil), Data: "q:clear"}})
	}
	rows = append(rows, []Button{{Label: t("bot.wz.venue_new", nil), Data: "venue:new"}})
	return Screen{Text: header(2, "bot.wz.title_when") + text, Buttons: nav(rows...)}, nil
}

// ─── time zone of a new venue ────────────────────────────────────────────────

// TimezoneIO is the optional extension of RefIO that names the zones of any ISO
// country (GET /v1/geo/countries/{iso2}/timezones). A source without it leaves
// a country the built-in data does not know to a typed zone, as before.
type TimezoneIO interface {
	CountryTimezones(ctx context.Context, jwt, iso2 string) (zones []TzOption, single bool, err error)
}

// apiZone asks the API for the zones of a country the built-in data does not
// know. A single zone is returned to be used at once; several are kept in the
// draft for the zone question's buttons. "" when there is nothing to use.
func (w *Wizard) apiZone(ctx context.Context, ws WizSession, d *Draft, iso2 string) string {
	d.Scratch.TzZones = nil
	if len(geotz.ZonesFor(iso2)) > 0 {
		return ""
	}
	io, ok := w.refs.(TimezoneIO)
	if !ok {
		return ""
	}
	zones, single, err := io.CountryTimezones(ctx, ws.JWT, iso2)
	if err != nil || len(zones) == 0 {
		return ""
	}
	if single && len(zones) == 1 {
		return zones[0].ID
	}
	d.Scratch.TzZones = zones
	return ""
}
