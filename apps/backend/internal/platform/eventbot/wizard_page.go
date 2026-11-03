package eventbot

// wizard_page.go — "Fill in from a link": the organizer sends the address of
// their OWN event page, arena-api reads it (POST .../event-page-facts, spec
// 59) and the answer becomes the draft's hints, exactly like a poster's:
// every found value is a "From the page: …" button, nothing is written
// without the press.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventpage"
)

// pageFactsCallTimeout is how long one page reading may take: arena-api
// spends up to a minute (page, model, picture).
const pageFactsCallTimeout = 80 * time.Second

// PageReading is arena-api's answer about an event page.
type PageReading struct {
	Facts    eventpage.Facts     `json:"facts"`
	Sources  map[string]string   `json:"sources"`
	FinalURL string              `json:"final_url"`
	Language string              `json:"language"`
	Warnings []eventpage.Warning `json:"warnings"`
}

// EventPageFacts asks arena-api what the organizer's event page says.
func (c *ArenaClient) EventPageFacts(ctx context.Context, jwt string, orgID uuid.UUID, pageURL string) (PageReading, error) {
	var out PageReading
	slow := *c
	hc := *c.http
	hc.Timeout = pageFactsCallTimeout
	slow.http = &hc
	err := slow.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/event-page-facts", jwt,
		map[string]string{"url": pageURL}, &out)
	return out, err
}

// HintsFromPage converts a page reading into draft hints. The page's
// picture becomes a hint only when it passes the bot's poster rules.
func HintsFromPage(p PageReading) DraftHints {
	f := p.Facts
	h := DraftHints{
		Source: hintSourcePage, Name: f.Name, Age: f.Age, Date: f.Date, Time: f.Time,
		VenueName: f.VenueName, City: f.City, Description: f.Description, Country: f.Country,
	}
	for _, d := range f.Dates {
		h.Dates = append(h.Dates, DraftHintDate{Date: d.Date, Time: d.Time})
		if d.UTCOffset != "" {
			h.Offsets = append(h.Offsets, d.UTCOffset)
		}
	}
	for _, c := range f.Categories {
		h.Categories = append(h.Categories, DraftHintCategory{Name: c.Name, PriceMinor: c.PriceMinor, Currency: c.Currency})
	}
	if img := f.Image; img != nil && img.FitsPoster && img.MediaID != "" {
		h.Image = &DraftHintImage{MediaID: img.MediaID, W: img.Width, H: img.Height}
	}
	return h
}

// looksLikePageURL reports whether a typed answer is a web address, not a
// name: one word starting with http:// or https://.
func looksLikePageURL(s string) bool {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, " \n\t") {
		return false
	}
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}

// readPageHints reads the page at rawURL and keeps the answer in the draft
// as hints. It reports whether hints were set; on failure it has told the
// person why and what to do.
func (b *Bot) readPageHints(ctx context.Context, chatID int64, loc, jwt string, orgID uuid.UUID, rawURL string, d *Draft) bool {
	done := make(chan struct{})
	notice := time.AfterFunc(posterReadingDelay, func() {
		defer close(done)
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.page_reading", nil), nil)
	})
	reading, err := b.arena.EventPageFacts(ctx, jwt, orgID, strings.TrimSpace(rawURL))
	if !notice.Stop() {
		<-done
	}
	if err != nil {
		var api *APIError
		if !errors.As(err, &api) || api.Status >= 500 && api.Code != "event_page.fetch_failed" {
			b.logger.Warn("eventbot: event page reading failed", slog.String("error", err.Error()))
		}
		b.send(ctx, chatID, b.texts.T(loc, pageErrorKey(err), nil), nil)
		return false
	}
	d.Hints = HintsFromPage(reading)
	b.send(ctx, chatID, b.wizard.pageNote(loc, d.Hints, reading), nil)
	return true
}

// pageErrorKey is the text of a failed page reading, by the API's code.
func pageErrorKey(err error) string {
	var api *APIError
	if !errors.As(err, &api) {
		return "bot.wz.page_err_unavailable"
	}
	switch api.Code {
	case "event_page.ticketing_platform":
		return "bot.wz.page_err_ticketing"
	case "event_page.invalid_url", "event_page.invalid_request":
		return "bot.wz.page_err_invalid"
	case "event_page.destination_not_allowed":
		return "bot.wz.page_err_not_public"
	case "event_page.not_html", "event_page.too_large":
		return "bot.wz.page_err_not_page"
	case "event_page.nothing_found":
		return "bot.wz.page_err_nothing"
	case "event_page.fetch_failed":
		return "bot.wz.page_err_fetch"
	case "event_page.rate_limited":
		return "bot.wz.page_err_rate"
	}
	return "bot.wz.page_err_unavailable"
}

// pageNote lists what the page said, its warnings, and how the hints work.
func (w *Wizard) pageNote(loc string, h DraftHints, p PageReading) string {
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
	for i, d := range h.Dates {
		v := DisplayDate(d.Date)
		if d.Time != "" {
			v += " " + d.Time
		}
		if i < len(p.Facts.Dates) && p.Facts.Dates[i].UTCOffset != "" && d.Time != "" {
			v += " (UTC" + p.Facts.Dates[i].UTCOffset + ")"
		}
		lines = append(lines, line("bot.wz.ack_date", v))
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
	if h.Image != nil {
		lines = append(lines, line("bot.wz.page_ack_image", strconv.Itoa(h.Image.W)+"×"+strconv.Itoa(h.Image.H)))
	}
	text := t("bot.wz.page_read", map[string]any{"List": strings.Join(lines, "\n")})
	var notes []string
	for _, wn := range p.Warnings {
		if key := pageWarningKey(wn.Code); key != "" {
			notes = append(notes, t(key, map[string]any{"Count": wn.Count}))
		}
	}
	if p.Sources["dates"] == "model" || p.Sources["categories"] == "model" || p.Sources["venue"] == "model" {
		notes = append(notes, t("bot.wz.page_note_model", nil))
	}
	if len(notes) > 0 {
		text += "\n\n" + strings.Join(dedupe(notes), "\n")
	}
	return text
}

// pageWarningKey is the text of a reading's warning, "" for one not shown.
func pageWarningKey(code string) string {
	switch code {
	case eventpage.WarnNoDates:
		return "bot.wz.page_note_no_dates"
	case eventpage.WarnNoPrices, eventpage.WarnPriceRangeOnly:
		return "bot.wz.page_note_no_prices"
	case eventpage.WarnDatesSkipped:
		return "bot.wz.page_note_more_dates"
	case eventpage.WarnOnline:
		return "bot.wz.page_note_online"
	case eventpage.WarnImageNotPoster:
		return "bot.wz.page_note_image_not_poster"
	}
	return ""
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
