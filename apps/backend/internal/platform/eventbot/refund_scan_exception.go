package eventbot

// refund_scan_exception.go — the platform operator's "allow a refund of this
// scanned ticket" (owner decision 2026-10-13). A ticket scanned at the gate is
// never refunded (refunds/scanned.go), but a show stopped half-way or a scan
// made by mistake needs a way out: POST .../scan-refund-exception lets ONE
// refund of that ticket through for 24 hours. Only a platform superadmin may
// grant it, so only they see the button; everybody else reads the plain
// refusal and what to do next.
//
// The dialog, kind "scanexc" in bot_dialogs, has two steps:
//
//	offer   the buttons are on screen; their indexes name the tickets
//	reason  the typed reason is awaited (the note the owners receive)
//
//	or:f:x:<i>  allow ticket i      or:f:xn  cancel
//
// After the grant the operator presses "Continue the refund", which opens
// the refund afresh: the server now reports the ticket refundable.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	scanExcDialogKind = "scanexc"
	scanExcStepOffer  = "offer"
	scanExcStepReason = "reason"

	// scanExcHours is how long the exception lives: the API's maximum, the
	// one the button names.
	scanExcHours = 24
	// maxScanExcNote is the API's limit for the note.
	maxScanExcNote = 1000
	// maxScanExcButtons bounds the buttons one screen offers.
	maxScanExcButtons = 5

	codeTicketNotScanned = "refund.ticket_not_scanned"
	codeScanExcExists    = "refund.scan_exception_exists"
)

// scanExcTicket is a scanned ticket the operator may allow a refund of.
type scanExcTicket struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"` // "№349", as the buyer's PDF prints it
}

type scanExcDialog struct {
	OrgID   uuid.UUID       `json:"org_id"`
	OrderID uuid.UUID       `json:"order_id"`
	Num     int64           `json:"num"`
	Tickets []scanExcTicket `json:"tickets"`
	Pick    int             `json:"pick"`
}

// scannedTickets lists the tickets the server will not refund because they
// were already scanned at the gate.
func scannedTickets(opts openapi.OrderRefundOptions) []scanExcTicket {
	var out []scanExcTicket
	for i, t := range opts.Tickets {
		if t.NotRefundableReason == nil || *t.NotRefundableReason != "ticket_scanned" {
			continue
		}
		rt := refundTicket{Seat: deref(t.SeatLabel)}
		if t.TicketNumber != nil {
			rt.Number = *t.TicketNumber
		}
		out = append(out, scanExcTicket{ID: uuid.UUID(t.TicketId), Label: rt.label(i)})
	}
	return out
}

// scannedTicketsLine names those tickets ("№349, №350"; "" when none).
func scannedTicketsLine(opts openapi.OrderRefundOptions) string {
	var parts []string
	for _, t := range scannedTickets(opts) {
		parts = append(parts, t.Label)
	}
	return strings.Join(parts, ", ")
}

// withScanExcRows adds one "Allow a refund of ticket №349 (24 h)" row per
// scanned ticket — for a platform superadmin only — and stores the offer so
// a press can name its ticket by index. Anyone else gets kb unchanged.
func (b *Bot) withScanExcRows(ctx context.Context, kb *models.InlineKeyboardMarkup, id *Identity, orgID, orderID uuid.UUID, num int64, tickets []scanExcTicket) *models.InlineKeyboardMarkup {
	if id == nil || !id.Superadmin || len(tickets) == 0 || kb == nil {
		return kb
	}
	if len(tickets) > maxScanExcButtons {
		tickets = tickets[:maxScanExcButtons]
	}
	st := scanExcDialog{OrgID: orgID, OrderID: orderID, Num: num, Tickets: tickets, Pick: -1}
	if !b.saveScanExc(ctx, id.Link.TelegramUserID, st, scanExcStepOffer) {
		return kb
	}
	loc := id.Locale()
	var rows [][]models.InlineKeyboardButton
	for i, t := range tickets {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         b.texts.T(loc, "bot.ref.exc_btn", map[string]any{"Ticket": t.Label, "Hours": scanExcHours}),
			CallbackData: "or:f:x:" + strconv.Itoa(i),
		}})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: append(rows, kb.InlineKeyboard...)}
}

func (b *Bot) saveScanExc(ctx context.Context, tg int64, st scanExcDialog, step string) bool {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, scanExcDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: scan exception dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return false
	}
	return true
}

// leaveScanExc ends the dialog: any press but its own, and every command.
func (b *Bot) leaveScanExc(ctx context.Context, tg int64) {
	if b.dialogs == nil {
		return
	}
	if err := b.dialogs.Delete(ctx, tg, scanExcDialogKind); err != nil {
		b.logger.Warn("eventbot: scan exception dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// scanExcCallback handles "or:f:x:<i>" and "or:f:xn".
func (b *Bot) scanExcCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, kind, arg string) {
	loc := id.Locale()
	if !id.Superadmin {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.exc_forbidden", nil), b.backKeyboard(loc, "home"))
		return
	}
	var st scanExcDialog
	_, found, expired, err := b.dialogs.Load(ctx, from.ID, scanExcDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: scan exception dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if !found || id.Current == nil || st.OrgID != id.Current.OrgID {
		b.resendGone(ctx, chatID, &msgID, loc, expired)
		return
	}
	if kind == "xn" {
		b.leaveScanExc(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.exc_cancelled", nil), b.resendResultKeyboard(loc, st.OrderID))
		return
	}
	i, ok := ParseIndex(arg, len(st.Tickets))
	if !ok {
		return
	}
	st.Pick = i
	if !b.saveScanExc(ctx, from.ID, st, scanExcStepReason) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.exc_failed", nil), b.resendResultKeyboard(loc, st.OrderID))
		return
	}
	b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.exc_ask", map[string]any{
		"Ticket": Esc(st.Tickets[i].Label), "Hours": scanExcHours, "Chars": maxScanExcNote,
	}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ref.no_btn", nil), CallbackData: "or:f:xn"}},
	}})
}

// scanExcText takes the typed reason and grants the exception. It reports
// whether the text was the dialog's.
func (b *Bot) scanExcText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if chatID != from.ID || b.dialogs == nil {
		return false
	}
	var st scanExcDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, scanExcDialogKind, &st)
	if err != nil || !found || step != scanExcStepReason || st.Pick < 0 || st.Pick >= len(st.Tickets) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || !id.Superadmin || id.Current == nil || id.Current.OrgID != st.OrgID {
		b.leaveScanExc(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	ticket := st.Tickets[st.Pick]
	vars := map[string]any{"Ticket": Esc(ticket.Label), "Hours": scanExcHours, "Chars": maxScanExcNote}
	cancel := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ref.no_btn", nil), CallbackData: "or:f:xn"}},
	}}
	note := strings.TrimSpace(text)
	if note == "" || len([]rune(note)) > maxScanExcNote {
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.ref.exc_bad", vars), cancel)
		return true
	}
	exc, err := b.arena.GrantScanException(ctx, jwt, st.OrgID, ticket.ID, scanExcHours, note)
	if err != nil {
		key, keep := scanExcErrorKey(err)
		if !keep {
			b.leaveScanExc(ctx, from.ID)
		}
		kb := b.resendResultKeyboard(loc, st.OrderID)
		if keep {
			kb = cancel // the reason may be typed again
		}
		if key == "bot.ref.exc_not_scanned" {
			kb = b.scanExcContinueKeyboard(loc, st.OrderID)
		}
		if ae, ok := err.(*APIError); !ok || ae.Status >= http.StatusInternalServerError {
			b.logger.Warn("eventbot: scan exception grant failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", routeOnly(err)))
		}
		b.reply(ctx, chatID, nil, b.texts.T(loc, key, vars), kb)
		return true
	}
	b.leaveScanExc(ctx, from.ID)
	key := "bot.ref.exc_done"
	if exc.Replayed {
		key = "bot.ref.exc_replayed"
	}
	b.reply(ctx, chatID, nil, b.texts.T(loc, key, vars), b.scanExcContinueKeyboard(loc, st.OrderID))
	return true
}

// scanExcErrorKey picks the text for a refused grant, and whether the
// question stays open (a failure worth another try).
func scanExcErrorKey(err error) (string, bool) {
	ae, ok := err.(*APIError)
	if !ok {
		return "bot.ref.exc_failed", true
	}
	switch {
	case ae.Status == http.StatusForbidden || ae.Status == http.StatusUnauthorized:
		return "bot.ref.exc_forbidden", false
	case ae.Status == http.StatusConflict && ae.Code == codeTicketNotScanned:
		return "bot.ref.exc_not_scanned", false
	case ae.Status == http.StatusConflict:
		return "bot.ref.exc_exists", false
	case ae.Status == http.StatusNotFound:
		return "bot.ref.exc_not_found", false
	}
	return "bot.ref.exc_failed", true
}

// scanExcContinueKeyboard opens the refund afresh, or goes back to the order.
func (b *Bot) scanExcContinueKeyboard(loc string, orderID uuid.UUID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ref.exc_continue_btn", nil), CallbackData: "or:f:s:" + orderID.String()}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:v:" + orderID.String()}},
	}}
}
