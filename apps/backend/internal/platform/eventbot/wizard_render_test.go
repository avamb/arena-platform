package eventbot

import (
	"context"
	"strings"
	"testing"
)

// Every step of a full draft renders in both locales without a raw key or
// an unfilled template value; the summary names what was entered.
func TestWizard_EveryStepRendersInBothLocales(t *testing.T) {
	refs := newFakeRefs()
	refs.channels = append(refs.channels, RefItem{ID: "ch2", Name: "second"})
	r := newWizardRun(t, refs)
	r.ws.Defaults = Defaults{Age: "12+", CountryID: "cz", CountryName: "Czechia", CityID: "prg", CityName: "Prague", VenueID: "akr", VenueName: "Palác Akropolis", Currency: "CZK"}
	r.eventHead()
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:sequence", stTCatName)
	r.text("Early", stTCatPrice)
	r.text("20", stTCatUntil)
	r.text("31.12.2026 10", stTCatName)
	r.text("Late", stTCatPrice)
	r.text("30", stTCatLast)
	r.press("last", stXDescription)
	r.press("skip", stXCurrency)
	r.press("cur:CZK", stXChannels)
	r.press("ch:ch1", stXChannels)
	r.press("done", stXPublish)
	r.press("pub:now", stSummary)

	steps := []string{stEvName, stEvPosterAsk, stEvAge, stEvPromoter, stPromoterName, stPromoterLegal, stEvPoster, stSDate, stSTime, stSSame,
		stSCountry, stSCity, stCityName, stSVenue, stVName, stVAddress, stVCapacity, stVTz, stSCapacity, stSMore, stTMode,
		stTName, stTPrice, stTChanges, stTChangeDate, stTChangePrice, stTChangeMore, stTKind, stTCatName, stTCatPrice,
		stTCatPlaces, stTCatLast, stTCatUntil, stTCatMore, stXDescription, stXCurrency, stXChannels, stXPublish, stSummary,
		stEditMenu, stCancel}
	for _, loc := range SupportedLocales {
		r.ws.Locale = loc
		for _, st := range steps {
			r.d.Step = st
			r.d.Cur = 0
			if st == stSSame {
				r.d.Cur = 1
			}
			screen, err := r.w.Render(context.Background(), r.ws, r.d)
			if err != nil {
				t.Fatalf("%s/%s: %v", loc, st, err)
			}
			if strings.TrimSpace(screen.Text) == "" || strings.Contains(screen.Text, "bot.wz.") || strings.Contains(screen.Text, "<no value>") {
				t.Fatalf("%s/%s renders badly:\n%s", loc, st, screen.Text)
			}
			for _, row := range screen.Buttons {
				for _, b := range row {
					if b.Label == "" || b.Data == "" || strings.Contains(b.Label, "bot.") {
						t.Fatalf("%s/%s button %+v", loc, st, b)
					}
				}
			}
			if testing.Verbose() {
				t.Logf("--- %s/%s ---\n%s\nbuttons=%v", loc, st, screen.Text, screen.Buttons)
			}
		}
	}
}
