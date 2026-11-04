package eventbot

import (
	"strconv"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// maxReferralLines caps the cashback lines of one order card: a referrer
// with many friends gets the first ones and "and N more".
const maxReferralLines = 5

// referralLines is the order card's "Refer a friend" part (spec 52): the
// cashback this order earned as a referrer's order (paid with its date,
// on its way, or not paid; each with the friend's order number) and the
// referrer's code a friend's order was bought with. "" for an order with no
// part in the programme.
func (b *Bot) referralLines(loc string, r *openapi.OrderReferral, tz string) string {
	if r == nil {
		return ""
	}
	out := ""
	for i, c := range r.Cashback {
		if i >= maxReferralLines {
			out += b.texts.T(loc, "bot.ord.ref_more", map[string]any{"N": len(r.Cashback) - i})
			break
		}
		args := map[string]any{
			"Amount": FormatMoney(c.Amount, c.Currency, loc),
			"Friend": orderNumberText(c.FriendOrderNumber),
		}
		switch c.State {
		case "paid":
			when := ""
			if c.PaidAt != nil {
				when = Esc(FormatWhen(*c.PaidAt, tz))
			}
			args["When"] = when
			out += b.texts.T(loc, "bot.ord.ref_paid", args)
		case "due", "sent":
			out += b.texts.T(loc, "bot.ord.ref_pending", args)
		default:
			out += b.texts.T(loc, "bot.ord.ref_unpaid", args)
		}
	}
	if u := r.UsedCode; u != nil {
		referrer := ""
		if u.ReferrerOrderNumber != nil {
			referrer = orderNumberText(u.ReferrerOrderNumber)
		}
		out += b.texts.T(loc, "bot.ord.ref_code", map[string]any{"Code": Esc(u.Code), "Referrer": referrer})
	}
	return out
}

// orderNumberText is "№<number>", or "—" when the number is unknown.
func orderNumberText(n *int64) string {
	if n == nil {
		return "—"
	}
	return "№" + strconv.FormatInt(*n, 10)
}
