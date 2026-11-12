package eventbot

// onboarding_country.go — the country questions of the organizer application
// ("country" and "address_country") over the full world list. The same screens
// as the event wizard's country question (country_choice.go): a quick pick on
// top, the seven regions, a region's countries a page at a time, and a typed
// name that offers its matches as buttons. The stored answer stays the
// two-letter code. The dialog keeps no state for this: the region and the page
// travel in the buttons' payloads.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
)

// maxQuickCountries is how many quick-pick countries the application shows.
const maxQuickCountries = 8

// onbHead is the top of a question: progress, label and hint.
func (b *Bot) onbHead(loc string, app *OnbApplication, f OnbSchemaField) string {
	head := b.texts.T(loc, "bot.onb.progress", map[string]any{"Pct": app.Progress}) + "\n\n<b>" + Esc(f.Label) + "</b>"
	if f.Hint != "" {
		head += "\n" + Esc(f.Hint)
	}
	return head
}

// onbCountryScheme is what a press of the country question sends back.
func onbCountryScheme(key string) countryScheme {
	return countryScheme{
		Pick:   func(iso2 string) string { return "onb:c:" + key + ":" + iso2 },
		Region: func(region string) string { return "onb:cr:" + key + ":" + region },
		Page:   func(region string, page int) string { return "onb:cp:" + key + ":" + region + ":" + strconv.Itoa(page) },
	}
}

// onbQuickCountries is the quick pick: the countries where organizers already
// work, and a short list of popular ones while there are none.
func onbQuickCountries(list WorldList) []string {
	var quick []string
	for _, c := range list.Countries {
		if c.HasOrganizers && len(quick) < maxQuickCountries {
			quick = append(quick, c.ISO2)
		}
	}
	if len(quick) == 0 {
		for _, cc := range popularCountries {
			if _, ok := list.ByISO2(cc); ok && len(quick) < maxQuickCountries {
				quick = append(quick, cc)
			}
		}
	}
	return quick
}

// onbCountryAsk draws the country question: the matches of a typed name, or a
// page of a region, or the quick pick over the regions. editMsgID is the
// message a page press redraws, nil for a new question.
func (b *Bot) onbCountryAsk(ctx context.Context, chatID int64, editMsgID *int, loc, head, key, region string, page int, matches []string) {
	list, err := b.arena.WorldCountries(ctx, "", loc)
	if err != nil {
		b.logger.Error("eventbot: world countries failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	view := countryView{List: list, Quick: onbQuickCountries(list), Region: region, Page: page, Matches: matches, Flags: true}
	tx := countryTexts{
		Regions: func(r string) string { return b.texts.T(loc, regionKey(r), nil) },
		Back:    b.texts.T(loc, "bot.wz.regions_back_btn", nil),
		Prev:    b.texts.T(loc, "bot.btn_prev", nil),
		Next:    b.texts.T(loc, "bot.btn_next", nil),
	}
	rows, kind := countryRows(view, onbCountryScheme(key), tx)
	ask := "bot.onb.country_hint"
	switch kind {
	case "region":
		ask = "bot.wz.ask_country"
	case "matches":
		ask = "bot.geo.matches"
	}
	kb := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		out := make([]models.InlineKeyboardButton, 0, len(row))
		for _, bt := range row {
			out = append(out, btn(bt.Label, bt.Data))
		}
		kb = append(kb, out)
	}
	b.reply(ctx, chatID, editMsgID, head+"\n\n"+b.texts.T(loc, ask, nil), inline(kb...))
}

// onbCountryPress handles "onb:cr:<key>:<region>" and
// "onb:cp:<key>:<region>:<page>".
func (b *Bot) onbCountryPress(ctx context.Context, chatID int64, msgID int, loc string, app *OnbApplication, parts []string) {
	if len(parts) < 4 {
		return
	}
	key, region, page := parts[2], parts[3], 1
	if parts[1] == "cp" {
		if len(parts) < 5 {
			return
		}
		page = ParsePage(parts[4])
	}
	form, err := b.onbForm(ctx, loc)
	if err != nil {
		return
	}
	f, ok := form.Schema.Field(key)
	if !ok || f.Type != "country" {
		return
	}
	var edit *int
	if parts[1] == "cp" {
		edit = &msgID
	}
	b.onbCountryAsk(ctx, chatID, edit, loc, b.onbHead(loc, app, f), key, region, page, nil)
}

// onbCountryTyped takes a typed country: a two-letter code of the world list
// is the answer; any other text offers the countries whose name matches.
func (b *Bot) onbCountryTyped(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, f OnbSchemaField, text string) {
	loc := b.onbLocale(from, d, app)
	list, err := b.arena.WorldCountries(ctx, "", loc)
	if err != nil {
		b.logger.Error("eventbot: world countries failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) == 2 {
		if c, ok := list.ByISO2(text); ok {
			b.onbSave(ctx, chatID, from, d, app, f.Key, c.ISO2)
			return
		}
	}
	matches := matchCountries(list.Countries, text, maxCountryMatches)
	if len(matches) == 0 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.geo.no_match", map[string]any{"Name": Esc(truncate(text, 40))}), nil)
		b.onbCountryAsk(ctx, chatID, nil, loc, b.onbHead(loc, app, f), f.Key, "", 1, nil)
		return
	}
	b.onbCountryAsk(ctx, chatID, nil, loc, b.onbHead(loc, app, f), f.Key, "", 1, isoCodes(matches))
}
