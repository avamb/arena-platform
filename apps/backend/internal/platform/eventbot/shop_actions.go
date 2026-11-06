package eventbot

// shop_actions.go — what the organizer does with one shop order besides the
// money (spec 61 section 7.3): ship with a tracking number, the courier text,
// the internal note, the buyer's letter once more and the cancellation of an
// unpaid order. The refund and the settling of an order in review are in
// shop_refund.go.
//
// A step that waits for typed text keeps its state in the "shop" dialog:
//
//	ship    the tracking number (or the "No number" button)
//	note    the internal note
//	cancel  the cancel word of an unpaid order
//
// Cancelling cannot be undone, so it is never one tap: the button only opens
// a prompt that asks for the localized word, and a wrong word cancels nothing.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

const (
	shopCancelWordKey = "bot.sh.cancel_word"
	// shopCourierMax bounds the courier block (Telegram caps a message).
	shopCourierMax = 3500
)

// shopCardAction handles the presses of a card: sh, sn, cr, nt, nx, rm, rs, cx,
// and the delivery steps ps and fr (shop_steps.go).
func (b *Bot) shopCardAction(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	code, idStr := "", arg
	if kind == "rs" || kind == "ps" || kind == "fr" {
		code, idStr, _ = strings.Cut(arg, ":")
	}
	orderID, err := uuid.Parse(idStr)
	if err != nil {
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	tg := from.ID
	st, _, _ := b.loadShopState(ctx, tg, orgID)
	edit := &msgID
	toCard := []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "sh:v:" + orderID.String()}}

	switch kind {
	case "sh":
		o, err := b.arena.ShopOrder(ctx, jwt, orgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		if !shopCanShip(o) && !shopCanEditTracking(o) {
			b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(loc, "bot.sh.err_changed", nil)+"\n\n")
			return
		}
		st.CardID, st.MsgID = &orderID, msgID
		b.saveShopState(ctx, tg, st, shStepShip)
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.ship_ask", map[string]any{"Num": o.Number}), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.no_number_btn", nil), "sh:sn:"+orderID.String())},
			toCard))
	case "sn":
		b.shopShip(ctx, chatID, edit, from, id, jwt, st, orderID, "")
	case "ps":
		b.shopStepPress(ctx, chatID, edit, from, id, jwt, st, orderID, code)
	case "fr":
		b.shopReasonPress(ctx, chatID, msgID, from, id, jwt, st, orderID, code)
	case "cr":
		b.shopCourier(ctx, chatID, edit, from, id, jwt, st, orderID)
	case "nt":
		o, err := b.arena.ShopOrder(ctx, jwt, orgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		st.CardID, st.MsgID = &orderID, msgID
		b.saveShopState(ctx, tg, st, shStepNote)
		text := b.texts.T(loc, "bot.sh.note_ask", map[string]any{"Num": o.Number})
		rows := [][]models.InlineKeyboardButton{}
		if strings.TrimSpace(o.StaffNote.Text) != "" {
			text += "\n\n" + b.texts.T(loc, "bot.sh.note_current", map[string]any{"Text": Esc(o.StaffNote.Text)})
			rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.note_erase_btn", nil), "sh:nx:"+orderID.String())})
		}
		rows = append(rows, toCard)
		b.reply(ctx, chatID, edit, text, inline(rows...))
	case "nx":
		b.shopSaveNote(ctx, chatID, edit, from, id, jwt, st, orderID, "")
	case "rm":
		o, err := b.arena.ShopOrder(ctx, jwt, orgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		var rows [][]models.InlineKeyboardButton
		for _, k := range shopLetters(o) {
			rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.letter_"+k, nil), "sh:rs:"+shopLetterCode(k)+":"+orderID.String())})
		}
		if len(rows) == 0 {
			b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(loc, "bot.sh.err_changed", nil)+"\n\n")
			return
		}
		rows = append(rows, toCard)
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.resend_ask", map[string]any{"Num": o.Number, "Email": Esc(o.Buyer.Email)}), inline(rows...))
	case "rs":
		letter, ok := shopLetterFromCode(code)
		if !ok {
			return
		}
		b.shopResend(ctx, chatID, edit, from, id, jwt, st, orderID, letter)
	case "cx":
		o, err := b.arena.ShopOrder(ctx, jwt, orgID, orderID)
		if err != nil {
			b.shopError(ctx, chatID, edit, id, err, "sh:b")
			return
		}
		if !shopCanCancel(o) {
			b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(loc, "bot.sh.cancel_changed", nil)+"\n\n")
			return
		}
		st.CardID, st.MsgID = &orderID, msgID
		b.saveShopState(ctx, tg, st, shStepCancel)
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.cancel_ask", map[string]any{
			"Num": o.Number, "Word": b.shopCancelWord(loc),
		}), inline(toCard))
	}
}

func (b *Bot) shopCancelWord(loc string) string { return b.texts.T(loc, shopCancelWordKey, nil) }

// ─── the actions ──────────────────────────────────────────────────────────────

// shopActionError turns a refusal of an order action into a sentence and draws
// the card again with it, read afresh so the buttons match the order's state.
func (b *Bot) shopActionError(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, err error) {
	loc := id.Locale()
	var ae *APIError
	if !errors.As(err, &ae) {
		b.logger.Warn("eventbot: shop order call failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.error_generic", nil), inline(
			[]models.InlineKeyboardButton{btn("« "+b.texts.T(loc, "bot.btn_back", nil), "sh:v:"+orderID.String())}))
		return
	}
	if ae.Code == "shop.module_not_enabled" || ae.Code == "shop.order_not_found" || ae.Status == http.StatusForbidden {
		b.shopError(ctx, chatID, edit, id, err, "sh:b")
		return
	}
	key, vars := "bot.error_generic", map[string]any(nil)
	switch {
	case ae.Code == "shop.fulfillment_order_closed":
		key = "bot.sh.err_closed"
	case ae.Code == "shop.fulfillment_order_unpaid":
		key = "bot.sh.err_unpaid"
	case ae.Code == "shop.fulfillment_order_in_review":
		key = "bot.sh.err_in_review"
	case ae.Code == "shop.out_of_stock":
		key = "bot.sh.err_out_of_stock"
	case ae.Code == "shop.payment_unconfirmed":
		key = "bot.sh.err_payment_unconfirmed"
	case ae.Code == "shop.postal_required":
		key = "bot.sh.err_postal_required"
	case ae.Code == "shop.invalid_postal":
		key = "bot.sh.err_invalid_postal"
	case ae.Code == "shop.text_too_long":
		key = "bot.sh.err_too_long"
	case ae.Status == http.StatusTooManyRequests:
		secs := ae.RetryAfter
		if secs <= 0 {
			secs = 60
		}
		key, vars = "bot.sh.err_rate_limited", map[string]any{"Seconds": secs}
	case ae.Code == "shop.order_no_buyer_email":
		key = "bot.sh.err_no_email"
	case ae.Code == "shop.order_email_unavailable":
		key = "bot.sh.err_letter_unavailable"
	case ae.Status == http.StatusConflict:
		key = "bot.sh.err_changed"
	default:
		b.logger.Warn("eventbot: shop order call refused", slog.Int64("telegram_user_id", from.ID),
			slog.Int("status", ae.Status), slog.String("code", ae.Code))
	}
	b.showShopCard(ctx, chatID, edit, from, id, jwt, st, orderID, b.texts.T(id.Locale(), key, vars)+"\n\n")
}

// shopShip hands the order to the courier with the tracking number ("" for none).
// It is the delivery step handed_over (ArenaClient.ShopShip), one behaviour for
// the first hand-over, another try after a failed delivery and the later edit
// of the number.
func (b *Bot) shopShip(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, tracking string) {
	o, err := b.arena.ShopShip(ctx, jwt, id.Current.OrgID, orderID, tracking)
	if err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	b.shopTrack(ctx, from.ID, id.Current.OrgID, "ship")
	b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(id.Locale(), "bot.sh.ship_done", map[string]any{"Num": o.Number})+"\n\n")
}

// shopCourier posts the delivery details as a message of its own, in a code
// block, so one tap copies the whole text. The card above keeps its buttons.
func (b *Bot) shopCourier(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID) {
	loc := id.Locale()
	if chatID != from.ID {
		// A delivery address goes to the private chat only.
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.courier_private", nil), nil)
		return
	}
	text, err := b.arena.ShopCourierText(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.send(ctx, chatID, b.texts.T(loc, "bot.sh.courier_empty", nil), nil)
		return
	}
	b.send(ctx, chatID, b.texts.T(loc, "bot.sh.courier_head", nil)+"\n<pre>"+Esc(clipMessage(text, shopCourierMax))+"</pre>", nil)
}

// shopSaveNote writes the internal note ("" erases it).
func (b *Bot) shopSaveNote(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, note string) {
	o, err := b.arena.ShopNote(ctx, jwt, id.Current.OrgID, orderID, note)
	if err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	key := "bot.sh.note_saved"
	if note == "" {
		key = "bot.sh.note_erased"
	}
	b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(id.Locale(), key, nil)+"\n\n")
}

// shopResend queues the buyer's letter again.
func (b *Bot) shopResend(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID, letter string) {
	if err := b.arena.ShopResend(ctx, jwt, id.Current.OrgID, orderID, letter); err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	b.shopTrack(ctx, from.ID, id.Current.OrgID, "resend")
	b.showShopCard(ctx, chatID, edit, from, id, jwt, st, orderID, b.texts.T(id.Locale(), "bot.sh.resend_done", nil)+"\n\n")
}

// shopCancel cancels an unpaid order after the typed word.
func (b *Bot) shopCancel(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopDialog, orderID uuid.UUID) {
	o, err := b.arena.ShopCancel(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.shopActionError(ctx, chatID, edit, from, id, jwt, st, orderID, err)
		return
	}
	b.shopTrack(ctx, from.ID, id.Current.OrgID, "cancel")
	b.renderShopCard(ctx, chatID, edit, from, id, st, o, b.texts.T(id.Locale(), "bot.sh.cancel_done", map[string]any{"Num": o.Number})+"\n\n")
}

// ─── typed text ───────────────────────────────────────────────────────────────

// shopText takes a typed text while a shop screen is open: as the search while
// the list is on screen, as the tracking number, the note or the cancel word
// while one of them is awaited. It reports whether the text was taken. An
// expired screen is reported once, like every dialog (spec 35 section 4.1).
func (b *Bot) shopText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st shopDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, shopDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: shop dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.btn", nil), "sh:home")},
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.btn_home", nil), "home")}))
		return true
	}
	if !found || (step != shStepList && step != shStepShip && step != shStepNote && step != shStepCancel && step != shStepReason) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canViewSales(id) || shopMode(id) == "" {
		b.leaveShop(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	text = strings.TrimSpace(text)
	if step == shStepList {
		st.Query = cutRunes(strings.Join(strings.Fields(text), " "), maxQueryRunes)
		st.Page = 1
		b.renderShopOrders(ctx, chatID, edit, id, jwt, st, "")
		return true
	}
	if st.CardID == nil {
		b.leaveShop(ctx, from.ID)
		return false
	}
	orderID := *st.CardID
	toCard := inline([]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "sh:v:" + orderID.String()}})
	switch step {
	case shStepReason:
		if text == "" {
			return false
		}
		b.shopReasonText(ctx, chatID, edit, from, id, jwt, st, orderID, text, toCard)
	case shStepShip, shStepNote:
		if text == "" {
			return false
		}
		if len([]rune(text)) > shopTextMax {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.err_too_long", nil), toCard)
			return true
		}
		if step == shStepShip {
			b.shopShip(ctx, chatID, edit, from, id, jwt, st, orderID, text)
		} else {
			b.shopSaveNote(ctx, chatID, edit, from, id, jwt, st, orderID, text)
		}
	case shStepCancel:
		if !b.typedPhrase(loc, shopCancelWordKey, text) {
			// Nothing is cancelled: the prompt stays, the word is repeated.
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.cancel_wrong", map[string]any{"Word": b.shopCancelWord(loc)}), toCard)
			return true
		}
		b.shopCancel(ctx, chatID, edit, from, id, jwt, st, orderID)
	}
	return true
}
