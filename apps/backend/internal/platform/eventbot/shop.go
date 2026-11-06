package eventbot

// shop.go — the "Shop" section of the bot (spec 61 section 7, stage M3): the
// screen with the storefront switch and the counts, the settings in plain
// words, and the "Open in browser" that lands on the shop. The orders and
// their actions are in shop_orders.go, shop_actions.go and shop_refund.go.
//
// The section exists for an organization that has the shop module, or
// whose module ended but still has orders to finish ("orders only"), and
// only for the roles that read sales (owner, manager, platform operator).
// Every press re-reads /v1/me, so a button of a screen sent before the module
// was switched off answers "the shop is not connected".
//
// Presses (all "sh:<data>"):
//
//	home            the shop screen               ol          the orders, fresh
//	t:<s|r|u|a>     tab                           p:<n>       page
//	x               clear the search              o:<i>       row i of the page
//	v:<order>       the card of an order          b           back to the list
//	sh / sn         hand to the courier (asks for the number) / without a number
//	ps:<p|d|f>      packed / delivered / not delivered (shop_steps.go)
//	fr:<n|h|a|r|o>  a reason of "not delivered" (shop_steps.go)
//	cr              courier text                  nt / nx     note / erase the note
//	rm / rs:<r|s|f> choose a letter / send it     cx          cancel (asks the word)
//	f:...           the refund dialog (shop_refund.go)
//	rv:<f|r>        settle an order in review (shop_refund.go)
//	sf:<o|oy|c|cy>  storefront: ask to open / open / ask to close / close
//	st              the settings (view only)      w:<h|s|o>   open in the browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

const (
	shopDialogKind       = "shop"
	shopRefundDialogKind = "shop_refund"

	// Steps of the shop dialog: a screen is on, or a typed answer is awaited.
	shStepHome   = "home"
	shStepList   = "list"
	shStepCard   = "card"
	shStepShip   = "ship"
	shStepNote   = "note"
	shStepCancel = "cancel"
	shStepReason = "reason" // the typed reason "other" of a failed delivery

	shopModuleName    = "shop"
	shopModeFull      = "full"
	shopModeOrdersOly = "orders_only"

	// The tabs of the orders list.
	shTabShip   = "to_ship"
	shTabReview = "review"
	shTabUnpaid = "unpaid"
	shTabFailed = "failed"
	shTabAll    = "all"

	shopPageSize = 10
	// shopTextMax bounds a tracking number, a note and a refund reason (the
	// server's limit is 2000 characters for each).
	shopTextMax = 2000
)

// shopMode says how much of the shop the current organization has: "full"
// while the module is connected, "orders_only" after it ended while orders
// remain, "" for none.
func shopMode(id *Identity) string {
	if id == nil || id.Current == nil {
		return ""
	}
	m := id.Current
	if m.ShopMode == shopModeFull || m.ShopMode == shopModeOrdersOly {
		return m.ShopMode
	}
	for _, mod := range m.Modules {
		if mod == shopModuleName {
			return shopModeFull
		}
	}
	return ""
}

// shopVisible reports whether the person sees the "Shop" button: the
// organization has a shop (or orders to finish) and the role reads sales.
func shopVisible(id *Identity) bool {
	return shopMode(id) != "" && canViewSales(id)
}

// shopRefundOwns reports the presses of the refund and settle dialog.
func shopRefundOwns(data string) bool {
	return strings.HasPrefix(data, "sh:f:") || strings.HasPrefix(data, "sh:rv:")
}

// shopMenuRow is the main menu's "Shop" row; nil when the person has none.
func (b *Bot) shopMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !shopVisible(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.sh.btn", nil), CallbackData: "sh:home"}}
}

// shopDialog is where the person stands in the shop screens, kept in
// bot_dialogs so a restart keeps it.
type shopDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	Tab   string    `json:"tab"`
	// Query is the search text; it is stored with the dialog and never logged.
	Query  string      `json:"query,omitempty"`
	Page   int         `json:"page"`
	IDs    []uuid.UUID `json:"ids,omitempty"`     // the orders of the page on screen, in order
	CardID *uuid.UUID  `json:"card_id,omitempty"` // the order whose card (or prompt) is open
	MsgID  int         `json:"msg_id,omitempty"`
	// TZ is the shop's time zone (the single zone of its country, else UTC).
	TZ string `json:"tz,omitempty"`
}

func newShopDialog(orgID uuid.UUID, tz string) shopDialog {
	return shopDialog{OrgID: orgID, Tab: shTabShip, Page: 1, TZ: tz}
}

func validShopTab(t string) bool {
	return t == shTabShip || t == shTabReview || t == shTabUnpaid || t == shTabFailed || t == shTabAll
}

func shopTabFromCode(c string) (string, bool) {
	switch c {
	case "s":
		return shTabShip, true
	case "r":
		return shTabReview, true
	case "u":
		return shTabUnpaid, true
	case "f":
		return shTabFailed, true
	case "a":
		return shTabAll, true
	}
	return "", false
}

func shopTabCode(tab string) string {
	switch tab {
	case shTabReview:
		return "r"
	case shTabUnpaid:
		return "u"
	case shTabFailed:
		return "f"
	case shTabAll:
		return "a"
	}
	return "s"
}

// loadShopState reads the dialog, or starts the default one when there is
// none, it ran out (expired is then true, once) or it belongs to another
// organization.
func (b *Bot) loadShopState(ctx context.Context, tg int64, orgID uuid.UUID) (st shopDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, shopDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: shop dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newShopDialog(orgID, ""), "", false
	}
	if !found || st.OrgID != orgID || !validShopTab(st.Tab) {
		return newShopDialog(orgID, ""), "", expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, false
}

func (b *Bot) saveShopState(ctx context.Context, tg int64, st shopDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, shopDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: shop dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveShop ends the dialog: whoever opens another screen is no longer
// searching, so a text typed next is not taken for a search or a note.
func (b *Bot) leaveShop(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, shopDialogKind); err != nil {
		b.logger.Warn("eventbot: shop dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// shopIdentity resolves who is asking for a shop screen. It answers the
// standard screens itself when the person has no organization, no right to
// sales, or the organization has no shop (any more).
func (b *Bot) shopIdentity(ctx context.Context, chatID int64, editMsgID *int, from *models.User) (*Identity, string, bool) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return nil, "", false
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return nil, "", false
	}
	if shopMode(id) == "" {
		b.leaveShop(ctx, from.ID)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.gone", nil), b.backKeyboard(loc, "home"))
		return nil, "", false
	}
	return id, jwt, true
}

// shopZone is the time zone the shop's dates are printed in: the single zone
// of the shop's country, "" (UTC) when the country has several or none.
func shopZone(country *string) string {
	if country == nil {
		return ""
	}
	return geotz.ForCountry(strings.ToUpper(strings.TrimSpace(*country)))
}

// shopBack is a "Back" button.
func (b *Bot) shopBack(loc, data string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: data}}
}

// shopTrack records one analytics event of the shop section (a constant
// action name, nothing the person typed).
func (b *Bot) shopTrack(ctx context.Context, tg int64, orgID uuid.UUID, action string) {
	b.an.track(ctx, tg, &orgID, "bot_shop", map[string]any{"action": action})
}

// ─── the shop screen ──────────────────────────────────────────────────────────

// shopReviewCount counts the orders waiting for a check: the list is read up
// to shopReviewFetch rows (the count has no route of its own); more reports a
// longer list.
func (b *Bot) shopReviewCount(ctx context.Context, jwt string, orgID uuid.UUID) (n int, more bool, err error) {
	res, err := b.arena.ShopOrders(ctx, jwt, orgID, ShopOrderQuery{Status: "manual_review", Limit: shopReviewFetch})
	if err != nil {
		return 0, false, err
	}
	return len(res.Orders), res.NextOffset != nil, nil
}

// shopReviewFetch bounds the "in review" tab: such orders are rare, and the
// tab pages them locally.
const shopReviewFetch = 200

func (b *Bot) renderShopHome(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	mode := shopMode(id)

	counts, err := b.arena.ShopOrderCounts(ctx, jwt, orgID)
	if err != nil {
		b.shopError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	review, more, err := b.shopReviewCount(ctx, jwt, orgID)
	if err != nil {
		b.shopError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	reviewText := fmt.Sprint(review)
	if more {
		reviewText += "+"
	}

	var settings *openapi.ShopSettings
	tz := ""
	if mode == shopModeFull {
		s, serr := b.arena.ShopSettings(ctx, jwt, orgID)
		if serr != nil {
			b.shopError(ctx, chatID, editMsgID, id, serr, "home")
			return
		}
		settings = &s
		tz = shopZone(s.CountryIso2)
	}

	st := newShopDialog(orgID, tz)
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveShopState(ctx, id.Link.TelegramUserID, st, shStepHome)

	// "To ship" is what waits for packing or for the courier: the orders of the steps waiting and packed.
	vars := map[string]any{
		"Org": Esc(id.Current.OrgName), "ToShip": counts.Steps.Waiting + counts.Steps.Packed, "Review": reviewText, "Unpaid": counts.Unpaid,
	}
	text := note + b.texts.T(loc, "bot.sh.home_head", vars)
	if settings != nil {
		text += "\n" + b.shopStorefrontLine(loc, *settings)
	} else {
		text += "\n" + b.texts.T(loc, "bot.sh.orders_only", nil)
	}
	text += "\n" + b.texts.T(loc, "bot.sh.home_counts", vars)

	rows := [][]models.InlineKeyboardButton{{btn(b.texts.T(loc, "bot.sh.orders_btn", nil), "sh:ol")}}
	if settings != nil {
		if settings.Enabled {
			rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.close_btn", nil), "sh:sf:c")})
		} else {
			rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.open_btn", nil), "sh:sf:o")})
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(b.texts.T(loc, "bot.sh.settings_btn", nil), "sh:st"),
			btn(b.texts.T(loc, "bot.web.btn", nil), "sh:w:h"),
		})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.web.btn", nil), "sh:w:h")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.btn_home", nil), "home")})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), inline(rows...))
}

// shopStorefrontLine says whether the storefront is open, with its address
// when the shop has its own domain.
func (b *Bot) shopStorefrontLine(loc string, s openapi.ShopSettings) string {
	key := "bot.sh.storefront_closed"
	if s.Enabled {
		key = "bot.sh.storefront_open"
	}
	line := b.texts.T(loc, key, nil)
	if s.ShopDomain != nil && strings.TrimSpace(*s.ShopDomain) != "" {
		line += " · " + Esc("https://"+strings.TrimSpace(*s.ShopDomain))
	}
	return line
}

// shopError answers a failed API call of a shop screen. A 404
// shop.module_not_enabled means the module went away since /v1/me was read.
func (b *Bot) shopError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error, back string) {
	loc := id.Locale()
	if APIErrorCode(err) == "shop.module_not_enabled" {
		b.leaveShop(ctx, id.Link.TelegramUserID)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.gone", nil), b.backKeyboard(loc, "home"))
		return
	}
	b.ecError(ctx, chatID, editMsgID, id, err, back)
}

// ─── presses ──────────────────────────────────────────────────────────────────

// shopCallback handles every "sh:<data>" press.
func (b *Bot) shopCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.shopIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")
	switch kind {
	case "home":
		b.shopTrack(ctx, from.ID, orgID, "open")
		b.renderShopHome(ctx, chatID, &msgID, id, jwt, "")
		return
	case "w":
		b.shopWeb(ctx, chatID, from, id, arg)
		return
	case "st":
		b.showShopSettings(ctx, chatID, &msgID, id, jwt)
		return
	case "sf":
		b.shopStorefrontPress(ctx, chatID, &msgID, from, id, jwt, arg)
		return
	case "f":
		b.shopRefundCallback(ctx, chatID, msgID, from, id, jwt, arg)
		return
	case "rv":
		b.startShopResolve(ctx, chatID, &msgID, from, id, jwt, arg)
		return
	}
	b.shopOrdersCallback(ctx, chatID, msgID, from, id, jwt, kind, arg)
}

// ─── open in the browser ──────────────────────────────────────────────────────

// shopWebTarget is the event center path "Open in browser" lands on: h the
// shop, s its settings, o one order.
func shopWebTarget(orgID uuid.UUID, where string, orderID *uuid.UUID) string {
	base := "/o/" + orgID.String() + "/shop"
	switch where {
	case "s":
		return base + "/settings"
	case "o":
		if orderID != nil {
			return base + "/orders/" + orderID.String()
		}
	}
	return base
}

// shopWeb sends a one-time link that opens the web event center at the shop.
// Like the main menu's button it goes to the private chat only, and a
// platform superadmin, who works in the admin console, gets no link.
func (b *Bot) shopWeb(ctx context.Context, chatID int64, from *models.User, id *Identity, where string) {
	loc := id.Locale()
	home := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.back_shop_btn", nil), "sh:home")}
	if chatID != from.ID {
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.private_only", nil), inline(home))
		return
	}
	if id.Superadmin {
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.superadmin", nil), inline(home))
		return
	}
	where, rest, _ := strings.Cut(where, ":")
	var orderID *uuid.UUID
	if where == "o" {
		if oid, err := uuid.Parse(rest); err == nil {
			orderID = &oid
		}
	}
	link, err := b.arena.WebLoginLinkTo(ctx, from.ID, shopWebTarget(id.Current.OrgID, where, orderID))
	if err != nil {
		b.reply(ctx, chatID, nil, b.texts.T(loc, webLinkErrorKey(err), nil), inline(home))
		return
	}
	b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.link", nil), inline(
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.open_btn", nil), URL: link.URL}},
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.web.signout_btn", nil), "web:out")},
		home))
}

// ─── settings (view only) ─────────────────────────────────────────────────────

func (b *Bot) showShopSettings(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string) {
	loc := id.Locale()
	if shopMode(id) != shopModeFull {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.orders_only_only", nil), inline(b.shopBack(loc, "sh:home")))
		return
	}
	s, err := b.arena.ShopSettings(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.shopError(ctx, chatID, editMsgID, id, err, "sh:home")
		return
	}
	none := b.texts.T(loc, "bot.sh.none", nil)
	opt := func(p *string) string {
		if p == nil || strings.TrimSpace(*p) == "" {
			return none
		}
		return Esc(strings.TrimSpace(*p))
	}
	age := none
	if s.AgeGateMinAge != nil {
		age = fmt.Sprintf("%d+", *s.AgeGateMinAge)
	}
	label := Esc(s.ShippingLabel)
	if strings.TrimSpace(s.ShippingLabel) == "" {
		label = none
	}
	channel := b.texts.T(loc, "bot.sh.channel_default", nil)
	if s.SalesChannelId != nil {
		channel = b.shopChannelLine(ctx, jwt, id.Current.OrgID, *s.SalesChannelId, channel)
	}
	text := b.texts.T(loc, "bot.sh.settings_text", map[string]any{
		"Org": Esc(id.Current.OrgName), "Currency": Esc(s.Currency),
		"Price": FormatMoney(s.ShippingFeeMinor, s.Currency, loc), "Label": label,
		"Country": opt(s.CountryIso2), "Age": age, "Email": opt(s.ContactEmail),
		"Channel": channel, "Domain": opt(s.ShopDomain), "State": b.shopStorefrontLine(loc, s),
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), inline(
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.edit_web_btn", nil), "sh:w:s")},
		b.shopBack(loc, "sh:home")))
}

// shopChannelLine names the sales channel that takes the shop's money with
// its payment provider; def is the text when it cannot be read.
func (b *Bot) shopChannelLine(ctx context.Context, jwt string, orgID, channelID uuid.UUID, def string) string {
	chs, err := b.arena.ShopChannels(ctx, jwt, orgID)
	if err != nil {
		return def
	}
	for _, ch := range chs {
		if ch.ID == channelID.String() {
			if ch.Provider != "" {
				return Esc(ch.Name) + " (" + Esc(ch.Provider) + ")"
			}
			return Esc(ch.Name)
		}
	}
	return def
}

// ─── the storefront switch ────────────────────────────────────────────────────

// shopBlockerKey is the plain-words text of one reason the storefront cannot
// open (the codes of `open_blockers`).
func shopBlockerKey(code string) string {
	switch code {
	case "settings_not_saved", "no_active_product", "payment_not_configured":
		return "bot.sh.blocker_" + code
	}
	return "bot.sh.blocker_other"
}

func (b *Bot) shopBlockersText(loc string, blockers []string) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.sh.cannot_open", nil))
	for _, code := range blockers {
		sb.WriteString("\n• " + b.texts.T(loc, shopBlockerKey(code), nil))
	}
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.sh.cannot_open_hint", nil))
	return sb.String()
}

// shopStorefrontPress handles "sh:sf:<o|oy|c|cy>".
func (b *Bot) shopStorefrontPress(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	if shopMode(id) != shopModeFull {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.orders_only_only", nil), inline(b.shopBack(loc, "sh:home")))
		return
	}
	cancel := b.shopBack(loc, "sh:home")
	switch arg {
	case "o":
		s, err := b.arena.ShopSettings(ctx, jwt, orgID)
		if err != nil {
			b.shopError(ctx, chatID, editMsgID, id, err, "sh:home")
			return
		}
		if s.Enabled {
			b.renderShopHome(ctx, chatID, editMsgID, id, jwt, "")
			return
		}
		if !s.CanOpen || len(s.OpenBlockers) > 0 {
			b.reply(ctx, chatID, editMsgID, b.shopBlockersText(loc, s.OpenBlockers), inline(
				[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.edit_web_btn", nil), "sh:w:s")}, cancel))
			return
		}
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.open_ask", nil), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.open_yes_btn", nil), "sh:sf:oy")}, cancel))
	case "c":
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sh.close_ask", nil), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.close_yes_btn", nil), "sh:sf:cy")}, cancel))
	case "oy", "cy":
		enable := arg == "oy"
		if _, err := b.arena.ShopStorefront(ctx, jwt, orgID, enable); err != nil {
			var ae *APIError
			if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && ae.Code == "shop.cannot_open" {
				b.reply(ctx, chatID, editMsgID, b.shopBlockersText(loc, ae.Blockers), inline(
					[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.edit_web_btn", nil), "sh:w:s")}, cancel))
				return
			}
			b.shopError(ctx, chatID, editMsgID, id, err, "sh:home")
			return
		}
		action, key := "storefront_close", "bot.sh.closed_done"
		if enable {
			action, key = "storefront_open", "bot.sh.opened_done"
		}
		b.shopTrack(ctx, from.ID, orgID, action)
		b.renderShopHome(ctx, chatID, editMsgID, id, jwt, b.texts.T(loc, key, nil)+"\n\n")
	}
}
