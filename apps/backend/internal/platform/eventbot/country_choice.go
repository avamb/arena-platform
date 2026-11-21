package eventbot

// country_choice.go — the one way the bot asks for a country over the full
// world list (spec 64 section 2.1.1), shared by the event wizard (the country
// of a venue) and the organizer application (the country of the organization).
//
// A short quick-pick group comes first, then the seven regions of the world; a
// region shows its countries as buttons, a page at a time. A country typed by
// name offers its matches as buttons. A country is only ever PICKED: nothing
// here creates one. The two screens differ only in what a press sends back
// (countryScheme) and in what they do with the pick.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// countryPageSize is how many countries one page of a region holds: two
// buttons in each of the five rows a list page has (paging.go).
const countryPageSize = listPageSize * 2

// maxCountryMatches caps the buttons a typed country name offers.
const maxCountryMatches = 8

// defaultRegions is the order of the regions when the server names none.
var defaultRegions = []string{"europe", "middle_east", "asia", "africa", "north_america", "latin_america", "oceania"}

// countryScheme builds the callback payloads of a country screen.
type countryScheme struct {
	// Pick is the payload of a country button.
	Pick func(iso2 string) string
	// Region opens a region; "" goes back to the region list.
	Region func(region string) string
	// Page opens page n of a region.
	Page func(region string, page int) string
}

// countryTexts are the labels a country screen needs.
type countryTexts struct {
	Regions func(region string) string // the label of a region button
	Back    string                     // "← Regions"
	Prev    string
	Next    string
}

// countryView is what a country screen shows.
type countryView struct {
	List  WorldList
	Quick []string // codes, in order; unknown codes are skipped
	// Region is the open region ("" = the region list); Page its 1-based page.
	Region string
	Page   int
	// Matches are the codes a typed name matched; non-empty replaces the quick
	// pick and the regions.
	Matches []string
	// Venues counts the venues in scope by code: a country that has some leads
	// its region and shows the number.
	Venues map[string]int
	// Flags puts the flag before the country's name.
	Flags bool
}

// worldRegions are the regions to offer: the server's order, only those that
// hold a country, never "other".
func (l WorldList) worldRegions() []string {
	order := l.Regions
	if len(order) == 0 {
		order = defaultRegions
	}
	have := map[string]bool{}
	for _, c := range l.Countries {
		have[c.Region] = true
	}
	out := make([]string, 0, len(order))
	for _, r := range order {
		if r != "other" && have[r] {
			out = append(out, r)
		}
	}
	return out
}

// validWorldRegion reports whether region is one the list offers.
func (l WorldList) validWorldRegion(region string) bool {
	for _, r := range l.worldRegions() {
		if r == region {
			return true
		}
	}
	return false
}

func (v countryView) label(c WorldCountry) string {
	name := c.Name
	if name == "" {
		name = c.NameEn
	}
	name = truncate(name, 40)
	if v.Flags {
		if f := flagOf(c.ISO2); f != "" {
			name = f + " " + name
		}
	}
	return name
}

// byName sorts countries by the name the person reads, any letter case.
func byName(cs []WorldCountry) {
	sort.SliceStable(cs, func(i, j int) bool { return strings.ToLower(cs[i].Name) < strings.ToLower(cs[j].Name) })
}

// regionCountries lists a region's countries: those with venues in scope first
// (most venues first, then by name), the rest by name.
func (v countryView) regionCountries(region string) []WorldCountry {
	var with, rest []WorldCountry
	for _, c := range v.List.Countries {
		if c.Region != region {
			continue
		}
		if v.Venues[c.ISO2] > 0 {
			with = append(with, c)
		} else {
			rest = append(rest, c)
		}
	}
	byName(with)
	sort.SliceStable(with, func(i, j int) bool { return v.Venues[with[i].ISO2] > v.Venues[with[j].ISO2] })
	byName(rest)
	return append(with, rest...)
}

// countryPages is how many pages n countries take.
func countryPages(n int) int {
	if n <= 0 {
		return 1
	}
	return (n + countryPageSize - 1) / countryPageSize
}

// clampPage keeps a page number inside 1..pages.
func clampPage(page, pages int) int {
	if page < 1 {
		return 1
	}
	if page > pages {
		return pages
	}
	return page
}

// pagerButtons is the "‹ 2/5 ›" row of paging.go for a screen whose presses
// are plain wizard payloads: nil for a one-page list. The middle button only
// shows the position (calNoop).
func pagerButtons(page, pages int, prev, next string, data func(page int) string) []Button {
	if pages <= 1 {
		return nil
	}
	var row []Button
	if page > 1 {
		row = append(row, Button{Label: "‹ " + prev, Data: data(page - 1)})
	}
	row = append(row, Button{Label: strconv.Itoa(page) + "/" + strconv.Itoa(pages), Data: calNoop})
	if page < pages {
		row = append(row, Button{Label: next + " ›", Data: data(page + 1)})
	}
	return row
}

// countryRows draws the buttons of a country screen: the matches of a typed
// name, or the region's page, or the quick pick over the region list. Each
// button is on its own row of two. The second result is the screen kind:
// "matches", "region" or "regions", which picks the question text.
func countryRows(v countryView, s countryScheme, tx countryTexts) ([][]Button, string) {
	byISO := map[string]WorldCountry{}
	for _, c := range v.List.Countries {
		byISO[c.ISO2] = c
	}
	pick := func(c WorldCountry, label string) []Button {
		return []Button{{Label: label, Data: s.Pick(c.ISO2)}}
	}
	if len(v.Matches) > 0 {
		var rows [][]Button
		for _, code := range v.Matches {
			if c, ok := byISO[code]; ok {
				rows = append(rows, pick(c, v.label(c)))
			}
		}
		rows = chunk(rows, 2)
		return append(rows, []Button{{Label: tx.Back, Data: s.Region("")}}), "matches"
	}
	if v.Region != "" && v.List.validWorldRegion(v.Region) {
		all := v.regionCountries(v.Region)
		pages := countryPages(len(all))
		page := clampPage(v.Page, pages)
		from := (page - 1) * countryPageSize
		to := from + countryPageSize
		if to > len(all) {
			to = len(all)
		}
		var rows [][]Button
		for _, c := range all[from:to] {
			label := v.label(c)
			if n := v.Venues[c.ISO2]; n > 0 {
				label = truncate(label, 34) + " · " + strconv.Itoa(n)
			}
			rows = append(rows, pick(c, label))
		}
		rows = chunk(rows, 2)
		if row := pagerButtons(page, pages, tx.Prev, tx.Next, func(n int) string { return s.Page(v.Region, n) }); row != nil {
			rows = append(rows, row)
		}
		return append(rows, []Button{{Label: tx.Back, Data: s.Region("")}}), "region"
	}
	var rows [][]Button
	for _, code := range v.Quick {
		if c, ok := byISO[strings.ToUpper(code)]; ok {
			rows = append(rows, pick(c, v.label(c)))
		}
	}
	rows = chunk(rows, 2)
	var regionRows [][]Button
	for _, r := range v.List.worldRegions() {
		regionRows = append(regionRows, []Button{{Label: tx.Regions(r), Data: s.Region(r)}})
	}
	return append(rows, chunk(regionRows, 2)...), "regions"
}

// matchCountries finds the countries a typed name could mean: a code first
// (ES, ESP), then the names that begin with it, then the names that contain it,
// in the request language and in English, any letter case and without accents.
// At most limit are returned; fewer than two letters match nothing.
func matchCountries(list []WorldCountry, typed string, limit int) []WorldCountry {
	want := geotz.Normalize(typed)
	if len([]rune(want)) < 2 {
		return nil
	}
	code := strings.ToUpper(strings.TrimSpace(typed))
	var codes, begins, parts []WorldCountry
	for _, c := range list {
		names := []string{geotz.Normalize(c.Name), geotz.Normalize(c.NameEn)}
		switch {
		case code == c.ISO2 || code == c.ISO3:
			codes = append(codes, c)
		case anyPrefix(names, want):
			begins = append(begins, c)
		case anyContains(names, want):
			parts = append(parts, c)
		}
	}
	byName(begins)
	byName(parts)
	out := append(append(codes, begins...), parts...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func anyPrefix(names []string, want string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, want) {
			return true
		}
	}
	return false
}

func anyContains(names []string, want string) bool {
	for _, n := range names {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// isoCodes lists the codes of countries, for a payload that must stay short.
func isoCodes(cs []WorldCountry) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ISO2)
	}
	return out
}
