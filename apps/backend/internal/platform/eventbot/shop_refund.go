package eventbot

// shop_refund.go — returning money of a shop order, and settling an order
// parked for a check (spec 61 sections 7.3 and 9). Both move money (or the
// goods) and cannot be undone, so the last step of both is a typed word. The
// dialog is kind "shop_refund" in bot_dialogs:
//
//	amount       the whole amount that is left, or another amount (refund)
//	amount_text  a typed amount, 0 < amount <= what is left
//	reason       a typed reason or "No reason" (refund; optional)
//	word         the summary and the typed word; nothing is sent before it
//	retry        the confirmed operation got no answer: it may have started
//
// The Idempotency-Key is minted when the dialog starts and kept in it, so a
// repeated word, a double press or a bot restart replays the first answer
// instead of returning the money twice. Changing the amount or the reason
// before anything was sent mints a new key; once a send went out without an
// answer (Attempted) the amount and the reason are locked and only the same
// operation can be repeated.
//
//	sh:f:s:<order>  start a refund       sh:f:all / sh:f:part  the amount
//	sh:f:sk         no reason            sh:f:b / sh:f:x       back / cancel
//	sh:f:again      repeat the send      sh:f:cur              show the step
//	sh:rv:f:<order> fulfil               sh:rv:r:<order>       refund and cancel

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	shRefStepAmount     = "amount"
	shRefStepAmountText = "amount_text"
	shRefStepReason     = "reason"
	shRefStepWord       = "word"
	shRefStepRetry      = "retry"

	shRefModeRefund       = "refund"
	shRefModeFulfil       = "fulfil"
	shRefModeRefundCancel = "refund_and_cancel"

	shopRefundWordKey = "bot.sh.refund_word"
	shopFulfilWordKey = "bot.sh.fulfil_word"

	// shopRefundAttemptedTTL keeps the key of an operation that may have
	// started: sending it again with the same key replays it.
	shopRefundAttemptedTTL = 7 * 24 * time.Hour
)

// shopRefundDialog is the state between two messages.
type shopRefundDialog struct {
	OrgID    uuid.UUID `json:"org_id"`
	OrderID  uuid.UUID `json:"order_id"`
	Num      int64     `json:"num"`
	Currency string    `json:"currency"`
	Mode     string    `json:"mode"`
	// Left is what could still go back when the dialog started, minor units.
	Left int64 `json:"left"`
	// Amount is the typed amount; nil returns everything that is left.
	Amount *int64 `json:"amount,omitempty"`
	Reason string `json:"reason,omitempty"`
	Key    string `json:"key"`
	// Attempted: the operation (its Key) went to the server and no answer came
	// back. It may have started, so the dialog and its key are kept until the
	// person decides; sending again with the same key replays.
	Attempted bool `json:"attempted,omitempty"`
	// Parked: the person left an attempted operation for another screen. It
	// keeps its key but takes no typed text until the order's own button
	// brings it back.
	Parked bool `json:"parked,omitempty"`
	MsgID  int  `json:"msg_id,omitempty"`
}

// shopNewKey is the Idempotency-Key of one operation.
func shopNewKey() string { return "tg-" + uuid.NewString() }

// amountMinor is the amount the operation returns.
func (d shopRefundDialog) amountMinor() int64 {
	if d.Amount != nil {
		return *d.Amount
	}
	return d.Left
}

func (d shopRefundDialog) ttl() time.Duration {
	if d.Attempted {
		return shopRefundAttemptedTTL
	}
	return dialogTTL
}

// word is the key of the phrase that confirms the operation.
func (d shopRefundDialog) wordKey() string {
	if d.Mode == shRefModeFulfil {
		return shopFulfilWordKey
	}
	return shopRefundWordKey
}

func (b *Bot) saveShopRefund(ctx context.Context, tg int64, st shopRefundDialog, step string) bool {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, shopRefundDialogKind, step, st, st.ttl()); err != nil {
		b.logger.Error("eventbot: shop refund dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return false
	}
	return true
}

func (b *Bot) saveShopRefundQuiet(ctx context.Context, tg int64, st shopRefundDialog, step string) bool {
	orgID := st.OrgID
	found, err := b.dialogs.SaveQuiet(ctx, tg, &orgID, shopRefundDialogKind, step, st, st.ttl())
	if err != nil {
		b.logger.Error("eventbot: shop refund dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return false
	}
	return found
}

func (b *Bot) deleteShopRefund(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, shopRefundDialogKind); err != nil {
		b.logger.Warn("eventbot: shop refund dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveIdleShopRefund ends the dialog when the person goes elsewhere, but
// never one whose operation may have started: its key stays (the dialog only
// stops taking typed text) until the order's own button brings it back.
func (b *Bot) leaveIdleShopRefund(ctx context.Context, tg int64) {
	var st shopRefundDialog
	step, found, _, err := b.dialogs.Load(ctx, tg, shopRefundDialogKind, &st)
	if err == nil && found && st.Attempted {
		if !st.Parked {
			st.Parked = true
			b.saveShopRefundQuiet(ctx, tg, st, step)
		}
		return
	}
	b.deleteShopRefund(ctx, tg)
}

// askLeaveShopRefund stops the menu when leaving would decide a shop refund's
// fate: one waiting for its typed word is dropped by leaving, one that may
// have started keeps its key. It reports whether it asked.
func (b *Bot) askLeaveShopRefund(ctx context.Context, chatID int64, from *models.User, id *Identity) bool {
	var st shopRefundDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, shopRefundDialogKind, &st)
	if err != nil || !found || st.Parked {
		return false
	}
	loc := id.Locale()
	back := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.menu_back_btn", nil), "sh:f:cur")}
	leave := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_leave_btn", nil), menuCallback+":leave")}
	vars := map[string]any{"Num": st.Num}
	switch {
	case st.Attempted:
		order := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_open_order_btn", nil), "sh:v:"+st.OrderID.String())}
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.sh.menu_leave_maybe", vars), inline(back, order, leave))
	case step == shRefStepWord:
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.sh.menu_leave_refund", vars), inline(back, leave))
	default:
		return false
	}
	return true
}

// ─── starting ─────────────────────────────────────────────────────────────────

// shopRefundCallback handles every "sh:f:<data>" press.
func (b *Bot) shopRefundCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, arg string) {
	loc := id.Locale()
	kind, rest, _ := strings.Cut(arg, ":")
	if kind == "s" {
		orderID, err := uuid.Parse(rest)
		if err != nil {
			return
		}
		b.startShopRefund(ctx, chatID, &msgID, from, id, jwt, orderID)
		return
	}
	var st shopRefundDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, shopRefundDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: shop refund dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if expired || !found || st.OrgID != id.Current.OrgID {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.dialog_expired", nil), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.btn", nil), "sh:home")}))
		return
	}
	st.Parked = false
	st.MsgID = msgID
	edit := &msgID
	switch kind {
	case "cur":
		b.showShopRefundStep(ctx, chatID, edit, id, st, step, "")
	case "x":
		b.cancelShopRefund(ctx, chatID, edit, from, id, jwt, st)
	case "b":
		if st.Attempted {
			b.showShopRefundStep(ctx, chatID, edit, id, st, step, "")
			return
		}
		b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepAmount, "")
	case "all", "part", "sk":
		if st.Attempted || st.Mode != shRefModeRefund {
			b.showShopRefundStep(ctx, chatID, edit, id, st, step, "")
			return
		}
		switch kind {
		case "all":
			st.Amount, st.Key = nil, shopNewKey()
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepReason, "")
		case "part":
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepAmountText, "")
		case "sk":
			st.Reason, st.Key = "", shopNewKey()
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepWord, "")
		}
	case "again":
		if st.Attempted {
			b.sendShopMoney(ctx, chatID, edit, from, id, st)
			return
		}
		b.showShopRefundStep(ctx, chatID, edit, id, st, step, "")
	}
}

// cancelShopRefund ends the dialog and returns to the order's card. An
// operation that may have started is not dropped: only parked.
func (b *Bot) cancelShopRefund(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st shopRefundDialog) {
	if st.Attempted {
		st.Parked = true
		b.saveShopRefundQuiet(ctx, from.ID, st, shRefStepRetry)
	} else {
		b.deleteShopRefund(ctx, from.ID)
	}
	prev, _, _ := b.loadShopState(ctx, from.ID, id.Current.OrgID)
	b.showShopCard(ctx, chatID, edit, from, id, jwt, prev, st.OrderID, "")
}

// shopOtherAttempt reports a parked or live dialog of ANOTHER order that may
// have started: its key must not be overwritten.
func (b *Bot) shopOtherAttempt(ctx context.Context, tg int64, orderID uuid.UUID) (shopRefundDialog, bool) {
	var cur shopRefundDialog
	_, found, _, err := b.dialogs.Load(ctx, tg, shopRefundDialogKind, &cur)
	if err != nil || !found || !cur.Attempted || cur.OrderID == orderID {
		return cur, false
	}
	return cur, true
}

// shopResume brings back the dialog of an order whose operation may have
// started, so the person repeats it with the same key instead of starting a
// second one.
func (b *Bot) shopResume(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, orderID uuid.UUID) bool {
	var cur shopRefundDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, shopRefundDialogKind, &cur)
	if err != nil || !found || !cur.Attempted || cur.OrderID != orderID || cur.OrgID != id.Current.OrgID {
		return false
	}
	cur.Parked = false
	b.showShopRefundStep(ctx, chatID, edit, id, cur, step, "")
	return true
}

// startShopRefund opens the dialog after reading the order afresh.
func (b *Bot) startShopRefund(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, orderID uuid.UUID) {
	loc := id.Locale()
	if chatID != from.ID {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ref.private_only", nil), inline(b.shopBack(loc, "sh:home")))
		return
	}
	if b.shopResume(ctx, chatID, edit, from, id, orderID) {
		return
	}
	if cur, blocked := b.shopOtherAttempt(ctx, from.ID, orderID); blocked {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.other_attempt", map[string]any{"Num": cur.Num}), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_open_order_btn", nil), "sh:v:"+cur.OrderID.String())},
			b.shopBack(loc, "sh:home")))
		return
	}
	o, err := b.arena.ShopOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.shopError(ctx, chatID, edit, id, err, "sh:b")
		return
	}
	prev, _, _ := b.loadShopState(ctx, from.ID, id.Current.OrgID)
	if !shopCanRefund(o) {
		b.renderShopCard(ctx, chatID, edit, from, id, prev, o, b.texts.T(loc, "bot.sh.refund_nothing", nil)+"\n\n")
		return
	}
	st := shopRefundDialog{
		OrgID: id.Current.OrgID, OrderID: o.Id, Num: o.Number, Currency: o.Currency,
		Mode: shRefModeRefund, Left: shopRefundLeft(o), Key: shopNewKey(),
	}
	b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepAmount, "")
}

// startShopResolve opens the dialog that settles an order in review. arg is
// "f:<order>" (fulfil) or "r:<order>" (refund and cancel).
func (b *Bot) startShopResolve(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt, arg string) {
	loc := id.Locale()
	which, idStr, _ := strings.Cut(arg, ":")
	orderID, err := uuid.Parse(idStr)
	if err != nil || (which != "f" && which != "r") {
		return
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ref.private_only", nil), inline(b.shopBack(loc, "sh:home")))
		return
	}
	if b.shopResume(ctx, chatID, edit, from, id, orderID) {
		return
	}
	if cur, blocked := b.shopOtherAttempt(ctx, from.ID, orderID); blocked {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.other_attempt", map[string]any{"Num": cur.Num}), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_open_order_btn", nil), "sh:v:"+cur.OrderID.String())},
			b.shopBack(loc, "sh:home")))
		return
	}
	o, err := b.arena.ShopOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.shopError(ctx, chatID, edit, id, err, "sh:b")
		return
	}
	prev, _, _ := b.loadShopState(ctx, from.ID, id.Current.OrgID)
	if !shopCanResolve(o) {
		b.renderShopCard(ctx, chatID, edit, from, id, prev, o, b.texts.T(loc, "bot.sh.err_changed", nil)+"\n\n")
		return
	}
	mode := shRefModeFulfil
	if which == "r" {
		mode = shRefModeRefundCancel
	}
	st := shopRefundDialog{
		OrgID: id.Current.OrgID, OrderID: o.Id, Num: o.Number, Currency: o.Currency,
		Mode: mode, Left: shopRefundLeft(o), Key: shopNewKey(),
	}
	b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepWord, "")
}

// ─── screens ──────────────────────────────────────────────────────────────────

// showShopRefundStep draws one step and stores the dialog on it.
func (b *Bot) showShopRefundStep(ctx context.Context, chatID int64, edit *int, id *Identity, st shopRefundDialog, step, note string) {
	loc := id.Locale()
	tg := id.Link.TelegramUserID
	if edit != nil {
		st.MsgID = *edit
	}
	if st.Attempted {
		step = shRefStepRetry
	}
	b.saveShopRefund(ctx, tg, st, step)
	vars := map[string]any{
		"Num": st.Num, "Left": FormatMoney(st.Left, st.Currency, loc),
		"Amount": FormatMoney(st.amountMinor(), st.Currency, loc), "Word": b.texts.T(loc, st.wordKey(), nil),
		"Reason": Esc(st.Reason),
	}
	cancel := btn(b.texts.T(loc, "bot.sh.refund_cancel_dialog_btn", nil), "sh:f:x")
	back := btn("« "+b.texts.T(loc, "bot.btn_back", nil), "sh:f:b")
	switch step {
	case shRefStepAmount:
		b.reply(ctx, chatID, edit, note+b.texts.T(loc, "bot.sh.refund_amount", vars), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_all_btn", vars), "sh:f:all")},
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_part_btn", nil), "sh:f:part")},
			[]models.InlineKeyboardButton{cancel}))
	case shRefStepAmountText:
		b.reply(ctx, chatID, edit, note+b.texts.T(loc, "bot.sh.refund_amount_text", vars), inline(
			[]models.InlineKeyboardButton{back}))
	case shRefStepReason:
		b.reply(ctx, chatID, edit, note+b.texts.T(loc, "bot.sh.refund_reason", vars), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_no_reason_btn", nil), "sh:f:sk")},
			[]models.InlineKeyboardButton{back}))
	case shRefStepWord:
		key := "bot.sh.refund_confirm"
		switch st.Mode {
		case shRefModeFulfil:
			key = "bot.sh.fulfil_confirm"
		case shRefModeRefundCancel:
			key = "bot.sh.refund_cancel_confirm"
		}
		reason := ""
		if st.Reason != "" {
			reason = b.texts.T(loc, "bot.sh.refund_reason_line", map[string]any{"Reason": Esc(st.Reason)})
		}
		vars["ReasonLine"] = reason
		first := []models.InlineKeyboardButton{cancel}
		if st.Mode == shRefModeRefund {
			first = []models.InlineKeyboardButton{back, cancel}
		}
		b.reply(ctx, chatID, edit, note+b.texts.T(loc, key, vars), inline(first))
	case shRefStepRetry:
		b.reply(ctx, chatID, edit, note+b.texts.T(loc, "bot.sh.refund_retry", vars), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.refund_again_btn", nil), "sh:f:again")},
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_open_order_btn", nil), "sh:v:"+st.OrderID.String())}))
	}
}

// ─── typed text ───────────────────────────────────────────────────────────────

// shopRefundText takes a typed text while the dialog asks for one: an amount,
// a reason or the confirming word. It reports whether it took the text.
func (b *Bot) shopRefundText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if chatID != from.ID {
		return false // the dialog lives in the private chat only
	}
	var st shopRefundDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, shopRefundDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: shop refund dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.btn", nil), "sh:home")}))
		return true
	}
	if !found || st.Parked || (step != shRefStepAmountText && step != shRefStepReason && step != shRefStepWord) {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		return false // a passing failure: the dialog and its key stay as they are
	}
	if id.Current == nil || id.Current.OrgID != st.OrgID || !canViewSales(id) || shopMode(id) == "" {
		b.leaveIdleShopRefund(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	text = strings.TrimSpace(text)
	switch step {
	case shRefStepAmountText:
		minor, ok := ParsePriceMinor(text)
		if !ok || minor <= 0 || minor > st.Left {
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepAmountText, b.texts.T(loc, "bot.sh.refund_amount_bad", map[string]any{
				"Left": FormatMoney(st.Left, st.Currency, loc)})+"\n\n")
			return true
		}
		st.Amount, st.Key = &minor, shopNewKey()
		b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepReason, "")
	case shRefStepReason:
		if text == "" || len([]rune(text)) > shopTextMax {
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepReason, b.texts.T(loc, "bot.sh.err_too_long", nil)+"\n\n")
			return true
		}
		st.Reason, st.Key = text, shopNewKey()
		b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepWord, "")
	case shRefStepWord:
		if !b.typedPhrase(loc, st.wordKey(), text) {
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepWord,
				b.texts.T(loc, "bot.sh.word_wrong", map[string]any{"Word": b.texts.T(loc, st.wordKey(), nil)})+"\n\n")
			return true
		}
		b.sendShopMoney(ctx, chatID, edit, from, id, st)
	}
	return true
}

// ─── the call ─────────────────────────────────────────────────────────────────

// shopMoneyResult is the outcome of one send, for the person and the tests.
type shopMoneyResult struct {
	Order   *openapi.ShopOrder
	Refund  *openapi.ShopRefund
	Replay  bool
	Err     error
	Unknown bool // no answer at all: the operation may have started
}

// callShopMoney sends the confirmed operation. A call with no answer is
// repeated with the SAME key: the server answers the operation it already
// started, it never starts a second one. Every attempt carries a freshly
// minted token, so a long wait never ends on an expired one.
func (b *Bot) callShopMoney(ctx context.Context, id *Identity, st shopRefundDialog) shopMoneyResult {
	var res shopMoneyResult
	for attempt := 0; attempt < shopReplayAttempts; attempt++ {
		jwt, merr := b.minter.Mint(id.Link.UserID)
		if merr != nil {
			res.Err = merr
			res.Unknown = true
			return res
		}
		var err error
		switch st.Mode {
		case shRefModeRefund:
			var out openapi.ShopRefundResponse
			out, err = b.arena.ShopRefund(ctx, jwt, st.OrgID, st.OrderID, st.Key, st.Amount, st.Reason)
			if err == nil {
				res.Order, res.Refund = &out.Order, &out.Refund
				res.Replay = out.Replayed != nil && *out.Replayed
			}
		default:
			var out openapi.ShopResolveResponse
			out, err = b.arena.ShopResolve(ctx, jwt, st.OrgID, st.OrderID, st.Key, st.Mode)
			if err == nil {
				res.Order, res.Refund, res.Replay = &out.Order, out.Refund, out.Replayed
			}
		}
		res.Err = err
		var ae *APIError
		if err == nil || errors.As(err, &ae) {
			return res
		}
		if ctx.Err() != nil {
			break
		}
		if attempt+1 < shopReplayAttempts {
			select {
			case <-ctx.Done():
			case <-time.After(refundReplayPause):
			}
		}
	}
	res.Unknown = true
	return res
}

// sendShopMoney records that the operation is going out, sends it and tells
// the person what came back.
func (b *Bot) sendShopMoney(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, st shopRefundDialog) {
	loc := id.Locale()
	st.Attempted, st.Parked = true, false
	if edit != nil {
		st.MsgID = *edit
	}
	// Without the record of the send, a second press could start another
	// one: nothing is sent, the person is told to try again.
	if !b.saveShopRefund(ctx, from.ID, st, shRefStepWord) {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sh.err_unavailable", nil), inline(b.shopBack(loc, "sh:v:"+st.OrderID.String())))
		return
	}
	b.consumePress(ctx, chatID)
	wctx, cancel := context.WithTimeout(detached(ctx), shopCallWindow)
	defer cancel()
	res := b.callShopMoney(wctx, id, st)
	b.finishShopMoney(ctx, chatID, edit, from, id, st, res)
}

// finishShopMoney writes the end of a send into the dialog and tells the person.
func (b *Bot) finishShopMoney(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, st shopRefundDialog, res shopMoneyResult) {
	loc := id.Locale()
	tg := from.ID
	orderBtn := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.menu_open_order_btn", nil), "sh:v:"+st.OrderID.String())}
	shopBtn := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.sh.back_shop_btn", nil), "sh:home")}

	var ae *APIError
	switch {
	case res.Err == nil:
		b.deleteShopRefund(ctx, tg)
		action := "refund"
		if st.Mode != shRefModeRefund {
			action = "resolve"
		}
		b.shopTrack(ctx, tg, st.OrgID, action)
		b.reply(ctx, chatID, edit, b.shopMoneyText(loc, st, res), inline(orderBtn, shopBtn))
	case errors.As(res.Err, &ae) && ae.Status < http.StatusInternalServerError:
		// The server ANSWERED with a refusal: nothing was recorded by this
		// call, so the key is spent on nothing and the dialog ends — except
		// a wrong amount, which goes back to the amount with what is left.
		if ae.Code == "shop.invalid_refund_amount" && ae.RefundableMinor != nil && *ae.RefundableMinor > 0 {
			st.Attempted, st.Left, st.Amount, st.Key = false, *ae.RefundableMinor, nil, shopNewKey()
			b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepAmount, b.texts.T(loc, "bot.sh.refund_amount_changed", map[string]any{
				"Left": FormatMoney(st.Left, st.Currency, loc)})+"\n\n")
			return
		}
		b.deleteShopRefund(ctx, tg)
		b.reply(ctx, chatID, edit, b.shopMoneyErrorText(loc, st, ae), inline(orderBtn, shopBtn))
	default:
		// No answer at all, or a server error: the operation may well be
		// under way. The dialog and its key are kept; never "failed".
		if res.Err != nil {
			b.logger.Warn("eventbot: shop money call got no answer", slog.Int64("telegram_user_id", tg), slog.String("error", res.Err.Error()))
		}
		b.showShopRefundStep(ctx, chatID, edit, id, st, shRefStepRetry, "")
	}
}

// shopMoneyText says how a refund or a decision ended.
func (b *Bot) shopMoneyText(loc string, st shopRefundDialog, res shopMoneyResult) string {
	vars := map[string]any{"Num": st.Num, "Amount": FormatMoney(st.amountMinor(), st.Currency, loc), "Reason": ""}
	state := ""
	if res.Refund != nil {
		vars["Amount"] = FormatMoney(res.Refund.Amount, res.Refund.Currency, loc)
		state = res.Refund.State
		if res.Refund.FailureReason != nil {
			vars["Reason"] = Esc(*res.Refund.FailureReason)
		}
	}
	if st.Mode == shRefModeFulfil {
		return b.texts.T(loc, "bot.sh.fulfil_done", vars)
	}
	prefix := "bot.sh.refund_"
	if st.Mode == shRefModeRefundCancel {
		prefix = "bot.sh.refund_cancel_"
	}
	switch state {
	case "succeeded":
		return b.texts.T(loc, prefix+"done", vars)
	case "provider_pending":
		return b.texts.T(loc, prefix+"pending", vars)
	case "manual_review":
		return b.texts.T(loc, prefix+"review", vars)
	case "failed":
		return b.texts.T(loc, prefix+"failed", vars)
	}
	return b.texts.T(loc, prefix+"pending", vars)
}

// shopMoneyErrorText is the sentence for a refusal of the refund or resolve
// route.
func (b *Bot) shopMoneyErrorText(loc string, st shopRefundDialog, ae *APIError) string {
	vars := map[string]any{"Num": st.Num}
	switch ae.Code {
	case "shop.nothing_to_refund":
		return b.texts.T(loc, "bot.sh.refund_nothing", vars)
	case "shop.order_not_refundable":
		return b.texts.T(loc, "bot.sh.err_not_refundable", vars)
	case "shop.out_of_stock":
		return b.texts.T(loc, "bot.sh.err_out_of_stock", vars)
	case "shop.payment_unconfirmed":
		return b.texts.T(loc, "bot.sh.err_payment_unconfirmed", vars)
	case "shop.order_not_resolvable":
		return b.texts.T(loc, "bot.sh.err_not_resolvable", vars)
	case "shop.order_not_in_review":
		return b.texts.T(loc, "bot.sh.err_not_in_review", vars)
	case "shop.invalid_refund_amount":
		return b.texts.T(loc, "bot.sh.refund_nothing", vars)
	}
	if ae.Status == http.StatusForbidden {
		return b.texts.T(loc, "bot.ec.no_rights", nil)
	}
	return b.texts.T(loc, "bot.sh.err_money_failed", vars)
}
