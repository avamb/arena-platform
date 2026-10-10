package eventbot

// promo_view.go — how a promo code is written on screen (EC-11, spec 35
// §6.2): the list entry, the card, the usage row and the summary of a code
// that is about to be created. Money goes through FormatMoney (minor units);
// the pieces are message keys so every language orders its own sentence. No
// buyer e-mail or phone appears anywhere in this file — the usage list shows
// a buyer's NAME, in the private chat, and nothing else about them.

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// promoDay renders a moment as a calendar day. Codes end at 23:59:59 UTC of
// their last day (untilInstant), so the UTC date is the one the organizer chose.
func promoDay(t time.Time) string {
	// allow:timeformat: a calendar day on an organizer's chat screen, not a wire timestamp
	return t.UTC().Format("02.01.2006")
}

// promoMoney is an amount of minor units with its currency, no stray space
// when the currency is unknown.
func promoMoney(minor int64, currency, loc string) string {
	return strings.TrimSpace(FormatMoney(minor, currency, loc))
}

// promoCurrencyOf is the currency a code's money is in: the fixed discount's
// own, else the one the usage report found; "" when it has none yet or the
// orders are in several (the sum of mixed currencies is no amount).
func promoCurrencyOf(it openapi.PromoCodeItem) string {
	if it.Currency != nil && *it.Currency != "" {
		return *it.Currency
	}
	if it.DiscountCurrency != nil {
		return *it.DiscountCurrency
	}
	return ""
}

func (b *Bot) promoStatusWord(loc, state string) string {
	return b.texts.T(loc, "bot.promo.st_"+state, nil)
}

// promoDiscountText is "15% off" or "5 EUR off the order".
func (b *Bot) promoDiscountText(loc string, it openapi.PromoCodeItem) string {
	if string(it.DiscountType) == promoTypePercent {
		return b.texts.T(loc, "bot.promo.discount_pct", map[string]any{"Value": it.DiscountValue})
	}
	cur := ""
	if it.Currency != nil {
		cur = *it.Currency
	}
	return b.texts.T(loc, "bot.promo.discount_fixed", map[string]any{"Amount": promoMoney(it.DiscountValue, cur, loc)})
}

// promoSessionsText is "all sessions, including future ones" or "N sessions".
func (b *Bot) promoSessionsText(loc string, it openapi.PromoCodeItem) string {
	if promoIsClub(it) {
		return b.texts.T(loc, "bot.promo.sess_all", nil)
	}
	return b.texts.T(loc, "bot.promo.sess_n", map[string]any{"N": len(it.AppliesToSessionIds)})
}

// promoLimitsText is the two usage caps, or "no limits".
func (b *Bot) promoLimitsText(loc string, it openapi.PromoCodeItem) string {
	var parts []string
	if it.MaxUses != nil {
		parts = append(parts, b.texts.T(loc, "bot.promo.lim_total", map[string]any{"Limit": fmt.Sprint(*it.MaxUses)}))
	}
	if it.MaxUsesPerCustomer != nil {
		parts = append(parts, b.texts.T(loc, "bot.promo.lim_per", map[string]any{"Limit": fmt.Sprint(*it.MaxUsesPerCustomer)}))
	}
	if len(parts) == 0 {
		return b.texts.T(loc, "bot.promo.lim_none", nil)
	}
	return strings.Join(parts, ", ")
}

// promoValidText is the validity window in words.
func (b *Bot) promoValidText(loc string, it openapi.PromoCodeItem) string {
	var parts []string
	if it.ValidFrom != nil {
		parts = append(parts, b.texts.T(loc, "bot.promo.valid_from", map[string]any{"Date": promoDay(*it.ValidFrom)}))
	}
	if it.ValidUntil != nil {
		parts = append(parts, b.texts.T(loc, "bot.promo.valid_until", map[string]any{"Date": promoDay(*it.ValidUntil)}))
	}
	if len(parts) == 0 {
		return b.texts.T(loc, "bot.promo.valid_none", nil)
	}
	return strings.Join(parts, " ")
}

// promoUsageText is the number of uses, the discount they took and the last
// one. Several currencies cannot be added up: the line then says to look at
// the usage list instead of printing a meaningless sum.
func (b *Bot) promoUsageText(loc string, it openapi.PromoCodeItem) string {
	if it.Uses == 0 {
		return b.texts.T(loc, "bot.promo.usage_none", nil)
	}
	when := ""
	if it.LastUsedAt != nil {
		when = promoDay(*it.LastUsedAt)
	}
	cur := promoCurrencyOf(it)
	if cur == "" {
		return b.texts.T(loc, "bot.promo.usage_mixed", map[string]any{"Count": it.Uses, "When": when})
	}
	return b.texts.T(loc, "bot.promo.usage", map[string]any{
		"Count": it.Uses, "Discount": promoMoney(it.DiscountTotal, cur, loc), "When": when,
	})
}

// promoEntry is one code in the list: name and state, what it gives and where,
// its limits and dates, and how much it has been used.
func (b *Bot) promoEntry(loc string, n int, it openapi.PromoCodeItem, now time.Time) string {
	state := promoState(it, now)
	return fmt.Sprintf("%d. %s <b>%s</b> · %s\n%s · %s\n%s · %s\n%s",
		n, PromoChip(state), Esc(it.Code), b.promoStatusWord(loc, state),
		b.promoDiscountText(loc, it), b.promoSessionsText(loc, it),
		b.promoLimitsText(loc, it), b.promoValidText(loc, it),
		b.promoUsageText(loc, it))
}

// promoCardLine is a labelled line of the card.
func (b *Bot) promoCardLine(loc, key, text string) string {
	return b.texts.T(loc, key, map[string]any{"Text": text})
}

// promoCardText is the card of one code. sessionLines are the already
// resolved lines naming its sessions (at most a handful, then "and N more").
func (b *Bot) promoCardText(loc string, it openapi.PromoCodeItem, now time.Time, sessionLines []string, more int) string {
	state := promoState(it, now)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s <b>%s</b> · %s", PromoChip(state), Esc(it.Code), b.promoStatusWord(loc, state)))
	sb.WriteString("\n\n" + b.promoCardLine(loc, "bot.promo.card_discount", b.promoDiscountText(loc, it)))
	if string(it.DiscountType) == promoTypeFixed {
		sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_rule", nil))
	}
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_sessions", b.promoSessionsText(loc, it)))
	for _, l := range sessionLines {
		sb.WriteString("\n" + l)
	}
	if more > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_sess_more", map[string]any{"N": more}))
	}
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_limits", b.promoLimitsText(loc, it)))
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_valid", b.promoValidText(loc, it)))
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_usage", b.promoUsageText(loc, it)))
	return sb.String()
}

// promoUsageRow is one use of a code: the order, the day, the buyer's name,
// what the discount took off, and the order's state. No e-mail, no phone.
func (b *Bot) promoUsageRow(loc string, r openapi.PromoRedemptionItem) string {
	cur := ""
	if r.Currency != nil {
		cur = *r.Currency
	}
	num := "—"
	if r.OrderNumber != nil {
		num = fmt.Sprint(*r.OrderNumber)
	}
	name := b.texts.T(loc, "bot.promo.buyer_unknown", nil)
	if r.BuyerName != nil && strings.TrimSpace(*r.BuyerName) != "" {
		name = Esc(truncate(*r.BuyerName, 40))
	}
	status := ""
	if r.OrderStatus != nil {
		chip := OrderChip(*r.OrderStatus)
		if key := orderStatusKey(*r.OrderStatus); key != "" {
			status = chip + " " + b.texts.T(loc, key, nil)
		} else {
			status = chip
		}
	}
	return b.texts.T(loc, "bot.promo.usage_row", map[string]any{
		"Num": num, "Date": promoDay(r.RedeemedAt), "Name": name,
		"Discount": promoMoney(r.DiscountAmount, cur, loc), "Amount": promoMoney(r.OrderAmount, cur, loc),
		"Status": status,
	})
}

// promoSummaryText is the last look at a code before it is created.
func (b *Bot) promoSummaryText(loc string, d *promoDraft) string {
	it := d.item()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>", Esc(d.Code)))
	sb.WriteString("\n\n" + b.promoCardLine(loc, "bot.promo.card_discount", b.promoDiscountText(loc, it)))
	if d.fixed() {
		sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_rule", nil))
		sb.WriteString("\n" + b.texts.T(loc, "bot.promo.only_currency", map[string]any{"Currency": d.Currency}))
	}
	if d.All {
		sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_sessions", b.texts.T(loc, "bot.promo.sess_all", nil)))
	} else {
		picked := d.Pick.checked()
		sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_sessions", b.texts.T(loc, "bot.promo.sess_n", map[string]any{"N": len(picked)})))
		shown := 0
		for i, on := range d.Pick.Sel {
			if !on {
				continue
			}
			if shown >= promoMaxSessionLines {
				sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_sess_more", map[string]any{"N": len(picked) - shown}))
				break
			}
			sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_sess_line", map[string]any{
				"Name": Esc(d.EventName), "When": Esc(d.Pick.Options[i].Label),
			}))
			shown++
		}
	}
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_limits", b.promoLimitsText(loc, it)))
	valid := b.texts.T(loc, "bot.promo.valid_none", nil)
	if d.Until != "" {
		valid = b.texts.T(loc, "bot.promo.valid_until", map[string]any{"Date": DisplayDate(d.Until)})
	}
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_valid", valid))
	sb.WriteString("\n" + b.promoCardLine(loc, "bot.promo.card_state", b.promoStatusWord(loc, d.Status)))
	return sb.String()
}

// promoMaxSessionLines is how many sessions a card or summary names before
// "and N more".
const promoMaxSessionLines = 5

// promoSessionOption is the label of a session in the picker: its start in
// the venue's zone, with the currency when the code is a fixed one (a session
// in another currency is shown but cannot be ticked).
func promoSessionOption(s openapi.SessionItem, tz string) promoOption {
	return promoOption{
		ID: s.Id, Label: FormatWhen(s.StartAt, tz), Currency: s.Currency, Tz: tz,
	}
}

// uuidSet is a set of ids.
func uuidSet(ids []uuid.UUID) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
