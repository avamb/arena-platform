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
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

	maxRefundReasonLen = 300
	refundQuickReasons = 3

	// Routes (spec 36 section 6.2) and the API codes that have their own text.
	refundRouteArena   = "arena_provider"
	refundRouteSite    = "seller_site"
	codeRefundProgress = "refund.in_progress"
)

type refundTicket struct {
	ID         uuid.UUID `json:"id"`
	Refundable int64     `json:"refundable"`
	Amount     int64     `json:"amount"` // 0 = everything that is refundable
	Sel        bool      `json:"sel"`
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
			d.Tickets = append(d.Tickets, refundTicket{ID: t.TicketId, Refundable: t.Refundable, Sel: true})
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
	if kind == "s" {
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.startRefund(ctx, chatID, &msgID, from, id, jwt, target)
		return
	}
	var st refundDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, refundDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: refund dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if !found || st.OrgID != id.Current.OrgID {
		b.resendGone(ctx, chatID, &msgID, loc, expired)
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
	}
	_ = step
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
	switch opts.Route {
	case refundRouteArena:
	case refundRouteSite:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.site", num), back)
		return
	case "unsupported":
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.unsupported", num), back)
		return
	default:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.nothing", num), back)
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
		label := mark + " " + strconv.Itoa(i+1) + " · " + FormatMoney(t.amountOf(), st.Currency, loc)
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

// ─── the call ─────────────────────────────────────────────────────────────────

func (b *Bot) sendRefund(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st refundDialog) {
	loc := id.Locale()
	req := openapi.CreateOrderRefundsRequest{Reason: st.Reason}
	notify := !st.NoLetter
	req.NotifyBuyer = &notify
	for _, i := range st.selected() {
		t := st.Tickets[i]
		item := struct {
			Amount   *int64             `json:"amount"`
			TicketId openapi_types.UUID `json:"ticket_id"`
		}{TicketId: t.ID}
		if t.Amount > 0 && t.Amount < t.Refundable {
			amount := t.Amount
			item.Amount = &amount
		}
		req.Items = append(req.Items, item)
	}
	res, err := b.arena.CreateRefunds(ctx, jwt, st.OrgID, st.OrderID, st.Key, req)
	back := b.resendResultKeyboard(loc, st.OrderID)
	switch {
	case err == nil:
		b.leaveRefund(ctx, from.ID)
		b.reply(ctx, chatID, editMsgID, b.refundResultText(loc, st, res), back)
	case IsAPIError(err, http.StatusConflict):
		b.leaveRefund(ctx, from.ID)
		key := "bot.ref.err_changed"
		switch APIErrorCode(err) {
		case codeRefundProgress:
			key = "bot.ref.err_progress"
		case codeSellerSiteOrder:
			key = "bot.ref.site"
		}
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, map[string]any{"Num": st.Num, "Provider": ""}), back)
	default:
		// Keep the dialog (and its key): pressing the word again replays or retries.
		b.ecError(ctx, chatID, editMsgID, id, err, "or:v:"+st.OrderID.String())
	}
}

// refundResultText lists what each refund came to.
func (b *Bot) refundResultText(loc string, st refundDialog, res openapi.CreateOrderRefundsResponse) string {
	head := b.texts.T(loc, "bot.ref.done_head", map[string]any{"Num": st.Num})
	return head + b.refundLines(loc, Esc(providerName(st.Provider)), res.Refunds)
}

// refundLines is one line per refund with its state in words; provider is
// already escaped.
func (b *Bot) refundLines(loc, provider string, refunds []openapi.OrderRefundItem) string {
	var sb strings.Builder
	for i, rf := range refunds {
		var status string
		switch rf.State {
		case "succeeded":
			status = b.texts.T(loc, "bot.ref.st_succeeded", nil)
		case "provider_pending":
			status = b.texts.T(loc, "bot.ref.st_pending", map[string]any{"Provider": provider})
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
	if !found || (step != refStepAmount && step != refStepReasonTxt && step != refStepWord) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canRefund(id) {
		b.leaveRefund(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ref.private_only", nil), b.refundBackKeyboard(loc))
		return true
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
		b.sendRefund(ctx, chatID, edit, from, id, jwt, st)
	}
	return true
}
