package eventbot

// refund_dialog.go — "Refund" on the order card (EC-21 and EC-22, spec 35 §8,
// spec 36 §9). The bot computes nothing: it reads GET .../refund-options and
// shows what the server says is refundable. The dialog, kind "refund" in
// bot_dialogs, has these steps:
//
//	pick     toggle the tickets to return money for (all selected to start)
//	amount   a typed part of ONE ticket's amount, only when the provider can
//	reason   a quick reason or a typed one — required
//	word     the summary and the typed word; nothing is sent before it
//
// The route decides what the person sees first: arena_provider opens the
// dialog; seller_site, unsupported and none only explain (EC-22) — the money
// goes back at the site or in the provider's dashboard, never from here.
//
// A refund cannot be undone, so the last press is a typed word (REFUND /
// ВЕРНУТЬ / DEVOLVER, English accepted in any language), exactly like a
// session cancellation. The Idempotency-Key is minted when the summary is
// first shown and kept in the dialog, so a repeated word, a double press or a
// bot restart replays the first answer instead of refunding twice; changing
// the choice mints a new key.
//
//	or:f:s:<order>  open for that order   or:f:t:<i>  toggle ticket i
//	or:f:all        all tickets           or:f:go     continue to the reason
//	or:f:p          part of the amount    or:f:r:<i>  quick reason i
//	or:f:ro         another reason        or:f:nt     toggle the buyer letter
//	or:f:b          back to the tickets

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	refundDialogKind = "refund"
	refStepPick      = "pick"
	refStepAmount    = "amount"
	refStepReason    = "reason"
	refStepReasonTxt = "reason_text"
	refStepWord      = "word"
	refStepFinal     = "final"
	// refStepSending: the confirmed operation is with the server. Nothing in
	// the dialog changes until the answer is in (or refundSendWindow passed,
	// after a restart that lost the call).
	refStepSending = "sending"

	maxRefundReasonLen = 300
	refundQuickReasons = 3

	// Routes (spec 36 section 6.2) and the API codes that have their own text.
	refundRouteArena   = "arena_provider"
	refundRouteSite    = "seller_site"
	codeRefundProgress = "refund.in_progress"
	codeRefundSite     = "refund.seller_site_order"
	codeRefundNotFit   = "refund.ticket_not_refundable"
	codeRefundUnsupp   = "refund.provider_not_supported"
	codeRefundUnavail  = "refund.engine_unavailable"

	// refundReplayAttempts is how many times the bot repeats a create call
	// that got no answer (the same Idempotency-Key, so the server answers the
	// operation it already started instead of starting another).
	refundReplayAttempts = 3
)

// refundReplayPause separates two repeats of a create call with no answer.
var refundReplayPause = 3 * time.Second

// refundSendWindow bounds the whole send: every attempt (refundCallTimeout
// each) and the pauses between them. A dialog still "sending" after it was
// lost (a bot restart) and may be confirmed again with the same key.
const refundSendWindow = (refundReplayAttempts+1)*refundCallTimeout + refundReplayAttempts*5*time.Second

// refundKeyTag is the part of the operation's key the "yes" button carries,
// so a button of an older final screen never confirms the current one.
func refundKeyTag(key string) string {
	k := strings.TrimPrefix(key, "tg-")
	if len(k) > 12 {
		k = k[:12]
	}
	return k
}

type refundTicket struct {
	ID         uuid.UUID `json:"id"`
	Refundable int64     `json:"refundable"`
	Amount     int64     `json:"amount"` // 0 = everything that is refundable
	Sel        bool      `json:"sel"`
	Number     int64     `json:"number,omitempty"` // the number printed on the ticket
	Seat       string    `json:"seat,omitempty"`
}

// label names the ticket the way the buyer's PDF does: its number and, for a
// seat, the place. The position is the fallback for a ticket without a number.
func (t refundTicket) label(pos int) string {
	out := "№" + strconv.Itoa(pos+1)
	if t.Number > 0 {
		out = "№" + strconv.FormatInt(t.Number, 10)
	}
	if t.Seat != "" {
		out += " (" + t.Seat + ")"
	}
	return out
}

// refundDialog is the state between two messages.
type refundDialog struct {
	OrgID    uuid.UUID      `json:"org_id"`
	OrderID  uuid.UUID      `json:"order_id"`
	Num      int64          `json:"num"`
	Provider string         `json:"provider"`
	Currency string         `json:"currency"`
	Partial  bool           `json:"partial"`
	Tickets  []refundTicket `json:"tickets"`
	Reason   string         `json:"reason"`
	NoLetter bool           `json:"no_letter"`
	Key      string         `json:"key"`
	MsgID    int            `json:"msg_id,omitempty"`
	// SendingAt is when the confirmed operation went to the server.
	SendingAt time.Time `json:"sending_at,omitempty"`
	// RunID names the bot process that sent it: after a restart nothing is
	// with the server for this dialog any more, whatever the clock says.
	RunID string `json:"run_id,omitempty"`
}

// sendingNow reports a dialog whose operation is with the server right now:
// sent by THIS process, within the send window.
func (b *Bot) sendingNow(step string, st refundDialog) bool {
	return step == refStepSending && st.RunID == b.runID && time.Since(st.SendingAt) < refundSendWindow
}

// ─── who sees the button ──────────────────────────────────────────────────────

// canRefund: owner and manager hold refund.create and refund.approve (0129).
func canRefund(id *Identity) bool { return canViewSales(id) }

// refundOffered reports whether the card carries the button: a paid order, a
// role that may refund, in the private chat.
func refundOffered(id *Identity, d openapi.OrderDetail, private bool) bool {
	return private && canRefund(id) && (d.Status == orderStatusPaid || d.Status == orderStatusPartRefund)
}

// withRefundButton adds the "Refund" row next to the resend row.
func (b *Bot) withRefundButton(kb *models.InlineKeyboardMarkup, id *Identity, d openapi.OrderDetail, private bool) *models.InlineKeyboardMarkup {
	if kb == nil || !refundOffered(id, d, private) || len(kb.InlineKeyboard) == 0 {
		return kb
	}
	row := []models.InlineKeyboardButton{{
		Text: b.texts.T(id.Locale(), "bot.ref.btn", nil), CallbackData: "or:f:s:" + d.Id.String(),
	}}
	last := len(kb.InlineKeyboard) - 1
	rows := append([][]models.InlineKeyboardButton{}, kb.InlineKeyboard[:last]...)
	rows = append(rows, row, kb.InlineKeyboard[last])
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── pure helpers ─────────────────────────────────────────────────────────────

func (d refundDialog) selected() []int {
	var out []int
	for i, t := range d.Tickets {
		if t.Sel {
			out = append(out, i)
		}
	}
	return out
}

// amountOf is what one ticket would be refunded for.
func (t refundTicket) amountOf() int64 {
	if t.Amount > 0 && t.Amount < t.Refundable {
		return t.Amount
	}
	return t.Refundable
}

func (d refundDialog) total() int64 {
	var sum int64
	for _, i := range d.selected() {
		sum += d.Tickets[i].amountOf()
	}
	return sum
}

// providerName turns the registry name into a label ("stripe" -> "Stripe").
func providerName(p string) string {
	if p == "" {
		return ""
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

// newRefundDialog builds the state from the server's options: every
// refundable ticket, all selected.
func newRefundDialog(orgID uuid.UUID, num int64, opts openapi.OrderRefundOptions) refundDialog {
	d := refundDialog{
		OrgID: orgID, OrderID: opts.OrderId, Num: num, Provider: opts.Provider,
		Currency: opts.Currency, Partial: opts.PartialRefund,
	}
	for _, t := range opts.Tickets {
		if t.Refundable > 0 && !t.InProgress {
			rt := refundTicket{ID: t.TicketId, Refundable: t.Refundable, Sel: true, Seat: deref(t.SeatLabel)}
			if t.TicketNumber != nil {
				rt.Number = *t.TicketNumber
			}
			d.Tickets = append(d.Tickets, rt)
		}
	}
	return d
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

func (b *Bot) saveRefund(ctx context.Context, tg int64, st refundDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, refundDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: refund dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveIdleRefund ends the dialog when the person goes elsewhere, but never
// one whose operation is with the server: its answer still needs it.
func (b *Bot) leaveIdleRefund(ctx context.Context, tg int64) {
	var st refundDialog
	step, found, _, err := b.dialogs.Load(ctx, tg, refundDialogKind, &st)
	if err == nil && found && b.sendingNow(step, st) {
		return
	}
	b.leaveRefund(ctx, tg)
}

func (b *Bot) leaveRefund(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, refundDialogKind); err != nil {
		b.logger.Warn("eventbot: refund dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── presses ──────────────────────────────────────────────────────────────────

// refundCallback handles every "or:f:<data>" press.
func (b *Bot) refundCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canRefund(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	kind, arg, _ := strings.Cut(data, ":")
	var st refundDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, refundDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: refund dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if found && b.sendingNow(step, st) {
		// The operation is with the server: nothing here may change it or
		// start another one until its answer is in.
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.ref.still_sending", map[string]any{"Num": st.Num}), nil)
		return
	}
	if kind == "s" {
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.startRefund(ctx, chatID, &msgID, from, id, jwt, target)
		return
	}
	if !found || st.OrgID != id.Current.OrgID {
		b.resendGone(ctx, chatID, &msgID, loc, expired)
		return
	}
	if step == refStepSending {
		// A send lost to a restart: confirm again; the same key replays the
		// operation the server may already have started.
		b.showRefundFinal(ctx, chatID, &msgID, id, st, b.texts.T(loc, "bot.ref.send_lost", nil)+"\n\n")
		return
	}
	idx, _ := strconv.Atoi(arg)
	switch kind {
	case "t":
		if idx >= 0 && idx < len(st.Tickets) {
			st.Tickets[idx].Sel = !st.Tickets[idx].Sel
			st.Tickets[idx].Amount = 0
		}
		b.showRefundPick(ctx, chatID, &msgID, id, st, "")
	case "all":
		for i := range st.Tickets {
			st.Tickets[i].Sel, st.Tickets[i].Amount = true, 0
		}
		b.showRefundPick(ctx, chatID, &msgID, id, st, "")
	case "b":
		b.showRefundPick(ctx, chatID, &msgID, id, st, "")
	case "p":
		if sel := st.selected(); st.Partial && len(sel) == 1 {
			st.MsgID = msgID
			b.saveRefund(ctx, from.ID, st, refStepAmount)
			t := st.Tickets[sel[0]]
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.partial_ask", map[string]any{
				"Amount": FormatMoney(t.Refundable, st.Currency, loc),
			}), b.refundBackKeyboard(loc))
		}
	case "go":
		if len(st.selected()) == 0 {
			b.showRefundPick(ctx, chatID, &msgID, id, st, b.texts.T(loc, "bot.ref.none_selected", nil)+"\n\n")
			return
		}
		b.showRefundReason(ctx, chatID, &msgID, id, st)
	case "r":
		if idx >= 1 && idx <= refundQuickReasons {
			st.Reason = b.texts.T(loc, "bot.ref.reason"+arg, nil)
			b.showRefundWord(ctx, chatID, &msgID, id, st, "")
		}
	case "ro":
		st.MsgID = msgID
		b.saveRefund(ctx, from.ID, st, refStepReasonTxt)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.reason_text_ask", nil), b.refundBackKeyboard(loc))
	case "nt":
		st.NoLetter = !st.NoLetter
		st.Key = "" // the summary changed: a new operation
		b.showRefundWord(ctx, chatID, &msgID, id, st, "")
	case "ok":
		// The one press that sends money back. Only valid on the final
		// screen, and only the button of THAT screen (it carries the key's
		// tag): a stale button of an earlier screen sends nothing.
		stale := b.texts.T(loc, "bot.ref.stale_btn", nil) + "\n\n"
		if step == refStepFinal && st.Key != "" && arg != refundKeyTag(st.Key) {
			b.showRefundFinal(ctx, chatID, &msgID, id, st, stale)
			return
		}
		if step != refStepFinal || st.Key == "" {
			b.showRefundPick(ctx, chatID, &msgID, id, st, stale)
			return
		}
		b.startSendRefund(ctx, chatID, msgID, from, id, st)
	case "no":
		b.leaveRefund(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.cancelled", nil), b.resendResultKeyboard(loc, st.OrderID))
	}
}

// refundRouteKey is the explanation for a route the bot cannot refund
// through ("" for arena_provider, which opens the dialog).
func refundRouteKey(route string) string {
	switch route {
	case refundRouteArena:
		return ""
	case refundRouteSite:
		return "bot.ref.site"
	case "unsupported":
		return "bot.ref.unsupported"
	case "none":
		// Arena took no money for this order (free, invitation).
		return "bot.ref.no_payment"
	}
	// unknown_provider, or a route this bot does not know yet: never claim
	// that everything was already refunded.
	return "bot.ref.unknown_provider"
}

// startRefund opens the dialog after reading the options afresh.
func (b *Bot) startRefund(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, orderID uuid.UUID) {
	loc := id.Locale()
	back := b.resendResultKeyboard(loc, orderID)
	if chatID != from.ID {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.private_only", nil), back)
		return
	}
	detail, err := b.arena.GetOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
		return
	}
	opts, err := b.arena.RefundOptions(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:v:"+orderID.String())
		return
	}
	num := map[string]any{"Num": detail.SystemId, "Provider": providerName(opts.Provider)}
	if key := refundRouteKey(opts.Route); key != "" {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, num), back)
		return
	}
	st := newRefundDialog(id.Current.OrgID, detail.SystemId, opts)
	if len(st.Tickets) == 0 {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.nothing", num), back)
		return
	}
	b.showRefundPick(ctx, chatID, editMsgID, id, st, "")
}

// ─── screens ──────────────────────────────────────────────────────────────────

func (b *Bot) refundBackKeyboard(loc string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:f:b"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
}

func (b *Bot) showRefundPick(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st refundDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	st.Key = ""
	b.saveRefund(ctx, id.Link.TelegramUserID, st, refStepPick)
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for i, t := range st.Tickets {
		mark := "☐"
		if t.Sel {
			mark = "☑"
		}
		label := mark + " " + t.label(i) + " · " + FormatMoney(t.amountOf(), st.Currency, loc)
		row = append(row, models.InlineKeyboardButton{Text: label, CallbackData: "or:f:t:" + strconv.Itoa(i)})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	ctl := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ref.select_all", nil), CallbackData: "or:f:all"}}
	if sel := st.selected(); st.Partial && len(sel) == 1 {
		ctl = append(ctl, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.ref.partial_btn", nil), CallbackData: "or:f:p"})
	}
	rows = append(rows, ctl,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ref.continue", map[string]any{
			"Amount": FormatMoney(st.total(), st.Currency, loc)}), CallbackData: "or:f:go"}},
		[]models.InlineKeyboardButton{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:v:" + st.OrderID.String()},
			{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
		})
	text := note + b.texts.T(loc, "bot.ref.pick", map[string]any{"Num": st.Num, "Provider": Esc(providerName(st.Provider))})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showRefundReason(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st refundDialog) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveRefund(ctx, id.Link.TelegramUserID, st, refStepReason)
	var rows [][]models.InlineKeyboardButton
	for i := 1; i <= refundQuickReasons; i++ {
		n := strconv.Itoa(i)
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ref.reason"+n, nil), CallbackData: "or:f:r:" + n}})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ref.reason_other_btn", nil), CallbackData: "or:f:ro"}},
		[]models.InlineKeyboardButton{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:f:b"},
			{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
		})
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.reason_ask", nil), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) refundWord(loc string) string { return b.texts.T(loc, "bot.ref.word", nil) }

// isRefundWord accepts the localized word or the English one, in any language.
func (b *Bot) isRefundWord(loc, text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "\"'«».!"))
	return strings.EqualFold(text, b.refundWord(loc)) || strings.EqualFold(text, "refund")
}

func (b *Bot) showRefundWord(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st refundDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	if st.Key == "" {
		st.Key = "tg-" + uuid.NewString()
	}
	b.saveRefund(ctx, id.Link.TelegramUserID, st, refStepWord)
	letterKey := "bot.ref.letter_on_btn"
	if st.NoLetter {
		letterKey = "bot.ref.letter_off_btn"
	}
	text := note + b.texts.T(loc, "bot.ref.confirm", map[string]any{
		"Num": st.Num, "N": len(st.selected()), "Amount": FormatMoney(st.total(), st.Currency, loc),
		"Provider": Esc(providerName(st.Provider)), "Reason": Esc(st.Reason), "Word": b.refundWord(loc),
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, letterKey, nil), CallbackData: "or:f:nt"}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:f:b"}, {Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// ticketsLine names the selected tickets for the final screen (at most 10).
func (d refundDialog) ticketsLine() string {
	var parts []string
	for n, i := range d.selected() {
		if n == 10 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, d.Tickets[i].label(i))
	}
	return strings.Join(parts, ", ")
}

// showRefundFinal is the screen after the typed word: the whole operation in
// one place and two buttons. Money moves only on the "yes" press.
func (b *Bot) showRefundFinal(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st refundDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	if st.Key == "" {
		st.Key = "tg-" + uuid.NewString()
	}
	b.saveRefund(ctx, id.Link.TelegramUserID, st, refStepFinal)
	letter := "bot.ref.letter_yes"
	if st.NoLetter {
		letter = "bot.ref.letter_no"
	}
	amount := FormatMoney(st.total(), st.Currency, loc)
	text := note + b.texts.T(loc, "bot.ref.final", map[string]any{
		"Num": st.Num, "N": len(st.selected()), "Tickets": Esc(st.ticketsLine()), "Amount": amount,
		"Provider": Esc(providerName(st.Provider)), "Letter": b.texts.T(loc, letter, nil),
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ref.yes_btn", map[string]any{"Amount": amount}), CallbackData: "or:f:ok:" + refundKeyTag(st.Key)}},
		{{Text: b.texts.T(loc, "bot.ref.no_btn", nil), CallbackData: "or:f:no"}},
	}})
}

// ─── the call ─────────────────────────────────────────────────────────────────

// startSendRefund hands the confirmed operation to the server OFF the update
// worker: the call may wait minutes for the payment provider, and the bot
// must keep answering everyone meanwhile. The dialog is "sending" until the
// answer is in, so no press can change or repeat it.
func (b *Bot) startSendRefund(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, st refundDialog) {
	loc := id.Locale()
	st.SendingAt = time.Now().UTC()
	st.RunID = b.runID
	st.MsgID = msgID
	b.saveRefund(ctx, from.ID, st, refStepSending)
	// The buttons go away while the server works: a second press cannot start
	// anything, and the person knows it is under way.
	b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.sending", nil), nil)
	tg := from.ID
	b.goBackground("refund send", func() {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundSendWindow)
		defer cancel()
		b.sendRefund(wctx, chatID, msgID, tg, id, st)
	}, func() {
		// Whether the call went out is unknown: the dialog goes back to the
		// final screen with the same key, which replays rather than repeats.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if b.ownsSending(cctx, tg, st.Key) {
			st.SendingAt = time.Time{}
			b.saveRefund(cctx, tg, st, refStepFinal)
		}
		b.reply(cctx, chatID, &msgID, b.texts.T(id.Locale(), "bot.ref.err_timeout", nil), b.resendResultKeyboard(id.Locale(), st.OrderID))
	})
}

// ownsSending reports whether the stored dialog is still the operation this
// send is for (the person may have opened another refund after a restart).
func (b *Bot) ownsSending(ctx context.Context, tg int64, key string) bool {
	var cur refundDialog
	step, found, _, err := b.dialogs.Load(ctx, tg, refundDialogKind, &cur)
	return err == nil && found && step == refStepSending && cur.Key == key
}

func (b *Bot) sendRefund(ctx context.Context, chatID int64, msgID int, tg int64, id *Identity, st refundDialog) {
	loc := id.Locale()
	back := b.resendResultKeyboard(loc, st.OrderID)
	edit := &msgID
	req := openapi.CreateOrderRefundsRequest{Reason: st.Reason}
	notify := !st.NoLetter
	req.NotifyBuyer = &notify
	for _, i := range st.selected() {
		t := st.Tickets[i]
		// The amount on the screen is the amount sent, always. Left out, the
		// server would refund whatever is left at that moment, which may
		// differ from what the person confirmed.
		amount := t.amountOf()
		item := struct {
			Amount   *int64             `json:"amount"`
			TicketId openapi_types.UUID `json:"ticket_id"`
		}{TicketId: t.ID, Amount: &amount}
		req.Items = append(req.Items, item)
	}
	var (
		res openapi.CreateOrderRefundsResponse
		err error
	)
	// A call with no answer (the bot's wait ended, the connection dropped) is
	// repeated with the SAME key: the server answers the operation it already
	// started, it never starts a second one. Every attempt carries a freshly
	// minted token, so a long wait never ends on an expired one.
	for attempt := 0; attempt <= refundReplayAttempts; attempt++ {
		jwt, merr := b.minter.Mint(id.Link.UserID)
		if merr != nil {
			err = merr
			break
		}
		res, err = b.arena.CreateRefunds(ctx, jwt, st.OrgID, st.OrderID, st.Key, req)
		var ae *APIError
		if err == nil || errors.As(err, &ae) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(refundReplayPause):
		}
	}
	// The answer is recorded on a context of its own: the send's window may
	// be spent.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	ours := b.ownsSending(rctx, tg, st.Key)
	var ae *APIError
	switch {
	case err == nil:
		if ours {
			b.leaveRefund(rctx, tg)
		}
		b.reply(rctx, chatID, edit, b.refundResultText(loc, st, res), back)
	case errors.As(err, &ae):
		// The server ANSWERED: nothing was refunded by this call. 5xx other
		// than the documented 503 is an unknown state, so the dialog goes
		// back to the final screen and the same key may be pressed again.
		key := b.refundErrorKey(ae)
		if key == "" {
			if ours {
				st.SendingAt = time.Time{}
				b.saveRefund(rctx, tg, st, refStepFinal)
			}
			b.reply(rctx, chatID, edit, b.texts.T(loc, "bot.ref.err_timeout", nil), back)
			return
		}
		if ours {
			b.leaveRefund(rctx, tg)
		}
		b.reply(rctx, chatID, edit, b.texts.T(loc, key, map[string]any{"Num": st.Num, "Provider": Esc(providerName(st.Provider))}), back)
	default:
		// No answer at all after the repeats: the refund may well be under
		// way. Keep the dialog (and its key) and say so; never "failed".
		b.logger.Warn("eventbot: refund call got no answer", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		if ours {
			st.SendingAt = time.Time{}
			b.saveRefund(rctx, tg, st, refStepFinal)
		}
		b.reply(rctx, chatID, edit, b.texts.T(loc, "bot.ref.err_timeout", nil), back)
	}
}

// refundErrorKey picks the text for an answer of the create route; "" means
// the answer says nothing about the refund (an internal error), so the person
// is told to look at the order instead of that nothing happened.
func (b *Bot) refundErrorKey(ae *APIError) string {
	switch {
	case ae.Status == http.StatusConflict && ae.Code == codeRefundProgress:
		return "bot.ref.err_progress"
	case ae.Status == http.StatusConflict && ae.Code == codeRefundSite:
		return "bot.ref.site"
	case ae.Status == http.StatusConflict && ae.Code == codeRefundNotFit:
		return "bot.ref.err_ticket"
	case ae.Status == http.StatusConflict:
		return "bot.ref.err_changed"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == codeRefundUnsupp:
		return "bot.ref.err_unsupported"
	case ae.Status == http.StatusUnprocessableEntity:
		return "bot.ref.err_unsupported"
	case ae.Status == http.StatusBadRequest:
		return "bot.ref.err_changed"
	case ae.Status == http.StatusServiceUnavailable && ae.Code == codeRefundUnavail:
		return "bot.ref.err_unavailable"
	case ae.Status == http.StatusNotFound:
		return "bot.ref.err_changed"
	case ae.Status == http.StatusForbidden || ae.Status == http.StatusUnauthorized:
		// Refused before anything was written: say so, never "taking longer".
		return "bot.ref.err_forbidden"
	}
	return ""
}

// refundResultText lists what each refund came to.
func (b *Bot) refundResultText(loc string, st refundDialog, res openapi.CreateOrderRefundsResponse) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.ref.done_head", map[string]any{"Num": st.Num}))
	for i, rf := range res.Refunds {
		var status string
		switch rf.State {
		case "succeeded":
			status = b.texts.T(loc, "bot.ref.st_succeeded", nil)
		case "provider_pending":
			if rf.ProviderRefundId == nil {
				// Not sent yet (the server queued it for the sweep): never
				// "accepted by the provider".
				status = b.texts.T(loc, "bot.ref.st_queued", nil)
			} else {
				status = b.texts.T(loc, "bot.ref.st_pending", map[string]any{"Provider": Esc(providerName(st.Provider))})
			}
		case "failed":
			status = b.texts.T(loc, "bot.ref.st_failed", map[string]any{"Why": Esc(deref(rf.FailureReason))})
		case "manual_review":
			status = b.texts.T(loc, "bot.ref.st_review", nil)
		default:
			status = b.texts.T(loc, "bot.ref.st_other", map[string]any{"State": Esc(rf.State)})
		}
		sb.WriteString("\n" + b.texts.T(loc, "bot.ref.line", map[string]any{
			"N": i + 1, "Amount": FormatMoney(rf.Amount, rf.Currency, loc), "Status": status,
		}))
	}
	return sb.String()
}

// ─── typed text ───────────────────────────────────────────────────────────────

// refundText takes the text typed while the dialog waits for a part-amount, a
// reason or the confirm word. It reports whether the text was taken.
func (b *Bot) refundText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if chatID != from.ID {
		return false // the dialog lives in the private chat only; a group's text is not an answer
	}
	var st refundDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, refundDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: refund dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
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
	// A refund with the server waits for buttons only: a typed text then
	// belongs to whatever other dialog asked for it.
	if !found || (step != refStepAmount && step != refStepReasonTxt && step != refStepWord && step != refStepFinal) {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canRefund(id) {
		b.leaveRefund(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	text = strings.TrimSpace(text)
	switch step {
	case refStepAmount:
		sel := st.selected()
		minor, ok := ParsePriceMinor(text)
		if len(sel) != 1 || !ok || minor <= 0 || minor > st.Tickets[sel[0]].Refundable {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ref.partial_bad", nil), b.refundBackKeyboard(loc))
			return true
		}
		st.Tickets[sel[0]].Amount = minor
		b.showRefundPick(ctx, chatID, edit, id, st, "")
	case refStepReasonTxt:
		if text == "" || len([]rune(text)) > maxRefundReasonLen {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ref.reason_bad", nil), b.refundBackKeyboard(loc))
			return true
		}
		st.Reason = text
		b.showRefundWord(ctx, chatID, edit, id, st, "")
	case refStepWord:
		if !b.isRefundWord(loc, text) {
			b.showRefundWord(ctx, chatID, edit, id, st, b.texts.T(loc, "bot.ref.word_wrong", map[string]any{"Word": b.refundWord(loc)})+"\n\n")
			return true
		}
		b.showRefundFinal(ctx, chatID, edit, id, st, "")
	case refStepFinal:
		b.showRefundFinal(ctx, chatID, edit, id, st, "") // only the buttons send money
	}
	return true
}
