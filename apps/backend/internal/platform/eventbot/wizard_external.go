package eventbot

// wizard_external.go — "Who sells the tickets?" (spec 63, L-3). The first
// question of a new event: the organization itself, on the site and in the
// widget, or a partner box office. For a partner the wizard asks the box
// office address, its name and an optional "from" price, skips every ticket
// question and sends the block as actionEvent.externalSale of each date's
// bundle. The price is in the event's currency (the currency question stays).
// An edited event whose dates a partner sells shows the same block on its card
// and changes it through the same three questions.

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/extsale"
)

// maxExternalPriceMinor is the largest "from" price the API accepts (1e9
// major units) in the hundredths the wizard parses.
const maxExternalPriceMinor = int64(1_000_000_000) * 100

// DraftExternal is the partner box office of a date: where the tickets are
// sold, who sells them and the optional "from" price shown on the storefront.
type DraftExternal struct {
	URL            string `json:"url"`
	SellerName     string `json:"seller_name"`
	PriceFromMinor *int64 `json:"price_from_minor,omitempty"`
}

func (e *DraftExternal) clone() *DraftExternal {
	if e == nil {
		return nil
	}
	c := *e
	if e.PriceFromMinor != nil {
		p := *e.PriceFromMinor
		c.PriceFromMinor = &p
	}
	return &c
}

func extEqual(a, b *DraftExternal) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.URL != b.URL || a.SellerName != b.SellerName {
		return false
	}
	if a.PriceFromMinor == nil || b.PriceFromMinor == nil {
		return a.PriceFromMinor == b.PriceFromMinor
	}
	return *a.PriceFromMinor == *b.PriceFromMinor
}

// draftExternalFromAPI reads the partner block of a loaded session.
func draftExternalFromAPI(x *openapi.SessionExternalSale) *DraftExternal {
	if x == nil {
		return nil
	}
	e := &DraftExternal{URL: x.Url, SellerName: x.SellerName}
	if x.PriceFrom != nil && x.Currency != nil {
		p := int64(math.Round(float64(*x.PriceFrom) * 100))
		e.PriceFromMinor = &p
	}
	return e
}

// extOf is the partner box office that sells date i of the draft: its own (an
// existing date read back) or the draft's (a date the draft adds); nil when
// the platform sells the date.
func (d *Draft) extOf(i int) *DraftExternal {
	if i >= 0 && i < len(d.Sessions) {
		if e := d.Sessions[i].External; e != nil {
			return e
		}
		if d.Sessions[i].SessionID != "" {
			return nil
		}
	}
	return d.External
}

// anyExternal reports whether a partner sells at least one date of the draft.
func (d *Draft) anyExternal() bool {
	if d.External != nil {
		return true
	}
	for _, s := range d.Sessions {
		if s.External != nil {
			return true
		}
	}
	return false
}

// externalWire is the actionEvent.externalSale of a bundle. The price needs a
// currency (the wire takes them together): without one it is left out.
func externalWire(e *DraftExternal, currency string) json.RawMessage {
	w := bil24compat.ImportExternalSale{URL: e.URL, SellerName: e.SellerName}
	if e.PriceFromMinor != nil && currency != "" {
		p := money.Major(*e.PriceFromMinor)
		w.PriceFrom, w.Currency = &p, currency
	}
	raw, _ := json.Marshal(w) // a struct of strings and one number cannot fail
	return raw
}

// openExternalEdit opens the partner questions from the card of an existing
// event or from the summary: the loaded block is the starting point and is
// replaced only by the last answer.
func (d *Draft) openExternalEdit() {
	if d.Mode == ModeEdit {
		d.startEdit("external", stExtURL)
		d.Scratch.Ext = d.External.clone()
		d.Scratch.ReturnToSummary = true
		return
	}
	d.History = nil
	d.Scratch.ReturnToSummary = true
	d.Step = stExtURL
}

// extEntry is the block the partner questions fill: a working copy for an
// edited event, the draft's own for a new one.
func (d *Draft) extEntry() *DraftExternal {
	if d.Mode == ModeEdit {
		if d.Scratch.Ext == nil {
			d.Scratch.Ext = &DraftExternal{}
		}
		return d.Scratch.Ext
	}
	if d.External == nil {
		d.External = &DraftExternal{}
	}
	return d.External
}

// extDone ends the partner questions: a new event goes on to its name (or back
// to the summary), an edited one writes the block to the dates a partner sells.
func (d *Draft) extDone() {
	if d.Mode == ModeEdit {
		d.External = d.Scratch.Ext.clone()
		for i := range d.Sessions {
			s := &d.Sessions[i]
			if s.External == nil {
				continue
			}
			s.External = d.External.clone()
			s.Dirty = true
			if i < len(d.Saved.Sessions) {
				d.Saved.Sessions[i].Done = false
			}
		}
		d.goHome()
		return
	}
	if d.Scratch.ReturnToSummary {
		d.goHome()
		return
	}
	d.goTo(stEvName)
}

// applyExternal handles the four questions of the partner block.
func (w *Wizard) applyExternal(ws WizSession, d *Draft, text, data string) (string, error) {
	t := func(key string, args map[string]any) string { return w.texts.T(ws.Locale, key, args) }
	switch d.Step {
	case stEvSeller:
		switch data {
		case "seller:platform":
			d.External = nil
			d.goTo(stEvName)
		case "seller:partner":
			if d.External == nil {
				d.External = &DraftExternal{}
			}
			d.goTo(stExtURL)
		default:
			if text != "" {
				return d.reject("err_seller_first", t("bot.wz.err_seller_first", nil)), nil
			}
		}

	case stExtURL:
		u, ferr := extsale.ValidateURL(text)
		if ferr != nil {
			return d.reject("err_ext_url", t("bot.wz.err_ext_url", nil)), nil
		}
		d.extEntry().URL = u
		d.goTo(stExtName)

	case stExtName:
		name, ferr := extsale.ValidateSellerName(text)
		if ferr != nil {
			return d.reject("err_ext_name", t("bot.wz.err_ext_name", nil)), nil
		}
		d.extEntry().SellerName = name
		d.goTo(stExtPrice)

	case stExtPrice:
		e := d.extEntry()
		if data == "skip" {
			e.PriceFromMinor = nil
		} else {
			p, ok := ParsePriceMinor(text)
			if !ok || p > maxExternalPriceMinor {
				return d.reject("err_ext_price", t("bot.wz.err_ext_price", nil)), nil
			}
			e.PriceFromMinor = &p
		}
		d.extDone()
	}
	return "", nil
}

// renderExternal draws the four questions of the partner block.
func (w *Wizard) renderExternal(ws WizSession, d *Draft, header func(int, string) string, nav func(...[]Button) [][]Button) Screen {
	loc := ws.Locale
	t := func(key string, args map[string]any) string { return w.texts.T(loc, key, args) }
	btn := func(key, data string) Button { return Button{Label: t(key, nil), Data: data} }
	head := header(1, "bot.wz.title_event")
	cur := d.extCurrent()
	switch d.Step {
	case stExtURL:
		return Screen{Text: head + t("bot.wz.ask_ext_url", map[string]any{"Value": Esc(cur.URL)}), Buttons: nav()}
	case stExtName:
		return Screen{Text: head + t("bot.wz.ask_ext_name", map[string]any{"Value": Esc(cur.SellerName)}), Buttons: nav()}
	case stExtPrice:
		price := ""
		if cur.PriceFromMinor != nil {
			price = FormatMoney(*cur.PriceFromMinor, d.currencyOrGuess(), loc)
		}
		return Screen{Text: head + t("bot.wz.ask_ext_price", map[string]any{"Value": price}),
			Buttons: nav([]Button{btn("bot.wz.skip_btn", "skip")})}
	}
	return Screen{Text: head + t("bot.wz.ask_seller", nil),
		Buttons: nav([]Button{btn("bot.wz.seller_platform_btn", "seller:platform")}, []Button{btn("bot.wz.seller_partner_btn", "seller:partner")})}
}

// extCurrent is the block the questions show as "now": the one being entered,
// or an empty one.
func (d *Draft) extCurrent() DraftExternal {
	switch {
	case d.Mode == ModeEdit && d.Scratch.Ext != nil:
		return *d.Scratch.Ext
	case d.External != nil:
		return *d.External
	}
	return DraftExternal{}
}

// externalText is the partner block as a sentence: "Sold by a partner: …".
func (w *Wizard) externalText(loc, cur string, e *DraftExternal) string {
	price := ""
	if e.PriceFromMinor != nil {
		price = w.texts.T(loc, "bot.wz.summary_external_price", map[string]any{"Price": FormatMoney(*e.PriceFromMinor, cur, loc)})
	}
	return w.texts.T(loc, "bot.wz.summary_external", map[string]any{"Name": Esc(e.SellerName), "Url": Esc(e.URL), "Price": price})
}

// dateSalesText says what date i sells and who sells it: the partner block, or
// the categories.
func (w *Wizard) dateSalesText(loc string, d *Draft, i int) string {
	if e := d.extOf(i); e != nil {
		return w.externalText(loc, d.currencyOrGuess(), e)
	}
	return w.ticketsText(loc, d.currencyOrGuess(), d.ticketsOf(i))
}

// salesLines is the "how it is sold" part of the summary when a partner sells
// at least one date: once when every date reads the same, else date by date.
func (w *Wizard) salesLines(loc string, d *Draft) string {
	first := w.dateSalesText(loc, d, 0)
	same := true
	for i := 1; i < len(d.Sessions); i++ {
		if w.dateSalesText(loc, d, i) != first {
			same = false
			break
		}
	}
	if same {
		return first
	}
	lines := []string{w.texts.T(loc, "bot.wz.summary_tickets_by_date", nil)}
	for i, s := range d.Sessions {
		lines = append(lines, "• "+dateLabel(s)+": "+w.dateSalesText(loc, d, i))
	}
	return strings.Join(lines, "\n")
}

// externalChanged reports whether the partner block of an existing date differs
// from the one that was loaded.
func (d *Draft) externalChanged() bool {
	if d.Mode != ModeEdit || d.Loaded == nil {
		return false
	}
	for _, s := range d.Sessions {
		if s.SessionID == "" || s.External == nil {
			continue
		}
		if l, ok := d.Loaded.Dates[s.SessionID]; ok && !extEqual(s.External, l.External) {
			return true
		}
	}
	return false
}

// extDiffers reports whether existing date i carries a partner block other than
// the loaded one (so "back to what was loaded" must not clear its mark).
func (d *Draft) extDiffers(i int) bool {
	if i < 0 || i >= len(d.Sessions) || d.Loaded == nil {
		return false
	}
	s := d.Sessions[i]
	l, ok := d.Loaded.Dates[s.SessionID]
	return ok && !extEqual(s.External, l.External)
}

// undoExternal puts the partner block back to the loaded one.
func (d *Draft) undoExternal() {
	d.External = nil
	for i := range d.Sessions {
		s := &d.Sessions[i]
		l, ok := d.Loaded.Dates[s.SessionID]
		if s.SessionID == "" || !ok || l.External == nil {
			continue
		}
		s.External, s.Dirty = l.External.clone(), false
		if d.External == nil {
			d.External = l.External.clone()
		}
	}
}

// firstLoadedExternal is the partner block the first existing external date had
// when the event was loaded.
func firstLoadedExternal(d *Draft) *DraftExternal {
	if d.Loaded == nil {
		return nil
	}
	for _, s := range d.Sessions {
		if l, ok := d.Loaded.Dates[s.SessionID]; ok && s.SessionID != "" && l.External != nil {
			return l.External
		}
	}
	return nil
}

// externalLabel names a partner block on a card line: "Eventim, https://…".
func externalLabel(e *DraftExternal) string {
	if e == nil {
		return ""
	}
	return Esc(e.SellerName) + ", " + Esc(e.URL)
}
