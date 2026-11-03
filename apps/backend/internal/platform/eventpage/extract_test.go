package eventpage

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testNow is the clock of every extraction test.
var testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func loadPage(t *testing.T, name, pageURL string) Page {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	return Page{FinalURL: u, HTML: b}
}

func warningCodes(r Result) []string {
	var out []string
	for _, w := range r.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func findWarning(r Result, code string) (Warning, bool) {
	for _, w := range r.Warnings {
		if w.Code == code {
			return w, true
		}
	}
	return Warning{}, false
}

func TestExtract_SingleJSONLDEvent(t *testing.T) {
	r := Extract(loadPage(t, "single_event.html", "https://teatro.example.org/obras/cerezos"), testNow)
	f := r.Facts
	if f.Name != "El jardín de los cerezos" {
		t.Errorf("name = %q", f.Name)
	}
	want := []DateFact{{Date: "2099-11-20", Time: "19:00", Doors: "18:30", UTCOffset: "+01:00"}}
	if !reflect.DeepEqual(f.Dates, want) {
		t.Errorf("dates = %+v", f.Dates)
	}
	if f.Date != "2099-11-20" || f.Time != "19:00" {
		t.Errorf("first date = %q %q", f.Date, f.Time)
	}
	if f.VenueName != "Teatro Kolybel" || f.City != "Madrid" || f.Country != "ES" || f.Street != "Calle de Alcalá 20" {
		t.Errorf("venue = %q, %q, %q, %q", f.VenueName, f.City, f.Country, f.Street)
	}
	if f.Age != "16+" {
		t.Errorf("age = %q", f.Age)
	}
	if f.Description != "Una comedia de Chéjov en ruso.\nDuración: 2 horas & un descanso." {
		t.Errorf("description = %q", f.Description)
	}
	wantCats := []Category{
		{Name: "Platea", PriceMinor: 2500, Currency: "EUR"},
		{Name: "Anfiteatro", PriceMinor: 1850, Currency: "EUR"},
		{Name: "Invitación", PriceMinor: 0, Currency: "EUR"},
	}
	if !reflect.DeepEqual(f.Categories, wantCats) {
		t.Errorf("categories = %+v", f.Categories)
	}
	if r.Language != "ru" {
		t.Errorf("language = %q, want the event's inLanguage", r.Language)
	}
	for _, field := range []string{"name", "dates", "venue", "description", "age", "categories"} {
		if r.Sources[field] != SourceJSONLD {
			t.Errorf("source of %s = %q", field, r.Sources[field])
		}
	}
	wantImgs := []ImageCandidate{
		{URL: "https://teatro.example.org/uploads/poster-4x5.jpg", Source: SourceJSONLD},
		{URL: "https://cdn.example.org/square.png", Source: SourceJSONLD},
		{URL: "https://cdn.example.org/og-wide.jpg", Source: SourceOpenGraph},
	}
	if !reflect.DeepEqual(r.ImageURLs, wantImgs) {
		t.Errorf("images = %+v", r.ImageURLs)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings = %v", warningCodes(r))
	}
	if r.NeedsModel() {
		t.Error("a page with dates and prices must not need the model")
	}
	if strings.Contains(r.Text, "Inicio Programa") || strings.Contains(r.Text, "© Teatro") {
		t.Errorf("navigation and footer must not reach the model text: %q", r.Text)
	}
}

func TestExtract_SeriesInGraph(t *testing.T) {
	r := Extract(loadPage(t, "series_graph.html", "https://blueroom.example.cz/jazz"), testNow)
	f := r.Facts
	if f.Name != "Jazz Nights" {
		t.Errorf("name = %q", f.Name)
	}
	if len(f.Dates) != MaxDates {
		t.Fatalf("dates = %d, want %d: %+v", len(f.Dates), MaxDates, f.Dates)
	}
	first, last := f.Dates[0], f.Dates[len(f.Dates)-1]
	if first != (DateFact{Date: "2099-01-02", Time: "20:00", UTCOffset: "+01:00"}) {
		t.Errorf("first = %+v", first)
	}
	if last.Date != "2099-03-13" {
		t.Errorf("last kept = %+v (the 11th future date must be skipped)", last)
	}
	byDate := map[string]DateFact{}
	for _, d := range f.Dates {
		byDate[d.Date] = d
	}
	if d := byDate["2099-01-23"]; d.Time != "20:00" || d.UTCOffset != "" {
		t.Errorf("no offset: %+v", d)
	}
	if d := byDate["2099-01-30"]; d.UTCOffset != "+00:00" {
		t.Errorf("Z: %+v", d)
	}
	if d := byDate["2099-02-06"]; d.Time != "" {
		t.Errorf("a day without time: %+v", d)
	}
	if d := byDate["2099-02-13"]; d.UTCOffset != "+01:00" {
		t.Errorf("+0100: %+v", d)
	}
	if _, ok := byDate["2099-01-16"]; ok {
		t.Error("a cancelled date must not be offered")
	}
	for code, count := range map[string]int{WarnDatesSkipped: 1, WarnPastDatesSkipped: 1, WarnCancelledSkipped: 1} {
		w, ok := findWarning(r, code)
		if !ok || w.Count != count {
			t.Errorf("warning %s = %+v (present %v), want count %d", code, w, ok, count)
		}
	}
	wantCats := []Category{{Name: "Standing", PriceMinor: 35000, Currency: "CZK"}, {Name: "Table", PriceMinor: 120000, Currency: "CZK"}}
	if !reflect.DeepEqual(f.Categories, wantCats) {
		t.Errorf("categories = %+v", f.Categories)
	}
	if f.VenueName != "Blue Room" || f.Street != "Karlova 5, Praha" || f.City != "" {
		t.Errorf("venue = %q %q %q", f.VenueName, f.Street, f.City)
	}
	if f.Description != "Live jazz every Friday." {
		t.Errorf("description inherited = %q", f.Description)
	}
}

func TestExtract_OpenGraphOnly(t *testing.T) {
	r := Extract(loadPage(t, "og_only.html", "https://klub.example.cz/koncert"), testNow)
	f := r.Facts
	if f.Name != "Podzimní koncert" || r.Sources["name"] != SourceOpenGraph {
		t.Errorf("name = %q from %q", f.Name, r.Sources["name"])
	}
	if f.Description != "Velký podzimní koncert kapely." || r.Sources["description"] != SourceOpenGraph {
		t.Errorf("description = %q", f.Description)
	}
	if len(f.Dates) != 0 || len(f.Categories) != 0 {
		t.Errorf("open graph has no dates or prices: %+v", f)
	}
	if !r.NeedsModel() {
		t.Error("no dates and no prices: the model must be asked")
	}
	for _, code := range []string{WarnNoDates, WarnNoPrices} {
		if _, ok := findWarning(r, code); !ok {
			t.Errorf("missing warning %s: %v", code, warningCodes(r))
		}
	}
	if r.Language != "cs" {
		t.Errorf("language = %q", r.Language)
	}
	if !reflect.DeepEqual(r.ImageURLs, []ImageCandidate{{URL: "https://klub.example.cz/img/poster.png", Source: SourceOpenGraph}}) {
		t.Errorf("images = %+v", r.ImageURLs)
	}
	for _, want := range []string{"Podzimní koncert", "sobota 21. listopadu, od 20:00.", "Vstupné: 450 Kč na místě."} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("model text misses %q: %q", want, r.Text)
		}
	}
	for _, not := range []string{"Ignore me", "color:red"} {
		if strings.Contains(r.Text, not) {
			t.Errorf("model text holds %q", not)
		}
	}
}

func TestExtract_PriceRangeAndOnline(t *testing.T) {
	r := Extract(loadPage(t, "price_range.html", "https://fest.example.org/"), testNow)
	if len(r.Facts.Categories) != 0 {
		t.Errorf("a range is not categories: %+v", r.Facts.Categories)
	}
	w, ok := findWarning(r, WarnPriceRangeOnly)
	if !ok || !reflect.DeepEqual(w.Values, []string{"20", "60", "USD"}) {
		t.Errorf("range warning = %+v", w)
	}
	if _, ok := findWarning(r, WarnNoPrices); ok {
		t.Error("a price range already explains the missing prices")
	}
	if _, ok := findWarning(r, WarnOnline); !ok {
		t.Errorf("online warning missing: %v", warningCodes(r))
	}
	if r.Facts.Name != "Summer Fest" || len(r.Facts.Dates) != 1 || r.Facts.Dates[0].Time != "" {
		t.Errorf("facts = %+v", r.Facts)
	}
}

func TestExtract_ScriptOnlyPage(t *testing.T) {
	r := Extract(loadPage(t, "script_app.html", "https://app.example.org/"), testNow)
	if _, ok := findWarning(r, WarnJavaScriptPage); !ok {
		t.Errorf("warnings = %v", warningCodes(r))
	}
	if r.NeedsModel() {
		t.Error("no text: nothing for the model to read")
	}
	if r.Facts.Name != "App" {
		t.Errorf("name from <title> = %q", r.Facts.Name)
	}
}

func TestExtract_EmptyListsAreNeverNil(t *testing.T) {
	r := Extract(Page{HTML: []byte("<html></html>")}, testNow)
	if r.Facts.Dates == nil || r.Facts.Categories == nil || r.Warnings == nil {
		t.Errorf("nil list in %+v", r)
	}
	if !r.Empty() {
		t.Error("nothing found must read as empty")
	}
}

func TestPriceToMinor(t *testing.T) {
	cases := map[string]int64{
		"25": 2500, "25.00": 2500, "34.9": 3490, "34,90": 3490, "1 200,00": 120000,
		"1.200,50": 120050, "1,200.50": 120050, "1,200": 120000, "€ 25": 2500, "25 EUR": 2500,
		"0": 0, "0.00": 0, "12.500": 1250, "1 500": 150000,
	}
	for in, want := range cases {
		got, ok := priceToMinor(in)
		if !ok || got != want {
			t.Errorf("priceToMinor(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "free", "-5", "1.2.3,4,5", "12.345", "999999999", "25.999"} {
		if got, ok := priceToMinor(bad); ok {
			t.Errorf("priceToMinor(%q) = %d, want refused", bad, got)
		}
	}
}

func TestParseStart(t *testing.T) {
	ok := map[string]parsedStart{
		"2026-11-20":                {date: "2026-11-20"},
		"2026-11-20T19:00":          {date: "2026-11-20", clock: "19:00"},
		"2026-11-20 19:00:30.5":     {date: "2026-11-20", clock: "19:00"},
		"2026-11-20T19:00:00-05:00": {date: "2026-11-20", clock: "19:00", offset: "-05:00"},
	}
	for in, want := range ok {
		got, valid := parseStart(in)
		got.instant = time.Time{}
		if !valid || got != want {
			t.Errorf("parseStart(%q) = %+v %v", in, got, valid)
		}
	}
	for _, bad := range []string{"2026-02-30", "2026-11-20T25:00", "20.11.2026", "2026-11-20T19:00+15:00", "1999-01-01"} {
		if _, valid := parseStart(bad); valid {
			t.Errorf("parseStart(%q) accepted", bad)
		}
	}
}

func TestPastDates(t *testing.T) {
	now := time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)
	p, _ := parseStart("2026-11-20T19:00:00+01:00") // 18:00 UTC
	if !p.past(now) {
		t.Error("an instant before now is past")
	}
	p, _ = parseStart("2026-11-19T20:00")
	if p.past(now) {
		t.Error("a day-old date without an offset gets a day of grace")
	}
	p, _ = parseStart("2026-11-18")
	if !p.past(now) {
		t.Error("two days ago is past")
	}
}

func TestAgeRating(t *testing.T) {
	for in, want := range map[string]string{"18-": "18+", "18+": "18+", "6-12": "6+", "0": "0+", "14-": "", "adults": ""} {
		if got := ageRating(in); got != want {
			t.Errorf("ageRating(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsTicketingHost(t *testing.T) {
	refused := []string{
		"www.ticketmaster.es", "ticketmaster.com", "WWW.EVENTBRITE.CO.UK", "eventbrite.com.",
		"tickets.dice.fm", "dice.fm", "www.eventim.de", "goout.net", "afisha.yandex.ru",
		"tickets.arenasoldout.com", "www.entradas.com", "leaan.co.il",
	}
	for _, h := range refused {
		if !IsTicketingHost(h) {
			t.Errorf("%s must be refused", h)
		}
	}
	allowed := []string{
		"teatro-kolybel.es", "myticketmaster-fan.org", "eventbrite", "theatre.example.com",
		"dice.example.com", "afisha.mytheatre.ru", "yandex.ru", "", "colosseum-club.cz",
	}
	for _, h := range allowed {
		if IsTicketingHost(h) {
			t.Errorf("%s must be allowed", h)
		}
	}
}

func TestFitsPoster(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want bool
	}{{1080, 1350, true}, {1200, 1200, true}, {1200, 630, false}, {800, 1000, false}, {1000, 1250, true}} {
		if got := FitsPoster(c.w, c.h); got != c.want {
			t.Errorf("FitsPoster(%d, %d) = %v", c.w, c.h, got)
		}
	}
}
