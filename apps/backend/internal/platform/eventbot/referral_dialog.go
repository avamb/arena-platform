package eventbot

// referral_dialog.go — the "Refer a friend" screens of the bot (spec 52
// section 5.2 and 7.1): the programme card of an event, the turn-on / change
// dialog, pause, resume, end, the cashback list. Everything lives in ONE
// bot_dialogs row of kind "referral" (dialogs.go), so a bot restart loses
// nothing and a typed number always knows which question it answers.
//
// Presses are "rf:<kind>:<arg>". The ones that act on an event carry the
// event's UUID, so an old message keeps meaning THAT event; the answers of the
// dialog and the list's chips and pages use the stored state:
//
//	rf:o:<event>    the card          rf:n:<event>   turn on / change
//	rf:ps:<event>   pause             rf:rs:<event>  resume
//	rf:en:<event>   end? (question)   rf:ey:<event>  yes, end (only on that question)
//	rf:rw:<event>   the cashback list rf:rf:<state>  a chip    rf:rp:<n>  a page
//	rf:c:<data>     one answer of the dialog: keep, cap100, nomax, back, go, cancel
//
// Ending is final, so it is a question screen and a separate "Yes" button. A
// pause and a resume are one press: they undo each other.
//
// The money rules are arena-api's. The bot never computes a cashback; it
// shows what the server stored and what it paid.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const referralDialogKind = "referral"

// referralDialog is the screens' state, stored as the bot_dialogs row's JSON.
type referralDialog struct {
	OrgID     uuid.UUID      `json:"org_id"`
	MsgID     int            `json:"msg_id,omitempty"`
	EventID   uuid.UUID      `json:"event_id"`
	EventName string         `json:"event_name,omitempty"`
	Draft     *referralDraft `json:"draft,omitempty"`
	// The cashback list on screen.
	Filter  string   `json:"filter,omitempty"`
	Page    int      `json:"page,omitempty"`
	Cursors []string `json:"cursors,omitempty"`
}

// canReferral reports whether the person may run the programme: the owner, the
// manager and the platform operator. The API still decides every call
// (promo.update plus refund.create and refund.approve).
func canReferral(id *Identity) bool { return canViewSales(id) }

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadReferralState reads the dialog. found is false when there is none, it
// ran out (expired is then true, once) or it belongs to another organization.
func (b *Bot) loadReferralState(ctx context.Context, tg int64, orgID uuid.UUID) (st referralDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, referralDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: referral dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return referralDialog{OrgID: orgID}, "", false, false
	}
	if !found || st.OrgID != orgID {
		return referralDialog{OrgID: orgID}, "", false, expired
	}
	return st, step, true, false
}

// saveReferralState stores the step BEFORE the reply that asks about it (the
// callers save, then reply).
func (b *Bot) saveReferralState(ctx context.Context, tg int64, st referralDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, referralDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: referral dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveReferral ends the dialog: whoever opens another screen is no longer
// answering a percent, so a text typed next is not taken for one.
func (b *Bot) leaveReferral(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, referralDialogKind); err != nil {
		b.logger.Warn("eventbot: referral dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// referralEventRow is the event card's button.
func (b *Bot) referralEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canReferral(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.rfp.btn", nil), CallbackData: "rf:o:" + eventID.String()}}
}

// referralCallback handles every "rf:<data>" press.
func (b *Bot) referralCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canReferral(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "noop" { // the position button of the list
		return
	}

	// Presses that name an event by its UUID.
	switch kind {
	case "o", "n", "ps", "rs", "en", "ey", "rw":
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		st, step, found, _ := b.loadReferralState(ctx, from.ID, orgID)
		if !found || st.EventID != eventID {
			st = referralDialog{OrgID: orgID, EventID: eventID}
			step = ""
		}
		if st.EventName == "" || kind == "o" {
			name, ok := b.referralEventName(ctx, chatID, &msgID, id, jwt, eventID)
			if !ok {
				return
			}
			st.EventName = name
		}
		st.MsgID = msgID
		switch kind {
		case "o":
			st.Draft, st.Filter, st.Page, st.Cursors = nil, "", 0, nil
			b.referralShowCard(ctx, chatID, &msgID, id, jwt, st, "")
		case "n":
			b.referralStart(ctx, chatID, &msgID, id, jwt, st)
		case "ps", "rs":
			b.referralToggle(ctx, chatID, &msgID, id, jwt, st, kind)
		case "en":
			b.referralAskEnd(ctx, chatID, &msgID, id, st)
		case "ey":
			if step != rfStepEnd {
				// The question is not on screen any more (a stale button):
				// show the card as it is rather than end anything.
				b.referralShowCard(ctx, chatID, &msgID, id, jwt, st, "")
				return
			}
			b.referralEnd(ctx, chatID, &msgID, id, jwt, st)
		case "rw":
			st.Draft, st.Filter, st.Page, st.Cursors = nil, "all", 1, nil
			b.referralRewards(ctx, chatID, &msgID, id, jwt, st, 1, "")
		}
		return
	}

	st, step, found, expired := b.loadReferralState(ctx, from.ID, orgID)
	if !found {
		note := ""
		if expired {
			note = b.texts.T(loc, "bot.dialog_expired", nil) + "\n\n"
		}
		b.referralGone(ctx, chatID, &msgID, loc, note)
		return
	}
	st.MsgID = msgID
	switch kind {
	case "c":
		b.referralAnswer(ctx, chatID, &msgID, id, jwt, st, step, arg)
	case "rf":
		if step != rfStepRewards || !rfValidFilter(arg) {
			b.referralShowCard(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		st.Filter, st.Cursors = arg, nil
		b.referralRewards(ctx, chatID, &msgID, id, jwt, st, 1, "")
	case "rp":
		if step != rfStepRewards {
			b.referralShowCard(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		want, _ := strconv.Atoi(arg)
		if _, ok := rfCursorFor(st.Cursors, want); !ok || want < 1 || (want != st.Page-1 && want != st.Page+1) {
			want = st.Page // a stale page button: show the page that is on screen
		}
		b.referralRewards(ctx, chatID, &msgID, id, jwt, st, want, "")
	}
}

// referralGone answers a press with no live dialog behind it.
func (b *Bot) referralGone(ctx context.Context, chatID int64, editMsgID *int, loc, note string) {
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.rfp.gone", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.rfp.events_btn", nil), CallbackData: "el:b"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// referralEventName names the event; an event of another organization, or a
// missing one, is "not found".
func (b *Bot) referralEventName(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, eventID uuid.UUID) (string, bool) {
	events, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return "", false
	}
	for _, e := range events {
		if e.Id == eventID {
			return e.Name, true
		}
	}
	b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
	return "", false
}

// referralError answers a failed call. The refusals that have their own words
// say plainly what to do; the rest is the standard "not found / no rights /
// error" of the event-center screens, with the way back to the card.
func (b *Bot) referralError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st referralDialog, err error) {
	loc := id.Locale()
	back := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rf:o:" + st.EventID.String()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
	var ae *APIError
	if asAPIError(err, &ae) {
		switch ae.Code {
		case referralCodeProvider:
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rfp.err_provider", map[string]any{"Why": b.referralWhy(loc, ae.Capability)}), back)
			return
		case referralCodeTerms:
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rfp.err_terms", nil), back)
			return
		case referralCodeInvalidValue:
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rfp.err_value", nil), back)
			return
		case referralCodeEnded:
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rfp.err_ended", nil), back)
			return
		}
	}
	b.ecError(ctx, chatID, editMsgID, id, err, "home")
}

// referralWhy words the first reason a channel cannot pay the cashback.
func (b *Bot) referralWhy(loc string, c *openapi.ReferralCapability) string {
	if c == nil {
		return ""
	}
	for _, ch := range c.Channels {
		if ch.Supported || ch.Reason == nil {
			continue
		}
		key := "bot.rfp.why_" + *ch.Reason
		if text := b.texts.T(loc, key, nil); text != key {
			return text
		}
	}
	return ""
}

// ─── the card ─────────────────────────────────────────────────────────────────

// referralShowCard reads the programme afresh and draws its card with the
// buttons its state allows. note is a line above (what was just done).
func (b *Bot) referralShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	var (
		prog     *openapi.ReferralProgram
		capab    *openapi.ReferralCapability
		warnings []openapi.ReferralWarning
	)
	resp, err := b.arena.ReferralProgram(ctx, jwt, orgID, st.EventID)
	switch err {
	case nil:
		prog, capab, warnings = &resp.Program, &resp.Capability, resp.Warnings
	default:
		var ae *APIError
		if asAPIError(err, &ae) && ae.Code == referralCodeNotFound {
			capab = ae.Capability
		} else {
			b.referralError(ctx, chatID, editMsgID, id, st, err)
			return
		}
	}

	var sb strings.Builder
	sb.WriteString(note)
	sb.WriteString(b.texts.T(loc, "bot.rfp.card_head", map[string]any{"Name": Esc(st.EventName)}))
	canStart := capab == nil || capab.Supported
	if prog == nil {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rfp.none", nil))
	} else {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rfp.st_"+prog.Status, nil))
		sb.WriteString("\n" + b.referralSettingsText(loc, prog.FriendDiscountPct, prog.CashbackPct, prog.CashbackCapPct, prog.MaxFriends))
		if sum, err := b.arena.ReferralSummary(ctx, jwt, orgID, st.EventID); err != nil {
			b.logger.Warn("eventbot: referral summary failed", slog.String("event_id", st.EventID.String()), slog.String("error", err.Error()))
		} else {
			sb.WriteString("\n\n" + b.referralSummaryText(loc, sum))
		}
	}
	if capab != nil && !capab.Supported {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rfp.cap_no", map[string]any{"Why": b.referralWhy(loc, capab)}))
	}
	for _, w := range warnings {
		key := "bot.rfp.warn_" + strings.TrimPrefix(w.Code, "referral.warning.")
		if text := b.texts.T(loc, key, nil); text != key {
			sb.WriteString("\n\n" + text)
		}
	}

	st.Draft = nil
	st.MsgID = derefInt(editMsgID, st.MsgID)
	b.saveReferralState(ctx, id.Link.TelegramUserID, st, rfStepCard)

	ev := st.EventID.String()
	btn := func(key, data string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: b.texts.T(loc, key, nil), CallbackData: data}
	}
	var rows [][]models.InlineKeyboardButton
	switch {
	case prog == nil || prog.Status == referralStatusEnded:
		if canStart {
			rows = append(rows, []models.InlineKeyboardButton{btn("bot.rfp.on_btn", "rf:n:"+ev)})
		}
	case prog.Status == referralStatusPaused:
		rows = append(rows, []models.InlineKeyboardButton{btn("bot.rfp.resume_btn", "rf:rs:"+ev), btn("bot.rfp.change_btn", "rf:n:"+ev)})
	default:
		rows = append(rows, []models.InlineKeyboardButton{btn("bot.rfp.pause_btn", "rf:ps:"+ev), btn("bot.rfp.change_btn", "rf:n:"+ev)})
	}
	if prog != nil {
		line := []models.InlineKeyboardButton{btn("bot.rfp.rewards_btn", "rf:rw:"+ev)}
		if prog.Status != referralStatusEnded {
			line = append(line, btn("bot.rfp.end_btn", "rf:en:"+ev))
		}
		rows = append(rows, line)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + ev},
		btn("bot.btn_home", "home"),
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func derefInt(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

// referralMax words the friends-per-code limit.
func (b *Bot) referralMax(loc string, max *int) string {
	if max == nil {
		return b.texts.T(loc, "bot.rfp.max_none", nil)
	}
	return strconv.Itoa(*max)
}

// referralSettingsText is the "friend gets X %, referrer gets Y %" paragraph.
func (b *Bot) referralSettingsText(loc string, friend, cashback, capPct int, max *int) string {
	return b.texts.T(loc, "bot.rfp.settings", map[string]any{
		"Friend": friend, "Cashback": cashback, "Cap": capPct, "Max": b.referralMax(loc, max),
	})
}

// referralSummaryText is the totals of the programme, one block per currency.
// Currencies are never added together.
func (b *Bot) referralSummaryText(loc string, s openapi.ReferralSummary) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.rfp.sum", map[string]any{"Codes": s.CodesIssued, "Friends": s.Friends}))
	rows := referralMoneyRows(s)
	if len(rows) == 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.rfp.sum_none", nil))
	}
	for _, r := range rows {
		sb.WriteString("\n\n<b>" + Esc(r.Currency) + "</b>")
		line := func(key string, minor int64) {
			if minor != 0 {
				sb.WriteString("\n" + b.texts.T(loc, key, map[string]any{"Amount": FormatMoney(minor, r.Currency, loc)}))
			}
		}
		line("bot.rfp.m_discount", r.Discount)
		line("bot.rfp.m_paid", r.Paid)
		line("bot.rfp.m_way", r.Way)
		line("bot.rfp.m_kept", r.Kept)
	}
	if s.FriendReversals > 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rfp.sum_reversals", map[string]any{"Count": s.FriendReversals}))
	}
	return sb.String()
}

// ─── pause, resume, end ───────────────────────────────────────────────────────

func (b *Bot) referralToggle(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog, kind string) {
	loc := id.Locale()
	action, doneKey := "pause", "bot.rfp.paused_done"
	if kind == "rs" {
		action, doneKey = "resume", "bot.rfp.resumed_done"
	}
	if _, err := b.arena.ReferralAction(ctx, jwt, id.Current.OrgID, st.EventID, action); err != nil {
		b.referralError(ctx, chatID, editMsgID, id, st, err)
		return
	}
	b.referralShowCard(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, doneKey, nil)+"\n\n")
}

// referralAskEnd draws the end question. Nothing ends before the "Yes" press,
// which is honoured only while this question is the stored step.
func (b *Bot) referralAskEnd(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st referralDialog) {
	loc := id.Locale()
	st.Draft = nil
	b.saveReferralState(ctx, id.Link.TelegramUserID, st, rfStepEnd)
	ev := st.EventID.String()
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rfp.end_ask", map[string]any{"Name": Esc(st.EventName)}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.rfp.end_yes_btn", nil), CallbackData: "rf:ey:" + ev}},
		{{Text: b.texts.T(loc, "bot.rfp.end_no_btn", nil), CallbackData: "rf:o:" + ev}},
	}})
}

func (b *Bot) referralEnd(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog) {
	loc := id.Locale()
	if _, err := b.arena.ReferralAction(ctx, jwt, id.Current.OrgID, st.EventID, "end"); err != nil {
		b.referralError(ctx, chatID, editMsgID, id, st, err)
		return
	}
	b.referralShowCard(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.rfp.ended_done", nil)+"\n\n")
}

// ─── the turn-on / change dialog ──────────────────────────────────────────────

// referralStart opens the dialog at the first question. A live programme is
// edited (its values are offered as "keep" buttons); none, or an ended one,
// starts a new one.
func (b *Bot) referralStart(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog) {
	resp, err := b.arena.ReferralProgram(ctx, jwt, id.Current.OrgID, st.EventID)
	var prog *openapi.ReferralProgram
	switch err {
	case nil:
		prog = &resp.Program
		if prog.Status == referralStatusEnded {
			prog = nil
		}
	default:
		var ae *APIError
		if !asAPIError(err, &ae) || ae.Code != referralCodeNotFound {
			b.referralError(ctx, chatID, editMsgID, id, st, err)
			return
		}
	}
	st.Draft = newReferralDraft(prog)
	b.referralShowDraft(ctx, chatID, editMsgID, id, st, "")
}

// referralText takes a typed text for the dialog's questions.
func (b *Bot) referralText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st referralDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, referralDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: referral dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.rfp.events_btn", nil), CallbackData: "el:b"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || !rfTakesText(step) || st.Draft == nil {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canReferral(id) {
		b.leaveReferral(ctx, from.ID)
		return false
	}
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	note := ""
	if key := rfApply(st.Draft, text, ""); key != "" {
		note = b.texts.T(id.Locale(), key, nil) + "\n\n"
	}
	b.referralShowDraft(ctx, chatID, edit, id, st, note)
	return true
}

// referralAnswer handles "rf:c:<data>".
func (b *Bot) referralAnswer(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog, step, data string) {
	loc := id.Locale()
	if st.Draft == nil || st.Draft.Step != step || !strings.HasPrefix(step, "rf_") {
		b.referralShowCard(ctx, chatID, editMsgID, id, jwt, st, "")
		return
	}
	switch data {
	case "cancel":
		b.referralShowCard(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.rfp.cancelled", nil)+"\n\n")
		return
	case "back":
		rfBack(st.Draft)
		b.referralShowDraft(ctx, chatID, editMsgID, id, st, "")
		return
	case "go":
		if st.Draft.Step == rfStepRules {
			b.referralSave(ctx, chatID, editMsgID, id, jwt, st)
			return
		}
		b.referralShowDraft(ctx, chatID, editMsgID, id, st, "")
		return
	}
	note := ""
	if key := rfApply(st.Draft, "", data); key != "" {
		note = b.texts.T(loc, key, nil) + "\n\n"
	}
	b.referralShowDraft(ctx, chatID, editMsgID, id, st, note)
}

// referralSave sends the finished answers. The dialog stays on the rules
// screen if the server refuses, so the person can go back and change a value.
func (b *Bot) referralSave(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog) {
	loc := id.Locale()
	editing := st.Draft.Editing
	in := st.Draft.settings()
	if _, err := b.arena.SetReferralProgram(ctx, jwt, id.Current.OrgID, st.EventID, in); err != nil {
		b.referralError(ctx, chatID, editMsgID, id, st, err)
		return
	}
	done := "bot.rfp.on_done"
	if editing {
		done = "bot.rfp.saved_done"
	}
	b.referralShowCard(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, done, nil)+"\n\n")
}

// referralShowDraft stores the step and draws its question. note is a line
// above (a refused answer, a hint).
func (b *Bot) referralShowDraft(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st referralDialog, note string) {
	loc := id.Locale()
	d := st.Draft
	st.MsgID = derefInt(editMsgID, st.MsgID)
	b.saveReferralState(ctx, id.Link.TelegramUserID, st, d.Step)

	head := b.texts.T(loc, "bot.rfp.d_head_new", map[string]any{"Name": Esc(st.EventName)})
	if d.Editing {
		head = b.texts.T(loc, "bot.rfp.d_head_edit", map[string]any{"Name": Esc(st.EventName)})
	}
	btn := func(label, data string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: label, CallbackData: data}
	}
	keep := func(value string) models.InlineKeyboardButton {
		return btn(b.texts.T(loc, "bot.rfp.keep_btn", map[string]any{"Value": value}), "rf:c:keep")
	}
	var question string
	var rows [][]models.InlineKeyboardButton
	switch d.Step {
	case rfStepFriend:
		question = b.texts.T(loc, "bot.rfp.q_friend", nil)
		if d.Editing {
			rows = append(rows, []models.InlineKeyboardButton{keep(strconv.Itoa(d.Old.Friend) + " %")})
		}
	case rfStepCashback:
		question = b.texts.T(loc, "bot.rfp.q_cashback", nil)
		if d.Editing {
			rows = append(rows, []models.InlineKeyboardButton{keep(strconv.Itoa(d.Old.Cashback) + " %")})
		}
	case rfStepCap:
		question = b.texts.T(loc, "bot.rfp.q_cap", nil)
		line := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.rfp.cap100_btn", nil), "rf:c:cap100")}
		if d.Editing && d.Old.Cap != 100 {
			line = append(line, keep(strconv.Itoa(d.Old.Cap)+" %"))
		}
		rows = append(rows, line)
	case rfStepMax:
		question = b.texts.T(loc, "bot.rfp.q_max", nil)
		line := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.rfp.nomax_btn", nil), "rf:c:nomax")}
		if d.Editing && d.Old.Max != nil {
			line = append(line, keep(strconv.Itoa(*d.Old.Max)))
		}
		rows = append(rows, line)
	case rfStepRules:
		key, goKey := "bot.rfp.rules", "bot.rfp.go_btn"
		if d.Editing {
			key, goKey = "bot.rfp.edit_confirm", "bot.rfp.save_btn"
		}
		question = b.texts.T(loc, key, map[string]any{
			"Friend": d.Friend, "Cashback": d.Cashback, "Cap": d.Cap, "Max": b.referralMax(loc, d.Max),
		})
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, goKey, nil), "rf:c:go")})
	}
	nav := []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.rfp.cancel_btn", nil), "rf:c:cancel")}
	if d.Step != rfStepFriend {
		nav = append([]models.InlineKeyboardButton{btn("« "+b.texts.T(loc, "bot.btn_back", nil), "rf:c:back")}, nav...)
	}
	rows = append(rows, nav)
	b.reply(ctx, chatID, editMsgID, clipMessage(note+head+"\n\n"+question, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── the cashback list ────────────────────────────────────────────────────────

// referralRewards draws one page of the list (page is 1-based). A page whose
// cursor the state does not hold starts again from the first.
func (b *Bot) referralRewards(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st referralDialog, page int, note string) {
	loc := id.Locale()
	if st.Filter == "" || !rfValidFilter(st.Filter) {
		st.Filter = "all"
	}
	cursor, ok := rfCursorFor(st.Cursors, page)
	if !ok {
		page, cursor, st.Cursors = 1, "", nil
	}
	res, err := b.arena.ReferralRewards(ctx, jwt, id.Current.OrgID, st.EventID, rfStateParam(st.Filter), cursor)
	if err != nil && cursor != "" && APIErrorCode(err) == "referral.invalid_cursor" {
		// A stored cursor the API no longer takes: start from the first page.
		page, st.Cursors = 1, nil
		res, err = b.arena.ReferralRewards(ctx, jwt, id.Current.OrgID, st.EventID, rfStateParam(st.Filter), "")
	}
	if err != nil {
		b.referralError(ctx, chatID, editMsgID, id, st, err)
		return
	}
	next := ""
	if res.NextCursor != nil {
		next = *res.NextCursor
	}
	st.Page = page
	st.Cursors = rfKeepCursors(st.Cursors, page, next)
	st.Draft = nil
	st.MsgID = derefInt(editMsgID, st.MsgID)
	b.saveReferralState(ctx, id.Link.TelegramUserID, st, rfStepRewards)

	var sb strings.Builder
	sb.WriteString(note)
	sb.WriteString(b.texts.T(loc, "bot.rfp.rw_head", map[string]any{
		"Name": Esc(st.EventName), "Filter": b.referralFilterLabel(loc, st.Filter), "Page": page,
	}))
	if len(res.Items) == 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rfp.rw_empty", nil))
	}
	for _, it := range res.Items {
		sb.WriteString("\n\n" + b.referralRewardText(loc, it))
	}

	var rows [][]models.InlineKeyboardButton
	if nav := b.referralRewardsPager(loc, page, next != ""); nav != nil {
		rows = append(rows, nav)
	}
	var chips []models.InlineKeyboardButton
	for i, f := range rfFilters {
		label := b.referralFilterLabel(loc, f)
		if f == st.Filter {
			label = "✔ " + label
		}
		chips = append(chips, models.InlineKeyboardButton{Text: label, CallbackData: "rf:rf:" + f})
		if (i+1)%4 == 0 || i == len(rfFilters)-1 {
			rows = append(rows, chips)
			chips = nil
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rf:o:" + st.EventID.String()},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// referralRewardsPager is the "‹ 2 ›" row of a list that only knows its next
// page: the number alone, because the API does not say how many pages there are.
func (b *Bot) referralRewardsPager(loc string, page int, hasNext bool) []models.InlineKeyboardButton {
	if page <= 1 && !hasNext {
		return nil
	}
	var row []models.InlineKeyboardButton
	if page > 1 {
		row = append(row, models.InlineKeyboardButton{Text: "‹ " + b.texts.T(loc, "bot.btn_prev", nil), CallbackData: pageCallback("rf:rp", page-1)})
	}
	row = append(row, models.InlineKeyboardButton{Text: strconv.Itoa(page), CallbackData: "rf:noop"})
	if hasNext {
		row = append(row, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.btn_next", nil) + " ›", CallbackData: pageCallback("rf:rp", page+1)})
	}
	return row
}

// referralFilterLabel is the chip text of a state filter.
func (b *Bot) referralFilterLabel(loc, filter string) string {
	if filter == "all" {
		return b.texts.T(loc, "bot.rfp.f_all", nil)
	}
	return b.texts.T(loc, "bot.rfp.rs_"+filter, nil)
}

// referralRewardText is one reward: the friend's order, the code, the cashback
// and where it stands. No buyer contact data comes from the API.
func (b *Bot) referralRewardText(loc string, it openapi.ReferralRewardItem) string {
	num := func(p *int64) string {
		if p == nil {
			return "–"
		}
		return "№" + strconv.FormatInt(*p, 10)
	}
	state := b.texts.T(loc, "bot.rfp.rs_"+it.State, nil)
	if it.State == "" {
		state = "–"
	}
	line := b.texts.T(loc, "bot.rfp.rw_row", map[string]any{
		"Friend": num(it.FriendOrderNumber), "Referrer": num(it.ReferrerOrderNumber), "Code": Esc(it.Code),
		"Amount": FormatMoney(it.CashbackMinor, it.Currency, loc), "State": state,
	})
	if it.Reason != nil {
		key := "bot.rfp.why_r_" + *it.Reason
		if text := b.texts.T(loc, key, nil); text != key {
			line += "\n" + text
		}
	}
	if it.FriendReversal != nil {
		line += "\n" + b.texts.T(loc, "bot.rfp.rw_reversal", nil)
	}
	return line
}

// asAPIError is errors.As for *APIError.
func asAPIError(err error, target **APIError) bool { return errors.As(err, target) }
