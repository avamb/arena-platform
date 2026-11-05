package eventbot

// shop_orders.go — the shop's "Orders" screens (spec 61 section 7.3): the
// tabs, ten rows a page, a free-text search, and the card of one order. The
// state (tab, search, page, the ids of the rows on screen) lives in
// bot_dialogs under kind "shop", so a restart keeps it and the buttons of a
// message already in the chat go on working; a row button carries the row's
// INDEX on the page (paging.go), resolved against the stored ids.
//
// The card shows the buyer's name, phone and e-mail and the delivery address.
// They are written to the private chat of the person who asked and nowhere
// else; nothing here puts a buyer field in a log line or a callback payload.

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Statuses of a shop order, as the API reports them.
const (
	shStatusPending     = "pending_payment"
	shStatusPaid        = "paid"
	shStatusShipped     = "shipped"
	shStatusCancelled   = "cancelled"
	shStatusExpired     = "expired"
	shStatusRefunded    = "refunded"
	shStatusPartRefund  = "partially_refunded"
	shStatusManual      = "manual_review"
	shReviewLate        = "late_payment_no_stock"
	shReviewUnconfirmed = "payment_unconfirmed"
	shDeliveryNone      = "none"
	shDeliveryPickup    = "pickup"
)

// shopChip is the one-glyph state of an order row.
func shopChip(status string) string {
	switch status {
	case shStatusPaid:
		return "✅"
	case shStatusShipped:
		return "📦"
	case shStatusPending:
		return "⏳"
	case shStatusExpired:
		return "⌛"
	case shStatusCancelled:
		return "✖"
	case shStatusRefunded, shStatusPartRefund:
		return "↩"
	case shStatusManual:
		return "⚠"
	}
	return "·"
}

// shopStatusWord is the word of the status; an order that was shipped and
// then refunded says both.
func (b *Bot) shopStatusWord(loc string, o openapi.ShopOrder) string {
	word := Esc(o.Status)
	switch o.Status {
	case shStatusPending, shStatusPaid, shStatusShipped, shStatusCancelled, shStatusExpired,
		shStatusRefunded, shStatusPartRefund, shStatusManual:
		word = b.texts.T(loc, "bot.sh.st_"+o.Status, nil)
	}
	if o.ShippedAt != nil && (o.Status == shStatusRefunded || o.Status == shStatusPartRefund) {
		word = b.texts.T(loc, "bot.sh.st_shipped", nil) + " · " + word
	}
	return word
}

// shopRowLabel is the text of a list row's button:
// "№1000000500 · ✅ · 47 EUR · Buyer Name · 15.10".
func shopRowLabel(o openapi.ShopOrder, loc, tz string) string {
	parts := []string{"№" + strconv.FormatInt(o.Number, 10), shopChip(o.Status), FormatMoney(o.Total, o.Currency, loc)}
	if name := truncate(o.Buyer.Name, 16); name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, shortDay(o.CreatedAt, tz))
	return strings.Join(parts, " · ")
}

// ─── the list ─────────────────────────────────────────────────────────────────

// shopPage is one page of the list with the paging arithmetic.
type shopPage struct {
	Orders []openapi.ShopOrder
	Total  int64 // -1 when unknown (a search)
	Pages  int
}

// shopFetch loads the page the state points at. A tab with a count (to ship,
// unpaid, all) knows its size from /orders/counts; "in review" is read whole
// and paged here; a search has no total, so the pager shows one page more
// while the server says another follows. A page past the end falls back to
// the last one.
func (b *Bot) shopFetch(ctx context.Context, jwt string, st *shopDialog) (shopPage, error) {
	q := ShopOrderQuery{Q: st.Query, Limit: shopPageSize}
	switch st.Tab {
	case shTabShip:
		q.View = "to_ship"
	case shTabUnpaid:
		q.View = "unpaid"
	case shTabReview:
		q.Status = "manual_review"
	}
	for attempt := 0; ; attempt++ {
		var p shopPage
		switch {
		case st.Tab == shTabReview && st.Query == "":
			q.Limit, q.Offset = shopReviewFetch, 0
			res, err := b.arena.ShopOrders(ctx, jwt, st.OrgID, q)
			if err != nil {
				return p, err
			}
			p.Total = int64(len(res.Orders))
			p.Pages = PagesFor(p.Total, shopPageSize)
			if st.Page > p.Pages {
				st.Page = p.Pages
			}
			from := (st.Page - 1) * shopPageSize
			to := from + shopPageSize
			if to > len(res.Orders) {
				to = len(res.Orders)
			}
			if from < to {
				p.Orders = res.Orders[from:to]
			}
			return p, nil
		case st.Query != "":
			q.Offset = (st.Page - 1) * shopPageSize
			res, err := b.arena.ShopOrders(ctx, jwt, st.OrgID, q)
			if err != nil {
				return p, err
			}
			p.Orders, p.Total, p.Pages = res.Orders, -1, st.Page
			if res.NextOffset != nil {
				p.Pages = st.Page + 1
			}
			if len(p.Orders) == 0 && st.Page > 1 && attempt == 0 {
				st.Page = 1
				continue
			}
			return p, nil
		}
		counts, err := b.arena.ShopOrderCounts(ctx, jwt, st.OrgID)
		if err != nil {
			return p, err
		}
		switch st.Tab {
		case shTabShip:
			p.Total = counts.ToShip
		case shTabUnpaid:
			p.Total = counts.Unpaid
		default:
			p.Total = counts.All
		}
		p.Pages = PagesFor(p.Total, shopPageSize)
		if st.Page > p.Pages {
			st.Page = p.Pages
		}
		q.Offset = (st.Page - 1) * shopPageSize
		res, err := b.arena.ShopOrders(ctx, jwt, st.OrgID, q)
		if err != nil {
			return p, err
		}
		p.Orders = res.Orders
		return p, nil
	}
}

// renderShopOrders draws the list for the state and stores the state, with
// the ids of the rows it shows, before answering.
func (b *Bot) renderShopOrders(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st shopDialog, note string) {
	loc := id.Locale()
	p, err := b.shopFetch(ctx, jwt, &st)
	if err != nil {
		b.shopError(ctx, chatID, editMsgID, id, err, "sh:home")
		return
	}
	st.IDs = st.IDs[:0]
	for _, o := range p.Orders {
		st.IDs = append(st.IDs, o.Id)
	}
	st.CardID = nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveShopState(ctx, id.Link.TelegramUserID, st, shStepList)

	search := ""
	if st.Query != "" {
		search = "\n" + b.texts.T(loc, "bot.sh.search_line", map[string]any{"Query": Esc(st.Query)})
	}
	total := ""
	if p.Total >= 0 {
		total = b.texts.T(loc, "bot.sh.list_total", map[string]any{"Total": p.Total})
	}
	text := note + b.texts.T(loc, "bot.sh.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Tab": b.texts.T(loc, "bot.sh.tab_"+st.Tab, nil),
		"Total": total, "Page": st.Page, "Pages": p.Pages, "Search": search,
	})
	if len(p.Orders) == 0 {
		if st.Query != "" {
			text += "\n\n" + b.texts.T(loc, "bot.sh.search_none", map[string]any{"Query": Esc(st.Query)})
		} else {
			text += "\n\n" + b.texts.T(loc, "bot.sh.empty_"+st.Tab, nil)
		}
	} else {
		text += "\n\n" + b.texts.T(loc, "bot.sh.list_hint", nil)
	}

	mark := func(tab string) string {
		label := b.texts.T(loc, "bot.sh.tab_"+tab, nil)
		if st.Tab == tab {
			return "✔ " + label
		}
		return label
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(mark(shTabShip), "sh:t:s"), btn(mark(shTabReview), "sh:t:r")},
		{btn(mark(shTabUnpaid), "sh:t:u"), btn(mark(shTabAll), "sh:t:a")},
	}
	for i, o := range p.Orders {
		rows = append(rows, []models.InlineKeyboardButton{btn(shopRowLabel(o, loc, st.TZ), ItemCallback("sh:o", i))})
	}
	if nav := PagerRow(Pager{Prefix: "sh:p", Page: st.Page, Pages: p.Pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		rows = append(rows, nav)
	}
	if st.Query != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.ec.clear_search_btn", nil), "sh:x")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.back_shop_btn", nil), "sh:home")})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), inline(rows...))
}

// ─── presses of the orders screens ───────────────────────────────────────────

// shopOrdersCallback handles the list presses and the actions of a card.
func (b *Bot) shopOrdersCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	tg := from.ID

	switch kind {
	case "ol":
		prev, _, _ := b.loadShopState(ctx, tg, orgID)
		tz := prev.TZ
		if tz == "" && shopMode(id) == shopModeFull {
			if s, err := b.arena.ShopSettings(ctx, jwt, orgID); err == nil {
				tz = shopZone(s.CountryIso2)
			}
		}
		b.shopTrack(ctx, tg, orgID, "orders")
		b.renderShopOrders(ctx, chatID, &msgID, id, jwt, newShopDialog(orgID, tz), "")
		return
	case "v":
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		// The id is in the payload: a card keeps meaning THAT order however
		// long ago the list was drawn.
		st, _, _ := b.loadShopState(ctx, tg, orgID)
		b.showShopCard(ctx, chatID, &msgID, from, id, jwt, st, target, "")
		return
	case "sh", "sn", "cr", "nt", "nx", "rm", "rs", "cx":
		b.shopCardAction(ctx, chatID, msgID, from, id, jwt, kind, arg)
		return
	}

	st, _, expired := b.loadShopState(ctx, tg, orgID)
	if expired {
		// The screen ran out while the person was away: say so once, and
		// start the default list rather than act on a press made on stale rows.
		b.renderShopOrders(ctx, chatID, &msgID, id, jwt, newShopDialog(orgID, st.TZ), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	switch kind {
	case "b":
	case "t":
		tab, ok := shopTabFromCode(arg)
		if !ok {
			return
		}
		st.Tab, st.Page = tab, 1
	case "p":
		st.Page = ParsePage(arg)
	case "x":
		st.Query, st.Page = "", 1
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show
			// the list as it is now instead of opening the wrong order.
			b.renderShopOrders(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.showShopCard(ctx, chatID, &msgID, from, id, jwt, st, st.IDs[i], "")
		return
	default:
		return
	}
	b.renderShopOrders(ctx, chatID, &msgID, id, jwt, st, "")
}

// ─── the card ─────────────────────────────────────────────────────────────────

// showShopCard loads one order and draws its card. note is a line above it.
func (b *Bot) showShopCard(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, note string) {
	o, err := b.arena.ShopOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.shopError(ctx, chatID, editMsgID, id, err, "sh:b")
		return
	}
	b.renderShopCard(ctx, chatID, editMsgID, from, id, st, o, note)
}

// renderShopCard draws the card of an order already read.
func (b *Bot) renderShopCard(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, st shopDialog, o openapi.ShopOrder, note string) {
	loc := id.Locale()
	oid := o.Id
	st.OrgID = id.Current.OrgID
	if !validShopTab(st.Tab) {
		st.Tab = shTabShip
	}
	if st.Page < 1 {
		st.Page = 1
	}
	st.CardID = &oid
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveShopState(ctx, from.ID, st, shStepCard)
	private := chatID == from.ID
	text := note + b.shopCardText(loc, o, st.TZ, private)
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.shopCardKeyboard(loc, o))
}

// shopCardText is the card. The buyer's contacts and address are left out of
// a group chat.
func (b *Bot) shopCardText(loc string, o openapi.ShopOrder, tz string, private bool) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.sh.card_head", map[string]any{
		"Num": o.Number, "Chip": shopChip(o.Status), "Status": b.shopStatusWord(loc, o),
		"Amount": FormatMoney(o.Total, o.Currency, loc), "When": Esc(FormatWhen(o.CreatedAt, tz)),
	}))
	if o.Status == shStatusManual {
		key := "bot.sh.card_review_other"
		switch {
		case o.ReviewReason != nil && *o.ReviewReason == shReviewLate:
			key = "bot.sh.card_review_late"
		case o.ReviewReason != nil && *o.ReviewReason == shReviewUnconfirmed:
			key = "bot.sh.card_review_unconfirmed"
		}
		sb.WriteString("\n\n" + b.texts.T(loc, key, nil))
	}

	if private {
		contacts := ""
		if p := strings.TrimSpace(deref(o.Buyer.Phone)); p != "" {
			contacts += "\n📞 " + Esc(p)
		}
		if e := strings.TrimSpace(o.Buyer.Email); e != "" {
			contacts += "\n✉️ " + Esc(e)
		}
		sb.WriteString(b.texts.T(loc, "bot.sh.card_buyer", map[string]any{"Name": Esc(o.Buyer.Name), "Contacts": contacts}))
	} else {
		sb.WriteString(b.texts.T(loc, "bot.sh.card_buyer", map[string]any{"Name": Esc(o.Buyer.Name), "Contacts": ""}))
	}

	sb.WriteString("\n\n" + b.shopDeliveryText(loc, o, private))
	if c := strings.TrimSpace(o.DeliveryNote); c != "" && private {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.card_comment", map[string]any{"Text": Esc(c)}))
	}

	sb.WriteString(b.texts.T(loc, "bot.sh.card_items", nil))
	for _, it := range o.Items {
		name := Esc(it.ProductTitle)
		if v := strings.TrimSpace(it.VariantTitle); v != "" && v != it.ProductTitle {
			name += " — " + Esc(v)
		}
		sb.WriteString("\n• " + name + " × " + strconv.Itoa(int(it.Qty)) + " · " + FormatMoney(it.TotalMinor, o.Currency, loc))
	}
	if o.Discount > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.line_discount", map[string]any{"Amount": FormatMoney(o.Discount, o.Currency, loc)}))
	}
	if o.ShippingFee > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.line_shipping", map[string]any{"Amount": FormatMoney(o.ShippingFee, o.Currency, loc)}))
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.sh.line_total", map[string]any{"Amount": FormatMoney(o.Total, o.Currency, loc)}))
	if o.RefundedMinor > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.line_refunded", map[string]any{"Amount": FormatMoney(o.RefundedMinor, o.Currency, loc)}))
	}
	if o.Payment != nil && o.Payment.Provider != "" {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.card_payment", map[string]any{
			"Provider": Esc(o.Payment.Provider), "State": Esc(o.Payment.State),
		}))
	}
	if o.ShippedAt != nil {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sh.card_shipped", map[string]any{"When": Esc(FormatWhen(*o.ShippedAt, tz))}))
		if t := strings.TrimSpace(o.TrackingNote); t != "" {
			sb.WriteString("\n" + b.texts.T(loc, "bot.sh.card_tracking", map[string]any{"Text": Esc(t)}))
		}
	}
	if n := strings.TrimSpace(o.StaffNote.Text); n != "" {
		when := ""
		if o.StaffNote.At != nil {
			when = FormatWhen(*o.StaffNote.At, tz)
		}
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sh.card_note", map[string]any{
			"By": Esc(o.StaffNote.By), "When": Esc(when), "Text": Esc(n),
		}))
	}
	return sb.String()
}

// shopDeliveryText is the delivery block: the address lines, "Pickup" or
// "No delivery". A group chat gets the kind only.
func (b *Bot) shopDeliveryText(loc string, o openapi.ShopOrder, private bool) string {
	switch o.DeliveryKind {
	case shDeliveryNone:
		return b.texts.T(loc, "bot.sh.delivery_none", nil)
	case shDeliveryPickup:
		return b.texts.T(loc, "bot.sh.delivery_pickup", nil)
	}
	if !private {
		return b.texts.T(loc, "bot.sh.delivery_hidden", nil)
	}
	a := o.Ship
	var lines []string
	add := func(parts ...string) {
		var kept []string
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			lines = append(lines, Esc(strings.Join(kept, ", ")))
		}
	}
	add(deref(a.Name))
	add(a.Address1)
	add(deref(a.Address2))
	add(strings.TrimSpace(deref(a.Postal)+" "+a.City), a.Country)
	if len(lines) == 0 {
		return b.texts.T(loc, "bot.sh.delivery_none", nil)
	}
	return b.texts.T(loc, "bot.sh.card_delivery", nil) + "\n" + strings.Join(lines, "\n")
}

// shopHasDelivery reports an order with something to hand over.
func shopHasDelivery(o openapi.ShopOrder) bool { return o.DeliveryKind != shDeliveryNone }

func shopIsShipped(o openapi.ShopOrder) bool {
	return o.Status == shStatusShipped || o.ShippedAt != nil
}

// shopCanShip: a paid (or partly refunded, or parked-for-goods) order that has
// something to hand over and was not shipped yet.
func shopCanShip(o openapi.ShopOrder) bool {
	if !shopHasDelivery(o) || shopIsShipped(o) {
		return false
	}
	switch o.Status {
	case shStatusPaid, shStatusPartRefund:
		return true
	case shStatusManual:
		// An order whose payment is not confirmed cannot be shipped yet.
		return o.ReviewReason == nil || *o.ReviewReason != shReviewUnconfirmed
	}
	return false
}

// shopCanEditTracking: a shipped order can still get its number changed.
func shopCanEditTracking(o openapi.ShopOrder) bool {
	return o.Status == shStatusShipped || (o.Status == shStatusPartRefund && o.ShippedAt != nil)
}

// shopRefundLeft is what can still go back to the buyer.
func shopRefundLeft(o openapi.ShopOrder) int64 {
	if left := o.Total - o.RefundedMinor; left > 0 {
		return left
	}
	return 0
}

// shopCanRefund: money was taken through a payment provider and some is left.
func shopCanRefund(o openapi.ShopOrder) bool {
	if o.Payment == nil {
		return false
	}
	switch o.Status {
	case shStatusPaid, shStatusShipped, shStatusPartRefund, shStatusManual:
		return shopRefundLeft(o) > 0
	}
	return false
}

func shopCanCancel(o openapi.ShopOrder) bool { return o.Status == shStatusPending }

func shopCanResolve(o openapi.ShopOrder) bool {
	return o.Status == shStatusManual && o.ReviewReason != nil && *o.ReviewReason == shReviewLate
}

// shopLetters are the buyer letters that exist for the order (the same rule
// as the web event center): the confirmation of a paid order, the shipping
// letter of a shipped one, the refund letter once money went back.
func shopLetters(o openapi.ShopOrder) []string {
	if strings.TrimSpace(o.Buyer.Email) == "" {
		return nil
	}
	switch o.Status {
	case shStatusPaid, shStatusShipped, shStatusPartRefund, shStatusRefunded:
	default:
		return nil
	}
	out := []string{"received"}
	if shopHasDelivery(o) && o.ShippedAt != nil {
		out = append(out, "shipped")
	}
	if o.Status == shStatusPartRefund || o.Status == shStatusRefunded || o.RefundedMinor > 0 {
		out = append(out, "refunded")
	}
	return out
}

// shopLetterCode is the one-letter code of a buyer letter in a callback.
func shopLetterCode(kind string) string {
	switch kind {
	case "shipped":
		return "s"
	case "refunded":
		return "f"
	}
	return "r"
}

func shopLetterFromCode(c string) (string, bool) {
	switch c {
	case "r":
		return "received", true
	case "s":
		return "shipped", true
	case "f":
		return "refunded", true
	}
	return "", false
}

// shopCardKeyboard is the buttons of a card. Every action carries the order's
// id, so a card keeps meaning THAT order whatever else the dialog holds.
func (b *Bot) shopCardKeyboard(loc string, o openapi.ShopOrder) *models.InlineKeyboardMarkup {
	oid := o.Id.String()
	var rows [][]models.InlineKeyboardButton
	switch {
	case shopCanShip(o):
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.ship_btn", nil), "sh:sh:"+oid)})
	case shopCanEditTracking(o):
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.track_btn", nil), "sh:sh:"+oid)})
	}
	var row []models.InlineKeyboardButton
	if shopHasDelivery(o) {
		row = append(row, btn(b.texts.T(loc, "bot.sh.courier_btn", nil), "sh:cr:"+oid))
	}
	row = append(row, btn(b.texts.T(loc, "bot.sh.note_btn", nil), "sh:nt:"+oid))
	rows = append(rows, row)
	if letters := shopLetters(o); len(letters) == 1 {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.resend_btn", nil), "sh:rs:"+shopLetterCode(letters[0])+":"+oid)})
	} else if len(letters) > 1 {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.resend_btn", nil), "sh:rm:"+oid)})
	}
	if shopCanRefund(o) {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_btn", nil), "sh:f:s:"+oid)})
	}
	if shopCanResolve(o) {
		rows = append(rows,
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.fulfil_btn", nil), "sh:rv:f:"+oid)},
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_cancel_btn", nil), "sh:rv:r:"+oid)})
	}
	if shopCanCancel(o) {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.cancel_btn", nil), "sh:cx:"+oid)})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.web.btn", nil), "sh:w:o:"+oid)},
		[]models.InlineKeyboardButton{btn("« "+b.texts.T(loc, "bot.sh.to_list_btn", nil), "sh:b"), btn(b.texts.T(loc, "bot.sh.back_shop_btn", nil), "sh:home")})
	return inline(rows...)
}
