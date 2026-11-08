package eventbot

// reissue.go — "Replace tickets" (spec 63, stages R-4 and R-5, decisions
// R-1..R-19). The letter went to a wrong address: every active ticket is
// closed and a new ticket (new number, new barcode, same seat) goes to the
// right address. The bot computes nothing: it reads the reissue options and
// shows what the server says.
//
// One dialog, kind "reissue" in bot_dialogs, serves two subjects:
//
//   - an ORDER (the order card's «Заменить билеты», or:x:s:<order>);
//   - an INVITATION issued before spec 46 — a complimentary issuance without
//     an order (the earlier invitation's card, «Заменить приглашение»,
//     or:x:si:<invitation>). It has no order number, no "order address"
//     resend and no payment provider, so it skips the case question and its
//     texts say "invitation" (the bot.reissue.inv_* variants, rxKey).
//
// Steps:
//
//	case    (orders only) which address does the buyer write from? "From
//	        the order's address" hands over to the resend dialog (a one-time
//	        address, the barcodes stay); "From another address" goes on here
//	email   the right address, typed (the current address masked, and for an
//	        order the buyer's name and phone, are shown for comparison)
//	reason  three buttons
//	note    an optional note for the journal, typed, or "No note"
//	check   what will happen; its confirm press ONLY opens the word step
//	word    the typed word (ЗАМЕНИТЬ БИЛЕТЫ / REISSUE TICKETS / REEMITIR
//	        ENTRADAS, English accepted in any language); a wrong word
//	        changes nothing and asks again
//	pending an earlier reissue has not been sent yet: its state and "Send the
//	        letter again"
//
// The Idempotency-Key ("tg-<uuid>") is minted when the check screen is first
// shown and kept in the dialog, so a repeated word, a double press or a bot
// restart replays the first answer instead of replacing the tickets twice.
// Going back to the start drops it: the next check screen is a new operation.
//
// Contacts are shown and typed only in the private chat (the buttons are
// offered there only) and never logged. The presses live inside the Orders
// prefix, so the orders list behind the card stays:
//
//	or:x:s:<order>  open for an order     or:x:si:<inv>  open for an invitation
//	or:x:fo         from the order's address
//	or:x:fn         from another address  or:x:r:<1|2|3>  a reason
//	or:x:nn         no note               or:x:ok  confirm (opens the word step)
//	or:x:b          back to the start     or:x:no  cancel
//	or:x:rs         send the letter of a pending reissue again

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	reissueDialogKind = "reissue"
	rxStepCase        = "case"
	rxStepEmail       = "email"
	rxStepReason      = "reason"
	rxStepNote        = "note"
	rxStepCheck       = "check"
	rxStepWord        = "word"
	rxStepPending     = "pending"

	// maxReissueNoteLen is the longest note the API accepts.
	maxReissueNoteLen = 500

	reissueRouteSite = "seller_site"

	// API codes of the reissue routes that have their own text.
	codeReissueNothing     = "reissue.nothing_to_reissue"
	codeReissueTicket      = "reissue.ticket_not_reissuable"
	codeReissueClosed      = "reissue.session_closed"
	codeReissueProgress    = "reissue.in_progress"
	codeReissueKeyReused   = "reissue.idempotency_key_reused"
	codeReissueApplied     = "reissue.already_applied"
	codeReissueNotFound    = "reissue.not_found"
	codeReissueEmail       = "reissue.invalid_email"
	codeReissueReason      = "reissue.invalid_reason"
	codeReissueNote        = "reissue.note_too_long"
	codeReissueNotIssued   = "reissue.invitation_not_issued"
	codeComplimentaryGone  = "complimentary.not_found"
	invitationStatusIssued = "issued"
)

// reissueReasons are the API's reason codes, in the order of the buttons
// (or:x:r:1 .. or:x:r:3).
var reissueReasons = []string{"sent_to_wrong_address", "forwarded_to_stranger", "other"}

// reissueInvVariants are the texts an invitation says in its own words: the
// order's wording names an order number, an order address or the order's
// history. Each has a bot.reissue.inv_<name> key.
var reissueInvVariants = map[string]bool{
	"email_ask": true, "reason_ask": true, "note_ask": true, "check": true, "word_ask": true,
	"done_head": true, "done_letter": true, "cancelled": true, "pending_head": true, "pending_queued": true,
	"err_nothing": true, "err_closed": true, "err_in_progress": true, "err_ticket_review": true,
	"err_ticket_refund": true, "err_applied": true, "err_refused": true, "err_reason": true, "resent": true,
}

// rxKey is the text of key for the dialog's subject: an invitation's own
// wording where it has one.
func rxKey(inv bool, key string) string {
	if !inv {
		return key
	}
	if name, ok := strings.CutPrefix(key, "bot.reissue."); ok && reissueInvVariants[name] {
		return "bot.reissue.inv_" + name
	}
	return key
}

// reissueScanned is a ticket whose code was already used at the door: the
// check screen warns that the new ticket admits too (decision R-6).
type reissueScanned struct {
	Number int64  `json:"number"`
	When   string `json:"when"`
}

// reissueDialog is the state between two messages.
type reissueDialog struct {
	OrgID   uuid.UUID `json:"org_id"`
	OrderID uuid.UUID `json:"order_id"` // the order; zero for an invitation
	// InvitationID is the complimentary issuance; zero for an order.
	InvitationID uuid.UUID        `json:"invitation_id,omitempty"`
	Num          int64            `json:"num"`
	Tickets      int              `json:"tickets"` // tickets that will be replaced
	Scanned      []reissueScanned `json:"scanned,omitempty"`
	Masked       string           `json:"masked"` // the current address, masked; "" when there is none
	Buyer        string           `json:"buyer"`  // name, for comparison (orders)
	Phone        string           `json:"phone"`  // phone, for comparison (orders)
	Provider     string           `json:"provider,omitempty"`
	// ProviderKeeps: the payment provider keeps the earlier address (it
	// cannot be told), so the organizer may have to resend its document.
	ProviderKeeps bool   `json:"provider_keeps,omitempty"`
	Email         string `json:"email,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Note          string `json:"note,omitempty"`
	Key           string `json:"key,omitempty"`
	// Attempted: the request went out and no answer came back. The same key
	// replays it, so nothing may change the operation any more.
	Attempted bool      `json:"attempted,omitempty"`
	PendingID uuid.UUID `json:"pending_id,omitempty"`
	MsgID     int       `json:"msg_id,omitempty"`
}

// inv reports a dialog about an invitation rather than an order.
func (st reissueDialog) inv() bool { return st.InvitationID != uuid.Nil }

// backTo is where "Back" of a result leads: the order card, or the earlier
// invitations' list.
func (st reissueDialog) backTo() string {
	if st.inv() {
		return "iv:lg"
	}
	return "or:v:" + st.OrderID.String()
}

// ─── who sees the button ──────────────────────────────────────────────────────

// reissueOffered reports whether the card carries the button: the resend
// rule (spec 63 section 7.3) — a paid or partly refunded order with an active
// ticket, owner or manager, in the private chat.
func reissueOffered(id *Identity, d openapi.OrderDetail, private bool) bool {
	return resendOffered(id, d, private)
}

// withReissueButton adds the "Replace tickets" row above the card's last row.
func (b *Bot) withReissueButton(kb *models.InlineKeyboardMarkup, id *Identity, d openapi.OrderDetail, private bool) *models.InlineKeyboardMarkup {
	if kb == nil || !reissueOffered(id, d, private) || len(kb.InlineKeyboard) == 0 {
		return kb
	}
	row := []models.InlineKeyboardButton{{
		Text: b.texts.T(id.Locale(), "bot.reissue.btn", nil), CallbackData: "or:x:s:" + d.Id.String(),
	}}
	last := len(kb.InlineKeyboard) - 1
	rows := append([][]models.InlineKeyboardButton{}, kb.InlineKeyboard[:last]...)
	rows = append(rows, row, kb.InlineKeyboard[last])
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// invitationReissueOffered reports whether an earlier invitation's card
// carries «Заменить приглашение»: still issued with an active ticket, owner
// or manager, in the private chat.
func invitationReissueOffered(id *Identity, it openapi.ComplimentaryListItem, private bool) bool {
	if !private || !canResend(id) || it.Status != invitationStatusIssued {
		return false
	}
	for _, t := range it.Tickets {
		if t.Status == "active" {
			return true
		}
	}
	return false
}

// ─── pure decisions ───────────────────────────────────────────────────────────

// reissueStartScreen is what the first press on an order shows, from the
// options alone: "site" (a live selling site writes the letters), "pending"
// (an earlier reissue waits for its letter), "blocked" (the API would refuse
// now, key is the text) or "case" (the dialog opens).
func reissueStartScreen(o openapi.ReissueOptions) (screen, key string) {
	if o.Route == reissueRouteSite {
		return "site", "bot.reissue.site"
	}
	return reissueStartFrom(o.PendingReissue, o.Blocked, o.Tickets, "case")
}

// invitationStartScreen is the same for an invitation, which has no case
// question: an open dialog starts at the address ("email").
func invitationStartScreen(o openapi.InvitationReissueOptions) (screen, key string) {
	return reissueStartFrom(o.PendingReissue, o.Blocked, o.Tickets, "email")
}

func reissueStartFrom(pending *openapi.ReissuePending, blocked *string, tickets []openapi.ReissueTicketOption, open string) (screen, key string) {
	if pending != nil {
		return "pending", ""
	}
	if blocked != nil && *blocked != "" {
		return "blocked", reissueRefusalKey(*blocked, reissueBlockingReason(tickets))
	}
	if reissuableCount(tickets) == 0 {
		return "blocked", "bot.reissue.err_nothing"
	}
	return open, ""
}

// reissueRefusalKey is the text of a refusal code; ticketReason is the reason
// of reissue.ticket_not_reissuable (review_hold, refund_in_progress). The key
// is the order's wording; rxKey turns it into an invitation's.
func reissueRefusalKey(code, ticketReason string) string {
	switch code {
	case codeOrderNotPaid:
		return "bot.reissue.err_not_paid"
	case codeSellerSiteOrder:
		return "bot.reissue.site"
	case codeReissueNothing:
		return "bot.reissue.err_nothing"
	case codeReissueClosed:
		return "bot.reissue.err_closed"
	case codeReissueProgress:
		return "bot.reissue.err_in_progress"
	case codeReissueTicket:
		if ticketReason == "refund_in_progress" {
			return "bot.reissue.err_ticket_refund"
		}
		return "bot.reissue.err_ticket_review"
	case codeReissueKeyReused:
		return "bot.reissue.err_key_reused"
	case codeReissueApplied:
		return "bot.reissue.err_applied"
	case codeReissueEmail:
		return "bot.reissue.bad_email"
	case codeReissueReason:
		return "bot.reissue.err_reason"
	case codeReissueNote:
		return "bot.reissue.note_bad"
	case codeReissueNotIssued:
		return "bot.reissue.inv_err_not_issued"
	}
	return "bot.reissue.err_refused"
}

// reissueBlockingReason is the reason of the first ticket that blocks the
// whole operation, "" when none does.
func reissueBlockingReason(tickets []openapi.ReissueTicketOption) string {
	if t := reissueBlockingTicket(tickets); t != nil && t.Reason != nil {
		return *t.Reason
	}
	return ""
}

func reissueBlockingTicket(tickets []openapi.ReissueTicketOption) *openapi.ReissueTicketOption {
	for i := range tickets {
		t := &tickets[i]
		if t.Status == "active" && !t.Reissuable && t.Reason != nil && *t.Reason != "not_active" {
			return t
		}
	}
	return nil
}

func reissuableCount(tickets []openapi.ReissueTicketOption) int {
	n := 0
	for _, t := range tickets {
		if t.Reissuable {
			n++
		}
	}
	return n
}

// reissueScannedOf lists the tickets that will be replaced although their
// code was already used, with the time in the venue's zone ("" = UTC).
func reissueScannedOf(tickets []openapi.ReissueTicketOption, tz string) []reissueScanned {
	var out []reissueScanned
	for _, t := range tickets {
		if t.Reissuable && t.ScannedAt != nil {
			out = append(out, reissueScanned{Number: t.Number, When: FormatWhen(*t.ScannedAt, tz)})
		}
	}
	return out
}

// reissueReasonFor maps a reason button ("1".."3") to the API's code.
func reissueReasonFor(arg string) (string, bool) {
	i, err := strconv.Atoi(arg)
	if err != nil || i < 1 || i > len(reissueReasons) {
		return "", false
	}
	return reissueReasons[i-1], true
}

// reissuePressLocked reports a press that would change an operation which
// may already be done (it went out and no answer came): going back, choosing
// again, a reason or the note. Each of them would need a new key, and a new
// key is a second replacement. Only "confirm" (redraws the word step),
// "cancel" and the typed word stay open.
func reissuePressLocked(st reissueDialog, kind string) bool {
	if !st.Attempted {
		return false
	}
	switch kind {
	case "ok", "no", "rs":
		return false
	}
	return true
}

// reissueOutcomeUnknown reports an answer that does not tell whether the
// tickets were replaced: no answer at all, or a server error.
func reissueOutcomeUnknown(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return true
	}
	return ae.Status >= 500 && ae.Status != http.StatusServiceUnavailable
}

// validReissueNote checks a typed note.
func validReissueNote(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len([]rune(s)) <= maxReissueNoteLen
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

func (b *Bot) saveReissue(ctx context.Context, tg int64, st reissueDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, reissueDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: reissue dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

func (b *Bot) leaveReissue(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, reissueDialogKind); err != nil {
		b.logger.Warn("eventbot: reissue dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// rxT renders key in the dialog's own wording.
func (b *Bot) rxT(loc string, st reissueDialog, key string, data map[string]any) string {
	return b.texts.T(loc, rxKey(st.inv(), key), data)
}

// ─── presses ──────────────────────────────────────────────────────────────────

// reissueCallback handles every "or:x:<data>" press.
func (b *Bot) reissueCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
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
	if kind == "s" || kind == "si" {
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		if kind == "si" {
			b.startInvitationReissue(ctx, chatID, &msgID, from, id, jwt, target)
		} else {
			b.startReissue(ctx, chatID, &msgID, from, id, jwt, target)
		}
		return
	}

	var st reissueDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, reissueDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: reissue dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if !found || st.OrgID != id.Current.OrgID {
		b.resendGone(ctx, chatID, &msgID, loc, expired)
		return
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.reissue.private_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	if reissuePressLocked(st, kind) {
		b.showReissueWord(ctx, chatID, &msgID, from.ID, loc, st, b.texts.T(loc, "bot.reissue.maybe_done", nil)+"\n\n")
		return
	}
	switch kind {
	case "fo":
		if st.inv() {
			// An invitation has no "order address" resend: ask for the address.
			b.askReissueEmail(ctx, chatID, &msgID, from.ID, loc, st)
			return
		}
		// Case 2 of section 1.2: the buyer writes FROM the order's address
		// and asks for another mailbox. Their tickets go there once, the
		// barcodes stay (decision R-18).
		b.leaveReissue(ctx, from.ID)
		b.startResend(ctx, chatID, &msgID, from, id, jwt, st.OrderID)
	case "fn":
		b.askReissueEmail(ctx, chatID, &msgID, from.ID, loc, st)
	case "b":
		st.Email, st.Reason, st.Note, st.Key = "", "", "", ""
		b.showReissueFirst(ctx, chatID, &msgID, from.ID, loc, st)
	case "r":
		reason, ok := reissueReasonFor(arg)
		if !ok || st.Email == "" {
			b.showReissueFirst(ctx, chatID, &msgID, from.ID, loc, st)
			return
		}
		st.Reason, st.Key = reason, ""
		st.MsgID = msgID
		b.saveReissue(ctx, from.ID, st, rxStepNote)
		b.reply(ctx, chatID, &msgID, b.rxT(loc, st, "bot.reissue.note_ask", nil), b.reissueNoteKeyboard(loc))
	case "nn":
		if st.Email == "" || st.Reason == "" {
			b.showReissueFirst(ctx, chatID, &msgID, from.ID, loc, st)
			return
		}
		st.Note = ""
		b.showReissueCheck(ctx, chatID, &msgID, from.ID, loc, st, "")
	case "ok":
		// The confirm press only opens the word step (decision R-5): the
		// tickets are replaced when the word is typed, never by a press.
		switch step {
		case rxStepCheck, rxStepWord:
			b.showReissueWord(ctx, chatID, &msgID, from.ID, loc, st, "")
		default:
			b.showReissueFirst(ctx, chatID, &msgID, from.ID, loc, st)
		}
	case "no":
		b.leaveReissue(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.rxT(loc, st, "bot.reissue.cancelled", map[string]any{"Num": st.Num}), b.reissueResultKeyboard(loc, st))
	case "rs":
		if step != rxStepPending || st.PendingID == uuid.Nil {
			b.showReissueFirst(ctx, chatID, &msgID, from.ID, loc, st)
			return
		}
		b.resendReissueLetter(ctx, chatID, &msgID, from, id, jwt, st)
	}
}

// startReissue opens the dialog for an order from a fresh read of its options.
func (b *Bot) startReissue(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, orderID uuid.UUID) {
	loc := id.Locale()
	if chatID != from.ID {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.private_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	opts, err := b.arena.ReissueOptions(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
		return
	}
	tz := deref(opts.VenueTimezone) // the venue's zone for the time of an earlier entry
	st := reissueDialog{
		OrgID: id.Current.OrgID, OrderID: orderID, Num: opts.OrderNumber, Tickets: reissuableCount(opts.Tickets),
		Buyer: strings.TrimSpace(opts.BuyerName), Phone: strings.TrimSpace(opts.BuyerPhone),
		Scanned: reissueScannedOf(opts.Tickets, tz),
	}
	if e := strings.TrimSpace(opts.OrderEmail); e != "" {
		st.Masked = maskEmail(e)
	}
	if p := opts.Provider; p != nil && !p.EmailUpdatable {
		st.Provider, st.ProviderKeeps = p.Title, true
		if st.Provider == "" {
			st.Provider = providerName(p.Name)
		}
	}
	screen, key := reissueStartScreen(opts)
	b.openReissue(ctx, chatID, editMsgID, from, loc, st, screen, key, opts.Tickets, opts.PendingReissue, tz)
}

// startInvitationReissue opens the dialog for an earlier invitation (a
// complimentary issuance without an order) from a fresh read of its options.
func (b *Bot) startInvitationReissue(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, invitationID uuid.UUID) {
	loc := id.Locale()
	if chatID != from.ID {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.private_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	opts, err := b.arena.InvitationReissueOptions(ctx, jwt, id.Current.OrgID, invitationID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:lg")
		return
	}
	tz := deref(opts.VenueTimezone) // the venue's zone for the time of an earlier entry
	st := reissueDialog{
		OrgID: id.Current.OrgID, InvitationID: invitationID, Tickets: reissuableCount(opts.Tickets),
		Scanned: reissueScannedOf(opts.Tickets, tz),
	}
	if e := strings.TrimSpace(opts.GuestEmail); e != "" {
		st.Masked = maskEmail(e)
	}
	screen, key := invitationStartScreen(opts)
	b.openReissue(ctx, chatID, editMsgID, from, loc, st, screen, key, opts.Tickets, opts.PendingReissue, tz)
}

// openReissue draws the first screen the options decided.
func (b *Bot) openReissue(ctx context.Context, chatID int64, editMsgID *int, from *models.User, loc string, st reissueDialog,
	screen, key string, tickets []openapi.ReissueTicketOption, pending *openapi.ReissuePending, tz string) {
	switch screen {
	case "site", "blocked":
		ticket := ""
		if t := reissueBlockingTicket(tickets); t != nil {
			ticket = "№" + strconv.FormatInt(t.Number, 10)
		}
		b.reply(ctx, chatID, editMsgID, b.rxT(loc, st, key, map[string]any{"Num": st.Num, "Ticket": ticket, "Email": Esc(st.Masked)}),
			b.reissueResultKeyboard(loc, st))
	case "pending":
		st.PendingID = pending.Id
		if editMsgID != nil {
			st.MsgID = *editMsgID
		}
		b.saveReissue(ctx, from.ID, st, rxStepPending)
		stateKey := rxKey(st.inv(), "bot.reissue.pending_queued")
		if pending.State == "send_failed" {
			stateKey = "bot.reissue.pending_failed"
		}
		text := b.rxT(loc, st, "bot.reissue.pending_head", map[string]any{
			"Num": st.Num, "Email": Esc(maskEmail(pending.NewEmail)), "When": Esc(FormatWhen(pending.CreatedAt, tz)),
		}) + "\n\n" + b.texts.T(loc, stateKey, nil)
		b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.reissue.pending_resend_btn", nil), CallbackData: "or:x:rs"}},
			{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: st.backTo()}, {Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
	default:
		b.showReissueFirst(ctx, chatID, editMsgID, from.ID, loc, st)
	}
}

// ─── screens ──────────────────────────────────────────────────────────────────

// showReissueFirst is the dialog's first question: the case for an order,
// the address for an invitation.
func (b *Bot) showReissueFirst(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog) {
	if st.inv() {
		b.askReissueEmail(ctx, chatID, editMsgID, tg, loc, st)
		return
	}
	b.showReissueCase(ctx, chatID, editMsgID, tg, loc, st)
}

// showReissueCase is the first question of an order: which address does the
// buyer write from?
func (b *Bot) showReissueCase(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveReissue(ctx, tg, st, rxStepCase)
	email := st.Masked
	if email == "" {
		email = b.texts.T(loc, "bot.reissue.no_address", nil)
	}
	text := b.texts.T(loc, "bot.reissue.case", map[string]any{"Num": st.Num, "N": st.Tickets, "Email": Esc(email)})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.reissue.case_order_btn", nil), CallbackData: "or:x:fo"}},
		{{Text: b.texts.T(loc, "bot.reissue.case_other_btn", nil), CallbackData: "or:x:fn"}},
		{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: st.backTo()},
			{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
		},
	}})
}

// askReissueEmail stores the address step and asks for the right address.
func (b *Bot) askReissueEmail(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveReissue(ctx, tg, st, rxStepEmail)
	kb := b.reissueBackKeyboard(loc)
	if st.inv() {
		// No case question to go back to: the way back is the list.
		kb = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: st.backTo()},
			{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
		}}}
	}
	b.reply(ctx, chatID, editMsgID, b.reissueEmailAsk(loc, st), kb)
}

// reissueEmailAsk asks for the right address and shows what to compare it with.
func (b *Bot) reissueEmailAsk(loc string, st reissueDialog) string {
	dash := "—"
	email, name, phone := st.Masked, st.Buyer, st.Phone
	if email == "" {
		email = dash
	}
	if name == "" {
		name = dash
	}
	if phone == "" {
		phone = dash
	}
	return b.rxT(loc, st, "bot.reissue.email_ask", map[string]any{
		"Num": st.Num, "N": st.Tickets, "Email": Esc(email), "Name": Esc(name), "Contact": Esc(phone),
	})
}

// showReissueReason asks for the reason, with the typed address back.
func (b *Bot) showReissueReason(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveReissue(ctx, tg, st, rxStepReason)
	rows := make([][]models.InlineKeyboardButton, 0, len(reissueReasons)+1)
	for i, code := range reissueReasons {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: b.texts.T(loc, "bot.reissue.reason_"+code, nil), CallbackData: "or:x:r:" + strconv.Itoa(i+1),
		}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:x:b"},
		{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
	})
	b.reply(ctx, chatID, editMsgID, b.rxT(loc, st, "bot.reissue.reason_ask", map[string]any{"Email": Esc(st.Email)}),
		&models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// reissueCheckText is the check screen: what will happen, before anything is sent.
func (b *Bot) reissueCheckText(loc string, st reissueDialog) string {
	var sb strings.Builder
	sb.WriteString(b.rxT(loc, st, "bot.reissue.check", map[string]any{
		"Num": st.Num, "N": st.Tickets, "Email": Esc(maskEmail(st.Email)),
		"Reason": b.texts.T(loc, "bot.reissue.reason_"+st.Reason, nil),
	}))
	if st.Note != "" {
		sb.WriteString("\n" + b.texts.T(loc, "bot.reissue.check_note", map[string]any{"Text": Esc(st.Note)}))
	}
	for _, s := range st.Scanned {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.reissue.check_scanned", map[string]any{
			"Ticket": "№" + strconv.FormatInt(s.Number, 10), "When": Esc(s.When),
		}))
	}
	if st.ProviderKeeps {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.reissue.check_provider", map[string]any{"Provider": Esc(st.Provider)}))
	}
	return sb.String()
}

// showReissueCheck is the check screen. It mints the operation's key the
// first time it is shown.
func (b *Bot) showReissueCheck(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog, note string) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	if st.Key == "" {
		st.Key = "tg-" + uuid.NewString()
	}
	b.saveReissue(ctx, tg, st, rxStepCheck)
	b.reply(ctx, chatID, editMsgID, note+b.reissueCheckText(loc, st), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.reissue.confirm_btn", nil), CallbackData: "or:x:ok"}},
		{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:x:b"},
			{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
		},
	}})
}

// showReissueWord asks for the typed word. Nothing is replaced before it.
func (b *Bot) showReissueWord(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st reissueDialog, note string) {
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	if st.Key == "" {
		st.Key = "tg-" + uuid.NewString()
	}
	b.saveReissue(ctx, tg, st, rxStepWord)
	rows := [][]models.InlineKeyboardButton{{{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"}}}
	if !st.Attempted {
		rows = [][]models.InlineKeyboardButton{{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:x:b"},
			{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
		}}
	}
	text := note + b.rxT(loc, st, "bot.reissue.word_ask", map[string]any{
		"Num": st.Num, "N": st.Tickets, "Word": b.reissueWord(loc),
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) reissueBackKeyboard(loc string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:x:b"},
		{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
	}}}
}

func (b *Bot) reissueNoteKeyboard(loc string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.reissue.no_note_btn", nil), CallbackData: "or:x:nn"}},
		{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:x:b"},
			{Text: b.texts.T(loc, "bot.reissue.cancel_btn", nil), CallbackData: "or:x:no"},
		},
	}}
}

// reissueResultKeyboard leads back to the order card or to the earlier
// invitations' list.
func (b *Bot) reissueResultKeyboard(loc string, st reissueDialog) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: st.backTo()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
}

// ─── the calls ────────────────────────────────────────────────────────────────

func (b *Bot) postReissue(ctx context.Context, jwt string, st reissueDialog) (openapi.ReissueTicketsResponse, error) {
	if st.inv() {
		return b.arena.InvitationReissueTickets(ctx, jwt, st.OrgID, st.InvitationID, st.Key, st.Email, st.Reason, st.Note)
	}
	return b.arena.ReissueTickets(ctx, jwt, st.OrgID, st.OrderID, st.Key, st.Email, st.Reason, st.Note)
}

// sendReissue makes the call after the right word and shows what came of it.
func (b *Bot) sendReissue(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st reissueDialog) {
	loc := id.Locale()
	res, err := b.postReissue(ctx, jwt, st)
	back := b.reissueResultKeyboard(loc, st)
	if err == nil {
		b.leaveReissue(ctx, from.ID)
		b.reply(ctx, chatID, editMsgID, b.reissueResultText(loc, st, res), back)
		return
	}
	if reissueOutcomeUnknown(err) {
		// The replacement may have been made: keep the key, so the same word
		// repeats the same request and the server answers its first result.
		b.logger.Warn("eventbot: reissue outcome unknown", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		st.Attempted = true
		b.showReissueWord(ctx, chatID, editMsgID, from.ID, loc, st, b.texts.T(loc, "bot.reissue.err_unknown", nil)+"\n\n")
		return
	}
	var ae *APIError
	_ = errors.As(err, &ae)
	code := APIErrorCode(err)
	switch {
	case code == codeReissueEmail:
		st.Email, st.Key = "", ""
		b.saveReissue(ctx, from.ID, st, rxStepEmail)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.bad_email", nil), b.reissueBackKeyboard(loc))
	case code == codeReissueNote:
		st.Note, st.Key = "", ""
		b.saveReissue(ctx, from.ID, st, rxStepNote)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.note_bad", nil), b.reissueNoteKeyboard(loc))
	case code == codeReissueKeyReused:
		// The same key once carried other details: a new operation, checked again.
		st.Key, st.Attempted = "", false
		b.showReissueCheck(ctx, chatID, editMsgID, from.ID, loc, st, b.texts.T(loc, "bot.reissue.err_key_reused", nil)+"\n\n")
	case IsAPIError(err, http.StatusForbidden):
		b.leaveReissue(ctx, from.ID)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.err_forbidden", nil), back)
	case IsAPIError(err, http.StatusNotFound):
		// orders.not_found, complimentary.not_found: gone or another
		// organization's, said the same way.
		b.leaveReissue(ctx, from.ID)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), back)
	case IsAPIError(err, http.StatusServiceUnavailable):
		st.Attempted = false
		b.showReissueWord(ctx, chatID, editMsgID, from.ID, loc, st, b.texts.T(loc, "bot.reissue.err_unavailable", nil)+"\n\n")
	default:
		b.leaveReissue(ctx, from.ID)
		ticket := ""
		if ae != nil && ae.TicketNumber > 0 {
			ticket = "№" + strconv.FormatInt(ae.TicketNumber, 10)
		}
		reason := ""
		if ae != nil {
			reason = ae.Reason
		}
		b.reply(ctx, chatID, editMsgID, b.rxT(loc, st, reissueRefusalKey(code, reason), map[string]any{
			"Num": st.Num, "Ticket": ticket, "Email": Esc(st.Masked),
		}), back)
	}
}

// reissueResultText is the result: the new ticket numbers and where the letter goes.
func (b *Bot) reissueResultText(loc string, st reissueDialog, res openapi.ReissueTicketsResponse) string {
	var sb strings.Builder
	if res.Replayed {
		sb.WriteString(b.texts.T(loc, "bot.reissue.replayed", nil) + "\n\n")
	}
	sb.WriteString(b.rxT(loc, st, "bot.reissue.done_head", map[string]any{"Num": st.Num, "N": len(res.Tickets)}))
	for _, p := range res.Tickets {
		seat := ""
		if s := strings.TrimSpace(p.Seat); s != "" {
			seat = " · " + Esc(s)
		}
		sb.WriteString("\n" + b.texts.T(loc, "bot.reissue.done_line", map[string]any{
			"Old": p.OldNumber, "New": p.NewNumber, "Seat": seat,
		}))
	}
	sb.WriteString("\n\n" + b.rxT(loc, st, "bot.reissue.done_letter", map[string]any{"Email": Esc(res.RecipientMasked)}))
	if st.ProviderKeeps {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.reissue.check_provider", map[string]any{"Provider": Esc(st.Provider)}))
	}
	return sb.String()
}

// resendReissueLetter queues the letter of a pending reissue again.
func (b *Bot) resendReissueLetter(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st reissueDialog) {
	loc := id.Locale()
	var err error
	if st.inv() {
		_, err = b.arena.InvitationResendReissue(ctx, jwt, st.OrgID, st.InvitationID, st.PendingID)
	} else {
		_, err = b.arena.ResendReissue(ctx, jwt, st.OrgID, st.OrderID, st.PendingID)
	}
	back := b.reissueResultKeyboard(loc, st)
	b.leaveReissue(ctx, from.ID)
	data := map[string]any{"Num": st.Num, "Email": Esc(st.Masked)}
	switch {
	case err == nil:
		b.reply(ctx, chatID, editMsgID, b.rxT(loc, st, "bot.reissue.resent", data), back)
	case APIErrorCode(err) == codeReissueApplied:
		b.reply(ctx, chatID, editMsgID, b.rxT(loc, st, "bot.reissue.err_applied", data), back)
	case APIErrorCode(err) == codeReissueNotFound || APIErrorCode(err) == codeComplimentaryGone || IsAPIError(err, http.StatusNotFound):
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), back)
	case IsAPIError(err, http.StatusForbidden):
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.reissue.err_forbidden", nil), back)
	default:
		b.ecError(ctx, chatID, editMsgID, id, err, st.backTo())
	}
}

// ─── typed text ───────────────────────────────────────────────────────────────

// reissueText takes the address, the note or the word typed while the dialog
// waits for one. It reports whether the text was taken.
func (b *Bot) reissueText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if chatID != from.ID {
		return false // the dialog lives in the private chat only
	}
	var st reissueDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, reissueDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: reissue dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
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
	if !found || (step != rxStepEmail && step != rxStepNote && step != rxStepWord) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		return false
	}
	if id.Current == nil || id.Current.OrgID != st.OrgID || !canResend(id) {
		b.leaveReissue(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	text = strings.TrimSpace(text)
	switch step {
	case rxStepEmail:
		if !validResendEmail(text) {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.reissue.bad_email", nil), b.reissueBackKeyboard(loc))
			return true
		}
		st.Email, st.Key = text, ""
		b.showReissueReason(ctx, chatID, edit, from.ID, loc, st)
	case rxStepNote:
		if !validReissueNote(text) {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.reissue.note_bad", nil), b.reissueNoteKeyboard(loc))
			return true
		}
		st.Note, st.Key = text, ""
		b.showReissueCheck(ctx, chatID, edit, from.ID, loc, st, "")
	case rxStepWord:
		if !b.isReissueWord(loc, text) {
			b.showReissueWord(ctx, chatID, edit, from.ID, loc, st,
				b.texts.T(loc, "bot.reissue.word_wrong", map[string]any{"Word": b.reissueWord(loc)})+"\n\n")
			return true
		}
		b.sendReissue(ctx, chatID, edit, from, id, jwt, st)
	}
	return true
}
