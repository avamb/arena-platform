package eventbot

// resend.go — "Resend tickets" on the order card (EC-13, spec 35 §6.4). The
// dialog, kind "resend" in bot_dialogs (dialogs.go), has three steps:
//
//	choose   "To the same address" (the order's, masked) or "To another address"
//	email    the person types the one-time address
//	confirm  a last press that shows where the letter goes — nothing is queued
//	         before it
//
// then POST .../orders/{id}/resend-tickets. The order's own e-mail never
// changes: a typed address goes to that one resend only (the server keeps it
// for 24 hours, on the delivery task), so the screens say so, and say that
// the person who types it answers for sending the buyer's tickets there.
//
// An order sold through a seller's own site is refused by the API (409
// order.seller_site_order — the site writes its own letters) and the result
// says so with the order number.
//
// Buyer contacts: the order's address is shown MASKED and only in the private
// chat (the button itself is offered there only); the typed address lives in
// the dialog row, like the orders search text, and is never logged. The
// presses are "or:r:<what>" — inside the Orders prefix, so they do not end the
// orders dialog whose list the person returns to.
//
//	or:r:s:<order>   open the dialog for that order    or:r:a   the same address
//	or:r:o           another address                   or:r:go  send
//	or:r:b           back to the choice

import (
	"context"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	resendDialogKind = "resend"
	resStepChoose    = "choose"
	resStepEmail     = "email"
	resStepConfirm   = "confirm"

	// maxResendEmailLen is the longest address the API accepts.
	maxResendEmailLen = 254

	// API error codes of the resend route that have their own screen.
	codeSellerSiteOrder = "order.seller_site_order"
	codeOrderNotPaid    = "order.not_paid"
	codeNoActiveTickets = "order.no_active_tickets"
	codeInvalidEmail    = "order.invalid_email"
)

// resendDialog is the state between two messages.
type resendDialog struct {
	OrgID   uuid.UUID `json:"org_id"`
	OrderID uuid.UUID `json:"order_id"`
	Num     int64     `json:"num"`     // the order number the person knows
	Tickets int       `json:"tickets"` // active tickets that would be sent
	Masked  string    `json:"masked"`  // the order's own address, masked; "" when it has none
	Email   string    `json:"email"`   // the one-time address typed; "" = the order's own
	MsgID   int       `json:"msg_id,omitempty"`
}

// ─── who sees the button ──────────────────────────────────────────────────────

// canResend reports whether the person may resend tickets: the same roles that
// read orders (owner, manager, operator) hold ticket.update.
func canResend(id *Identity) bool { return canViewSales(id) }

// activeTickets counts the tickets of an order that would be sent.
func activeTickets(d openapi.OrderDetail) int {
	n := 0
	for _, t := range d.Tickets {
		if t.Status == "active" {
			n++
		}
	}
	return n
}

// resendOffered reports whether the card carries the button: a paid order with
// at least one active ticket, for a role that may use it, in the private chat.
func resendOffered(id *Identity, d openapi.OrderDetail, private bool) bool {
	return private && canResend(id) && (d.Status == orderStatusPaid || d.Status == orderStatusPartRefund) && activeTickets(d) > 0
}

// withResendButton adds the "Resend tickets" row above the card's last row
// (Back / Home) when the card should carry it.
func (b *Bot) withResendButton(kb *models.InlineKeyboardMarkup, id *Identity, d openapi.OrderDetail, private bool) *models.InlineKeyboardMarkup {
	if kb == nil || !resendOffered(id, d, private) || len(kb.InlineKeyboard) == 0 {
		return kb
	}
	row := []models.InlineKeyboardButton{{
		Text: b.texts.T(id.Locale(), "bot.resend.btn", nil), CallbackData: "or:r:s:" + d.Id.String(),
	}}
	last := len(kb.InlineKeyboard) - 1
	rows := append([][]models.InlineKeyboardButton{}, kb.InlineKeyboard[:last]...)
	rows = append(rows, row, kb.InlineKeyboard[last])
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── address helpers ──────────────────────────────────────────────────────────

// validResendEmail accepts a bare address of the form local@domain.tld — the
// same rule the API applies, so a typo is caught here before a round trip.
func validResendEmail(s string) bool {
	if s == "" || len(s) > maxResendEmailLen || strings.ContainsAny(s, " \t\r\n<>,;\"") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// maskEmail hides all but the first character of the local part and of the
// domain's first label ("anna@example.com" -> "a***@e******.com").
func maskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return "***"
	}
	word := func(w string) string {
		r := []rune(w)
		if len(r) == 0 {
			return ""
		}
		return string(r[:1]) + strings.Repeat("*", len(r)-1)
	}
	domain := s[at+1:]
	first, rest := domain, ""
	if dot := strings.Index(domain, "."); dot > 0 {
		first, rest = domain[:dot], domain[dot:]
	}
	return word(s[:at]) + "@" + word(first) + rest
}

// orderAddress is the address the letter goes to when none is typed: the
// buyer's, else the first active holder's — the rule the API applies.
func orderAddress(d openapi.OrderDetail) string {
	if e := strings.TrimSpace(deref(d.BuyerEmail)); e != "" {
		return e
	}
	for _, t := range d.Tickets {
		if t.Status == "active" {
			if e := strings.TrimSpace(deref(t.HolderEmail)); e != "" {
				return e
			}
		}
	}
	return ""
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

func (b *Bot) saveResend(ctx context.Context, tg int64, st resendDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, resendDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: resend dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

func (b *Bot) leaveResend(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, resendDialogKind); err != nil {
		b.logger.Warn("eventbot: resend dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── presses ──────────────────────────────────────────────────────────────────

// resendCallback handles every "or:r:<data>" press.
func (b *Bot) resendCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canResend(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "s" {
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.startResend(ctx, chatID, &msgID, from, id, jwt, target)
		return
	}

	var st resendDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, resendDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: resend dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if !found || st.OrgID != id.Current.OrgID {
		b.resendGone(ctx, chatID, &msgID, loc, expired)
		return
	}
	switch kind {
	case "b":
		st.Email = ""
		b.showResendChoice(ctx, chatID, &msgID, id, st, "")
	case "a":
		st.Email = ""
		b.showResendConfirm(ctx, chatID, &msgID, from.ID, loc, st)
	case "o":
		st.Email = ""
		st.MsgID = msgID
		b.saveResend(ctx, from.ID, st, resStepEmail)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.resend.other_ask", map[string]any{"Num": st.Num}), b.resendBackKeyboard(loc))
	case "go":
		if step != resStepConfirm {
			b.showResendChoice(ctx, chatID, &msgID, id, st, "")
			return
		}
		b.sendResend(ctx, chatID, &msgID, from, id, jwt, st)
	}
}

// resendGone answers a press (or a typed text) with no live dialog behind it.
// An expired one is reported once, like every dialog (spec 35 §4.1).
func (b *Bot) resendGone(ctx context.Context, chatID int64, editMsgID *int, loc string, expired bool) {
	key := "bot.dialog_expired"
	if !expired {
		key = "bot.ec.not_found"
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ord.btn", nil), CallbackData: "or:new"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// startResend opens the dialog for an order after checking, from a fresh read,
// that there is still something to send.
func (b *Bot) startResend(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, orderID uuid.UUID) {
	loc := id.Locale()
	if chatID != from.ID {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.resend.private_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	detail, err := b.arena.GetOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
		return
	}
	back := b.resendResultKeyboard(loc, orderID)
	switch {
	case detail.Status != orderStatusPaid && detail.Status != orderStatusPartRefund:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.resend.not_paid", map[string]any{"Num": detail.SystemId}), back)
		return
	case activeTickets(detail) == 0:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.resend.no_tickets", map[string]any{"Num": detail.SystemId}), back)
		return
	}
	st := resendDialog{
		OrgID: id.Current.OrgID, OrderID: orderID, Num: detail.SystemId, Tickets: activeTickets(detail),
	}
	if addr := orderAddress(detail); addr != "" {
		st.Masked = maskEmail(addr)
	}
	b.showResendChoice(ctx, chatID, editMsgID, id, st, "")
}

// showResendChoice is the first screen: the same address or another.
func (b *Bot) showResendChoice(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st resendDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveResend(ctx, id.Link.TelegramUserID, st, resStepChoose)
	var rows [][]models.InlineKeyboardButton
	key := "bot.resend.choose_noaddr"
	if st.Masked != "" {
		key = "bot.resend.choose"
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: b.texts.T(loc, "bot.resend.same_btn", map[string]any{"Email": st.Masked}), CallbackData: "or:r:a",
		}})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.resend.other_btn", nil), CallbackData: "or:r:o"}},
		[]models.InlineKeyboardButton{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:v:" + st.OrderID.String()},
			{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
		},
	)
	text := note + b.texts.T(loc, key, map[string]any{"Num": st.Num, "N": st.Tickets, "Email": st.Masked})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// showResendConfirm is the last screen before anything is queued.
func (b *Bot) showResendConfirm(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st resendDialog) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	key, to := "bot.resend.confirm_same", st.Masked
	if st.Email != "" {
		key, to = "bot.resend.confirm_other", st.Email
	}
	b.saveResend(ctx, tg, st, resStepConfirm)
	text := b.texts.T(loc, key, map[string]any{"Num": st.Num, "N": st.Tickets, "Email": Esc(to)})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.resend.send_btn", nil), CallbackData: "or:r:go"}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:r:b"}, {Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

func (b *Bot) resendBackKeyboard(loc string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:r:b"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
}

func (b *Bot) resendResultKeyboard(loc string, orderID uuid.UUID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:v:" + orderID.String()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
}

// ─── the call ─────────────────────────────────────────────────────────────────

// sendResend makes the call and shows what came of it.
func (b *Bot) sendResend(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st resendDialog) {
	loc := id.Locale()
	res, err := b.arena.ResendOrderTickets(ctx, jwt, st.OrgID, st.OrderID, st.Email)
	back := b.resendResultKeyboard(loc, st.OrderID)
	num := map[string]any{"Num": st.Num}
	switch {
	case err == nil:
		b.leaveResend(ctx, from.ID)
		key, to := "bot.resend.done_same", res.RecipientMasked
		if res.DifferentAddress {
			key = "bot.resend.done_other"
		}
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, map[string]any{
			"Num": st.Num, "N": res.QueuedTickets, "Email": Esc(to),
		}), back)
	case IsAPIError(err, http.StatusConflict):
		b.leaveResend(ctx, from.ID)
		key := "bot.resend.not_paid"
		switch APIErrorCode(err) {
		case codeSellerSiteOrder:
			key = "bot.resend.site"
		case codeNoActiveTickets:
			key = "bot.resend.no_tickets"
		case codeOrderNotPaid:
		}
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, num), back)
	case APIErrorCode(err) == codeInvalidEmail:
		st.Email = ""
		st.MsgID = 0
		if editMsgID != nil {
			st.MsgID = *editMsgID
		}
		b.saveResend(ctx, from.ID, st, resStepEmail)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.resend.bad_email", nil), b.resendBackKeyboard(loc))
	default:
		b.leaveResend(ctx, from.ID)
		b.ecError(ctx, chatID, editMsgID, id, err, "or:v:"+st.OrderID.String())
	}
}

// ─── typed text ───────────────────────────────────────────────────────────────

// resendText takes the address typed while the dialog waits for one. It
// reports whether the text was taken. An expired dialog is reported once.
func (b *Bot) resendText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st resendDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, resendDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: resend dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.resendGone(ctx, chatID, nil, loc, true)
		return true
	}
	if !found || step != resStepEmail {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canResend(id) {
		b.leaveResend(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.resend.private_only", nil), b.resendBackKeyboard(loc))
		return true
	}
	addr := strings.TrimSpace(text)
	if !validResendEmail(addr) {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.resend.bad_email", nil), b.resendBackKeyboard(loc))
		return true
	}
	st.Email = addr
	b.showResendConfirm(ctx, chatID, edit, from.ID, loc, st)
	return true
}
