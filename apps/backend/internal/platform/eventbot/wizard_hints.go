package eventbot

// wizard_hints.go — what the poster said, offered as one-tap answers.
//
// When the organizer sends the poster (at the first question or at the
// poster step), the bot reads it with a vision model (posterread) and keeps
// the facts in Draft.Hints. From then on every question whose answer the
// poster printed shows one extra button, "From the poster: …"; pressing it
// is the same as typing the value. Nothing is ever written into the draft
// without that press: a model can misread a 3 for an 8, and the organizer
// is the one who knows.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// DraftHints is the poster's reading, kept with the draft.
type DraftHints struct {
	Name        string              `json:"name,omitempty"`
	Age         string              `json:"age,omitempty"`
	Date        string              `json:"date,omitempty"` // YYYY-MM-DD
	Time        string              `json:"time,omitempty"` // HH:MM
	VenueName   string              `json:"venue_name,omitempty"`
	City        string              `json:"city,omitempty"`
	Description string              `json:"description,omitempty"`
	Categories  []DraftHintCategory `json:"categories,omitempty"`
	// Source is "page" for an event page's reading (spec 59), "" for a
	// poster: it names the button ("From the page" or "From the poster").
	Source string `json:"source,omitempty"`
	// Dates are every date the page lists; the n-th is the hint of the n-th
	// date question. A poster gives Date and Time only.
	Dates []DraftHintDate `json:"dates,omitempty"`
	// Country is the page's ISO 3166-1 alpha-2 code, "" when unknown.
	Country string `json:"country,omitempty"`
	// Image is the page's picture, stored by arena-api and attached to
	// nothing until pressed at the poster question.
	Image *DraftHintImage `json:"image,omitempty"`
	// Offsets are the UTC offsets the page wrote for its dates ("+01:00"),
	// shown once in the "found" message; never applied.
	Offsets []string `json:"offsets,omitempty"`
}

// DraftHintDate is one date an event page lists.
type DraftHintDate struct {
	Date string `json:"date"` // YYYY-MM-DD
	Time string `json:"time,omitempty"`
}

// DraftHintImage is the page's picture.
type DraftHintImage struct {
	MediaID string `json:"media_id"`
	W       int    `json:"w"`
	H       int    `json:"h"`
}

// hintSourcePage marks hints read from an event page.
const hintSourcePage = "page"

// pageImageData is the callback payload of "From the page: picture".
const pageImageData = "pageimg"

// buttonKey is the text key of the hint button.
func (h DraftHints) buttonKey() string {
	if h.Source == hintSourcePage {
		return "bot.wz.hint_btn_page"
	}
	return "bot.wz.hint_btn"
}

// dateHint is the hint of the n-th date (0-based).
func (h DraftHints) dateHint(n int) (DraftHintDate, bool) {
	if n < len(h.Dates) {
		return h.Dates[n], true
	}
	if n == 0 && h.Date != "" {
		return DraftHintDate{Date: h.Date, Time: h.Time}, true
	}
	return DraftHintDate{}, false
}

// DraftHintCategory is one price the poster printed.
type DraftHintCategory struct {
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency,omitempty"`
}

// Empty reports whether the poster gave nothing usable.
func (h DraftHints) Empty() bool {
	return h.Name == "" && h.Age == "" && h.Date == "" && h.Time == "" && h.VenueName == "" &&
		h.City == "" && h.Description == "" && len(h.Categories) == 0 && len(h.Dates) == 0 && h.Image == nil
}

// HintsFromFacts converts the reader's facts into draft hints.
func HintsFromFacts(f posterread.Facts) DraftHints {
	h := DraftHints{Name: f.Name, Age: f.Age, Date: f.Date, Time: f.Time, VenueName: f.VenueName, City: f.City, Description: f.Description}
	for _, c := range f.Categories {
		h.Categories = append(h.Categories, DraftHintCategory{Name: c.Name, PriceMinor: c.PriceMinor, Currency: c.Currency})
	}
	return h
}

// hintButtonData is the callback payload of the "From the poster" button.
const hintButtonData = "hint"

// hintCategory is the poster category the current ticket question is about:
// the single category, or the n-th one being entered, or the one whose name
// was just typed.
func (d *Draft) hintCategory(step string) (DraftHintCategory, bool) {
	cats := d.Hints.Categories
	if len(cats) == 0 {
		return DraftHintCategory{}, false
	}
	switch step {
	case stTName, stTPrice, stXCurrency:
		return cats[0], true
	case stTCatName:
		if n := len(d.tk().Categories); n < len(cats) {
			return cats[n], true
		}
	case stTCatPrice:
		for _, c := range cats {
			if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(d.Scratch.Cat.Name)) {
				return c, true
			}
		}
		if n := len(d.tk().Categories); n < len(cats) {
			return cats[n], true
		}
	}
	return DraftHintCategory{}, false
}

// hintValue is the poster's answer to the current question, as the person
// would type it ("" when the poster has none). label is what the button
// shows; text and data are what pressing it feeds to apply — a typed text
// for free-text questions, a button payload for choice questions.
func (d *Draft) hintValue() (label, text, data string) {
	h := d.Hints
	switch d.Step {
	case stEvName:
		return h.Name, h.Name, ""
	case stEvAge:
		return h.Age, "", "age:" + h.Age
	case stSDate:
		if dh, ok := h.dateHint(d.Cur); ok && dh.Date != "" {
			v := DisplayDate(dh.Date)
			return v, v, ""
		}
	case stSTime:
		if dh, ok := h.dateHint(d.Cur); ok {
			return dh.Time, dh.Time, ""
		}
		if d.Cur == 0 {
			return h.Time, h.Time, ""
		}
	case stEvPoster:
		if img := h.Image; img != nil && img.MediaID != d.Event.PosterMediaID {
			v := "🖼 " + strconv.Itoa(img.W) + "×" + strconv.Itoa(img.H)
			return v, "", pageImageData
		}
	case stVName:
		return h.VenueName, h.VenueName, ""
	case stCityName:
		return h.City, h.City, ""
	case stXDescription:
		return truncate(h.Description, 40), h.Description, ""
	case stTName, stTCatName:
		if c, ok := d.hintCategory(d.Step); ok {
			return c.Name, c.Name, ""
		}
	case stTPrice, stTCatPrice:
		if c, ok := d.hintCategory(d.Step); ok {
			v := hintPrice(c.PriceMinor)
			return v, v, ""
		}
	case stXCurrency:
		if c, ok := d.hintCategory(d.Step); ok && c.Currency != "" {
			return c.Currency, "", "cur:" + c.Currency
		}
	}
	return "", "", ""
}

// hintPrice renders minor units the way a person types a price: "25" or
// "34.90".
func hintPrice(minor int64) string {
	if minor%100 == 0 {
		return fmt.Sprintf("%d", minor/100)
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// hintsNote lists what the poster said, for the message shown once after
// the reading.
func (w *Wizard) hintsNote(loc string, h DraftHints) string {
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	line := func(labelKey, value string) string {
		return t("bot.wz.ack", map[string]any{"Label": t(labelKey, nil), "Value": Esc(value)})
	}
	lines := []string{}
	if h.Name != "" {
		lines = append(lines, line("bot.wz.ack_name", h.Name))
	}
	if h.Age != "" {
		lines = append(lines, line("bot.wz.ack_age", h.Age))
	}
	if h.Date != "" {
		lines = append(lines, line("bot.wz.ack_date", DisplayDate(h.Date)))
	}
	if h.Time != "" {
		lines = append(lines, line("bot.wz.ack_time", h.Time))
	}
	if h.VenueName != "" {
		lines = append(lines, line("bot.wz.ack_venue", h.VenueName))
	}
	if h.City != "" {
		lines = append(lines, line("bot.wz.ack_city", h.City))
	}
	for _, c := range h.Categories {
		cur := c.Currency
		if cur == "" {
			cur = "…"
		}
		lines = append(lines, line("bot.wz.ack_category", c.Name+" — "+hintPrice(c.PriceMinor)+" "+cur))
	}
	if h.Description != "" {
		lines = append(lines, line("bot.wz.ack_description", truncate(h.Description, 80)))
	}
	if len(lines) == 0 {
		return t("bot.wz.poster_read_none", nil)
	}
	return t("bot.wz.poster_read", map[string]any{"List": strings.Join(lines, "\n")})
}

// maxPosterCountries caps the "From the poster" country buttons: a city name
// shared by many countries is still a short list.
const maxPosterCountries = 3

// posterCountries is the answer the poster gives to the country question of
// the first date (UXH-02): the platform's countries that have a place with
// the poster's city name, in geotz's order (a country where it is one of the
// largest cities first). Nil without a city, for a later date, or when no
// country of the platform has such a place.
func (w *Wizard) posterCountries(d *Draft, countries []RefItem) []RefItem {
	if d.Cur != 0 || (strings.TrimSpace(d.Hints.City) == "" && d.Hints.Country == "") {
		return nil
	}
	byISO := map[string]RefItem{}
	for _, c := range countries {
		byISO[strings.ToUpper(c.ISO2)] = c
	}
	var out []RefItem
	// An event page may name the country itself (spec 59): it comes first.
	if c, ok := byISO[strings.ToUpper(d.Hints.Country)]; ok && d.Hints.Country != "" {
		out = append(out, c)
		delete(byISO, strings.ToUpper(d.Hints.Country))
	}
	for _, cc := range geotz.CountriesOfCity(d.Hints.City) {
		if c, ok := byISO[cc]; ok {
			out = append(out, c)
			if len(out) == maxPosterCountries {
				break
			}
		}
	}
	return out
}
