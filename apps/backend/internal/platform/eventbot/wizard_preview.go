package eventbot

// wizard_preview.go — two summaries of an edited event (U-5b, 2026-10-15):
// the new date's own summary right after its categories ("Set its own
// categories / Change the places / Remove this date / Next"), and the preview
// "Publish changes" shows before anything is sent: what goes to the site, date
// by date, as the save will post it (sendPlan), and a warning for every
// category the import is going to close — a renamed category is a new one,
// and the old one is closed (syncImportedCategoryQuotas,
// import.tier_not_in_payload).

import "strings"

// confirmDateScreen sums up a date added to an edited event.
func (w *Wizard) confirmDateScreen(loc string, d *Draft, head string, nav func(rows ...[]Button) [][]Button) Screen {
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	if d.Cur < 0 || d.Cur >= len(d.Sessions) {
		return Screen{Text: head, Buttons: nav()}
	}
	s := d.Sessions[d.Cur]
	tk := d.ticketsOf(d.Cur)
	list := w.ticketsText(loc, d.currencyOrGuess(), tk)
	if s.CopiedFrom != "" {
		list = t("bot.wz.s_confirm_same", map[string]any{"Prev": s.CopiedFrom}) + " " + list
	}
	text := head + t("bot.wz.s_confirm", map[string]any{
		"N": d.Cur + 1, "When": dateLabel(s), "Venue": Esc(s.VenueName), "City": Esc(s.CityName), "Capacity": s.Capacity, "List": list,
	})
	rows := [][]Button{{{Label: t("bot.wz.s_confirm_own_btn", nil), Data: "sc:own"}}}
	if tk.Mode != ModeParallel {
		// Categories sold at once have places of their own: their sum is the
		// date's places, changed through the categories.
		rows = append(rows, []Button{{Label: t("bot.wz.s_confirm_cap_btn", nil), Data: "sc:cap"}})
	}
	if d.canRemoveDate(d.Cur) {
		rows = append(rows, []Button{{Label: t("bot.wz.s_confirm_drop_btn", nil), Data: "sc:drop"}})
	}
	rows = append(rows, []Button{{Label: t("bot.wz.s_confirm_next_btn", nil), Data: "sc:next"}})
	return Screen{Text: clipMessage(text, maxMessageRunes), Buttons: nav(rows...)}
}

// previewScreen is what "Publish changes" shows first: what the save will send
// and what it leaves alone. Only its "Publish" button saves.
func (w *Wizard) previewScreen(loc, orgName string, d *Draft) Screen {
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	cur := d.currencyOrGuess()
	plan := d.sendPlan()
	currency := contains(d.Changed, "currency")
	var lines, warns, same, already []string
	for _, c := range d.changes() {
		if c.Field != "dates" && c.Field != "tickets" {
			lines = append(lines, "• "+w.changeLine(loc, orgName, d, c))
		}
	}
	for i, s := range d.Sessions {
		label := dateLabel(s)
		if i < len(d.Saved.Sessions) && d.Saved.Sessions[i].Done {
			already = append(already, label)
			continue
		}
		tk := d.ticketsOf(i)
		if i >= len(plan) || !plan[i] {
			if currency && s.SessionID != "" && !hasCategories(*tk) {
				warns = append(warns, t("bot.wz.preview_no_categories", map[string]any{"When": label}))
			}
			same = append(same, label)
			continue
		}
		switch {
		case s.SessionID == "":
			lines = append(lines, "• "+t("bot.wz.preview_new_date", map[string]any{
				"When": label, "Venue": Esc(s.VenueName), "Capacity": s.Capacity, "List": w.ticketsText(loc, cur, tk),
			}))
		case s.Dirty:
			lines = append(lines, "• "+t("bot.wz.preview_date_tickets", map[string]any{"When": label, "List": w.ticketsText(loc, cur, tk)}))
			warns = append(warns, w.closedCategories(loc, d, i)...)
		default:
			lines = append(lines, "• "+t("bot.wz.preview_date_resent", map[string]any{"When": label}))
		}
	}
	text := t("bot.wz.preview_title", nil) + strings.Join(lines, "\n")
	if len(same) > 0 {
		text += "\n• " + t("bot.wz.preview_unchanged", map[string]any{"List": strings.Join(same, ", ")})
	}
	if len(already) > 0 {
		text += "\n• " + t("bot.wz.preview_already", map[string]any{"List": strings.Join(already, ", ")})
	}
	for _, wn := range warns {
		text += "\n\n⚠ " + wn
	}
	text += "\n\n" + t("bot.wz.preview_footer", nil)
	return Screen{Text: clipMessage(text, maxMessageRunes), Buttons: [][]Button{
		{{Label: t("bot.wz.preview_publish_btn", nil), Data: "publish:go"}},
		{{Label: t("bot.wz.preview_back_btn", nil), Data: "e:home"}},
	}}
}

// closedCategories warns about the categories of an existing date that the
// save is going to close: a loaded category whose id the date no longer
// carries. When new categories come in their place (a rename is one of
// these), the warning names them too.
func (w *Wizard) closedCategories(loc string, d *Draft, i int) []string {
	if d.Loaded == nil || i < 0 || i >= len(d.Sessions) {
		return nil
	}
	loaded, ok := d.Loaded.Dates[d.Sessions[i].SessionID]
	if !ok {
		return nil
	}
	now := d.ticketsOf(i)
	kept := map[string]bool{}
	var created []string
	for _, c := range now.Categories {
		if c.TierID == "" {
			created = append(created, quoted(loc, c.Name))
			continue
		}
		kept[c.TierID] = true
	}
	var closed []string
	for _, c := range loaded.Tickets.Categories {
		if c.TierID != "" && !kept[c.TierID] {
			closed = append(closed, quoted(loc, c.Name))
		}
	}
	if len(closed) == 0 {
		return nil
	}
	data := map[string]any{"When": dateLabel(d.Sessions[i]), "Old": strings.Join(closed, ", "), "New": strings.Join(created, ", ")}
	key := "bot.wz.preview_warn_closed"
	if len(created) > 0 {
		key = "bot.wz.preview_warn_renamed"
	}
	if len(closed) > 1 || len(created) > 1 {
		key += "_many"
	}
	text := w.texts.T(loc, key, data)
	if len(created) > 0 && now.Mode == ModeParallel {
		// A closed category keeps its places (it is closed, never deleted),
		// and a date sold "at once" gets the new categories' places on top.
		text += " " + w.texts.T(loc, "bot.wz.preview_warn_places", nil)
	}
	return []string{text}
}

// quoted puts a name in the quotation marks of the language.
func quoted(loc, name string) string {
	if loc == "en" {
		return "“" + Esc(name) + "”"
	}
	return "«" + Esc(name) + "»"
}
