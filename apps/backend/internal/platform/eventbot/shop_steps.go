package eventbot

// shop_steps.go — the delivery steps of a shop order in the bot (spec 61
// section 7b): the step and the last lines of the history on the card, the
// buttons of the next steps, the reason picker of "not delivered" and the
// typed text of the reason "other".
//
// The server owns the rules: every move goes through
// POST .../shop/orders/{id}/fulfillment (the same writer the event center and
// the delivery group use, X-Client-Channel: telegram_bot), and the buttons
// here only mirror its transition table so a person is not offered a step the
// server would refuse. "Handed to courier" is the old "Shipped": the same
// press (sh:sh) asks for the tracking number and the same call stores it.
//
// Presses (callback data stays under 64 bytes: sh:ps:<code>:<order uuid>):
//
//	ps:p  packed        ps:d  delivered       ps:f  open the reason picker
//	fr:n/h/a/r:<order>  a reason of the picker (no answer, not home, wrong
//	                    address, refused)     fr:o:<order>  "other": asks the text

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The delivery steps, as the API reports them.
const (
	stepWaiting    = "waiting"
	stepPacked     = "packed"
	stepHandedOver = "handed_over"
	stepDelivered  = "delivered"
	stepFailed     = "failed"

	// shopHistoryLines is how many of the latest steps a card lists.
	shopHistoryLines = 3
	// shopReasonMax is the server's limit for the reason of a failed delivery.
	shopReasonMax = 500
)

// shopNextSteps is the server's transition table (shop.NextSteps).
var shopNextSteps = map[string][]string{
	stepWaiting:    {stepPacked, stepHandedOver, stepFailed},
	stepPacked:     {stepHandedOver, stepFailed},
	stepHandedOver: {stepDelivered, stepFailed},
	stepFailed:     {stepHandedOver},
}

// shopReasonCodes are the reasons of the picker, by their one-letter callback
// code. "other" has no API code: the person's own text is the reason.
var shopReasonCodes = []struct{ code, reason string }{
	{"n", "no_answer"}, {"h", "not_home"}, {"a", "wrong_address"}, {"r", "refused"},
}

func shopReasonFromCode(c string) (string, bool) {
	for _, r := range shopReasonCodes {
		if r.code == c {
			return r.reason, true
		}
	}
	return "", false
}

// shopStep is the delivery step of an order as a person reads it: the one
// the server sent, else (an order from before steps) handed_over for a shipped
// order and waiting for a paid one. "" when there is nothing to hand over or
// the order is not paid.
func shopStep(o openapi.ShopOrder) string {
	if !shopHasDelivery(o) {
		return ""
	}
	if o.FulfillmentStep != nil && *o.FulfillmentStep != "" {
		return *o.FulfillmentStep
	}
	if o.ShippedAt != nil || o.Status == shStatusShipped {
		return stepHandedOver
	}
	switch o.Status {
	case shStatusPaid, shStatusPartRefund:
		return stepWaiting
	}
	return ""
}

// shopStepOffered reports whether the card may offer a step: the order is
// paid (or shipped, or partly refunded: a refund never changes the step) and
// the step follows the current one. An order in review is judged by
// shopCanShip: it can only be handed over.
func shopStepOffered(o openapi.ShopOrder, step string) bool {
	switch o.Status {
	case shStatusPaid, shStatusShipped, shStatusPartRefund:
	default:
		return false
	}
	cur := shopStep(o)
	for _, s := range shopNextSteps[cur] {
		if s == step {
			return true
		}
	}
	return false
}

// shopStepWord is the word of a step in the person's language.
func (b *Bot) shopStepWord(loc, step string) string {
	switch step {
	case stepWaiting, stepPacked, stepHandedOver, stepDelivered, stepFailed:
		return b.texts.T(loc, "bot.sh.step_"+step, nil)
	}
	return Esc(step)
}

// shopReasonWord is a reason as a person reads it: the word of a code, else
// the text somebody wrote (escaped).
func (b *Bot) shopReasonWord(loc, reason string) string {
	for _, r := range shopReasonCodes {
		if r.reason == reason {
			return b.texts.T(loc, "bot.sh.reason_"+reason, nil)
		}
	}
	return Esc(reason)
}

// shopStepBlock is the step of the card and its latest history lines: who set
// each step, when, the tracking number and the reason. "" for an order with no
// step.
func (b *Bot) shopStepBlock(loc string, o openapi.ShopOrder, tz string) string {
	cur := shopStep(o)
	if cur == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.sh.card_step", map[string]any{"Step": b.shopStepWord(loc, cur)}))
	events := []openapi.ShopOrderEvent(nil)
	if o.Events != nil {
		events = *o.Events
	}
	if len(events) > shopHistoryLines {
		events = events[len(events)-shopHistoryLines:]
	}
	for _, e := range events {
		extra := ""
		if t := strings.TrimSpace(e.Tracking); t != "" {
			extra += " · " + b.texts.T(loc, "bot.sh.step_track", map[string]any{"Text": Esc(t)})
		}
		if r := strings.TrimSpace(e.Reason); r != "" {
			extra += " · " + b.texts.T(loc, "bot.sh.step_reason", map[string]any{"Text": b.shopReasonWord(loc, r)})
		}
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.step_line", map[string]any{
			"Step": b.shopStepWord(loc, e.Step), "Who": Esc(e.ActorName), "When": Esc(FormatWhen(e.CreatedAt, tz)), "Extra": extra,
		}))
	}
	return sb.String()
}

// shopStepRows are the buttons of the steps a card offers besides "Handed to
// courier" (which is the ship button): packed and delivered side by side, then
// "not delivered". Every one carries the order's id.
func (b *Bot) shopStepRows(loc string, o openapi.ShopOrder) [][]models.InlineKeyboardButton {
	oid := o.Id.String()
	var rows [][]models.InlineKeyboardButton
	var first []models.InlineKeyboardButton
	if shopStepOffered(o, stepPacked) {
		first = append(first, btn(b.texts.T(loc, "bot.sh.packed_btn", nil), "sh:ps:p:"+oid))
	}
	if shopStepOffered(o, stepDelivered) {
		first = append(first, btn(b.texts.T(loc, "bot.sh.delivered_btn", nil), "sh:ps:d:"+oid))
	}
	if len(first) > 0 {
		rows = append(rows, first)
	}
	if shopStepOffered(o, stepFailed) {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.failed_btn", nil), "sh:ps:f:"+oid)})
	}
	return rows
}

// shopStepPress handles "sh:ps:<p|d|f>:<order>".
func (b *Bot) shopStepPress(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, code string) {
	switch code {
	case "p":
		b.shopSetStep(ctx, chatID, edit, from, id, jwt, st, orderID, stepPacked, "")
	case "d":
		b.shopSetStep(ctx, chatID, edit, from, id, jwt, st, orderID, stepDelivered, "")
	case "f":
		loc := id.Locale()
		o, err := b.arena.ShopOrder(ctx, jwt, id.Current.OrgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		if !shopStepOffered(o, stepFailed) {
			b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(loc, "bot.sh.err_changed", nil)+"\n\n")
			return
		}
		oidStr := orderID.String()
		var rows [][]models.InlineKeyboardButton
		var pair []models.InlineKeyboardButton
		for _, r := range shopReasonCodes {
			pair = append(pair, btn(b.texts.T(loc, "bot.sh.reason_"+r.reason, nil), "sh:fr:"+r.code+":"+oidStr))
			if len(pair) == 2 {
				rows, pair = append(rows, pair), nil
			}
		}
		if len(pair) > 0 {
			rows = append(rows, pair)
		}
		rows = append(rows,
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.reason_other", nil), "sh:fr:o:"+oidStr)},
			[]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "sh:v:" + oidStr}})
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.failed_ask", map[string]any{"Num": o.Number}), inline(rows...))
	}
}

// shopReasonPress handles "sh:fr:<n|h|a|r|o>:<order>": a reason of the picker
// fails the delivery at once, "other" asks for the text.
func (b *Bot) shopReasonPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, code string) {
	edit := &msgID
	if code == "o" {
		loc := id.Locale()
		o, err := b.arena.ShopOrder(ctx, jwt, id.Current.OrgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		if !shopStepOffered(o, stepFailed) {
			b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(loc, "bot.sh.err_changed", nil)+"\n\n")
			return
		}
		st.CardID, st.MsgID = &orderID, msgID
		b.saveShopState(ctx, from.ID, st, shStepReason)
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.reason_text_ask", map[string]any{"Num": o.Number}), inline(
			[]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "sh:v:" + orderID.String()}}))
		return
	}
	reason, ok := shopReasonFromCode(code)
	if !ok {
		return
	}
	b.shopSetStep(ctx, chatID, edit, from, id, jwt, st, orderID, stepFailed, reason)
}

// shopSetStep moves the order to a step and draws the card from the answer.
// A refusal (the step moved meanwhile, the order was refunded) is told in a
// sentence above the card read afresh.
func (b *Bot) shopSetStep(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, step, reason string) {
	o, err := b.arena.ShopFulfillment(ctx, jwt, id.Current.OrgID, orderID, step, nil, reason)
	if err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	b.shopTrack(ctx, from.ID, id.Current.OrgID, "step_"+step)
	b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(id.Locale(), "bot.sh.step_done", map[string]any{
		"Num": o.Number, "Step": b.shopStepWord(id.Locale(), step),
	})+"\n\n")
}

// shopReasonText takes the typed reason "other" of a failed delivery.
func (b *Bot) shopReasonText(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, text string, toCard *models.InlineKeyboardMarkup) {
	if utf8.RuneCountInString(text) > shopReasonMax {
		b.reply(ctx, chatID, edit, b.texts.T(id.Locale(), "bot.sh.err_reason_long", nil), toCard)
		return
	}
	b.shopSetStep(ctx, chatID, edit, from, id, jwt, st, orderID, stepFailed, text)
}

// ─── the "To ship" tab: waiting and packed together ─────────────────────────

// shopMergeMax bounds what the merged tab reads from each step (the API's page
// limit), so it shows the newest 200 orders of the two steps together. A
// shop with more than that waiting is better served by the browser.
const shopMergeMax = 200

// shopFetchShip loads the page of the "To ship" tab: the orders at the steps
// waiting and packed, newest first. The API filters by ONE step, so the two
// are read apart (as many rows each as the page needs) and merged here by the
// server's own order, created_at then id, both descending. Without a search the
// total comes from the step counts; with one it is unknown and the pager shows
// one page more while another follows.
func (b *Bot) shopFetchShip(ctx context.Context, jwt string, st *shopDialog) (shopPage, error) {
	var p shopPage
	total := int64(-1)
	if st.Query == "" {
		counts, err := b.arena.ShopOrderCounts(ctx, jwt, st.OrgID)
		if err != nil {
			return p, err
		}
		total = counts.Steps.Waiting + counts.Steps.Packed
	}
	maxPages := shopMergeMax / shopPageSize
	for attempt := 0; ; attempt++ {
		if total >= 0 {
			p.Total = total
			p.Pages = PagesFor(total, shopPageSize)
			if p.Pages > maxPages {
				p.Pages = maxPages
			}
			if st.Page > p.Pages {
				st.Page = p.Pages
			}
		}
		need := st.Page * shopPageSize
		if need > shopMergeMax {
			need = shopMergeMax
		}
		var merged []openapi.ShopOrder
		more := false
		for _, step := range []string{stepWaiting, stepPacked} {
			res, err := b.arena.ShopOrders(ctx, jwt, st.OrgID, ShopOrderQuery{Step: step, Q: st.Query, Limit: need})
			if err != nil {
				return p, err
			}
			merged = append(merged, res.Orders...)
			more = more || res.NextOffset != nil
		}
		sort.SliceStable(merged, func(i, j int) bool {
			if !merged[i].CreatedAt.Equal(merged[j].CreatedAt) {
				return merged[i].CreatedAt.After(merged[j].CreatedAt)
			}
			return merged[i].Id.String() > merged[j].Id.String()
		})
		from := (st.Page - 1) * shopPageSize
		if from >= len(merged) && st.Page > 1 && attempt == 0 && total < 0 {
			// A search page past the end: start again from the first.
			st.Page = 1
			continue
		}
		to := from + shopPageSize
		if to > len(merged) {
			to = len(merged)
		}
		if from < to {
			p.Orders = merged[from:to]
		}
		if total < 0 {
			p.Total, p.Pages = -1, st.Page
			if (len(merged) > st.Page*shopPageSize || more) && st.Page < maxPages {
				p.Pages = st.Page + 1
			}
		}
		return p, nil
	}
}
