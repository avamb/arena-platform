package eventbot

// promo_create.go — the glue of the "new code" dialog and of the "Sessions"
// edit (EC-11, spec 35 §6.2): which screen each step draws, how an answer or a
// press reaches the pure state machine in promo.go, and the calls the dialog
// makes (the event and session lists, the currencies on sale, the create).

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// maxPromoCurrencyEvents is how many running events are read to find the
// currencies an organization sells in, and maxPromoCurrencyButtons how many
// of them become buttons.
const (
	maxPromoCurrencyEvents  = 6
	maxPromoCurrencyButtons = 4
)

// Error codes of the promo routes that have their own words.
const (
	promoCodeDuplicate    = "promo.duplicate"
	promoCodeBadValue     = "promo.invalid_discount_value"
	promoCodeBadCurrency  = "promo.invalid_currency"
	promoCodeNeedCurrency = "promo.currency_required"
	promoCodeBadSession   = "promo.invalid_session"
)

// promoAPIErrKey maps a refusal of the create route to the message that says,
// plainly, what to change. "" means the API's answer has no words of its own.
func promoAPIErrKey(code string, fixed bool) string {
	switch code {
	case promoCodeDuplicate:
		return "bot.promo.err_duplicate"
	case promoCodeBadValue:
		if fixed {
			return "bot.promo.err_amount"
		}
		return "bot.promo.err_percent"
	case promoCodeBadCurrency, promoCodeNeedCurrency:
		return "bot.promo.err_currency"
	case promoCodeBadSession:
		return "bot.promo.err_session_foreign"
	}
	return ""
}

// ─── start ────────────────────────────────────────────────────────────────────

// promoStart opens the dialog at the first question. The names of the
// organization's codes are read once, for the duplicate check; the server's
// own 409 still decides.
func (b *Bot) promoStart(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog) {
	codes, err := b.arena.ListPromoCodes(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	d := newPromoDraft()
	for _, c := range codes {
		d.Names = append(d.Names, strings.ToUpper(c.Code))
	}
	d.EventID, d.EventName = st.EventID, st.EventName
	st.Draft, st.Edit, st.CardID, st.IDs = d, nil, nil, nil
	b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, "")
}

// ─── presses and typed text ───────────────────────────────────────────────────

// promoCreatePress handles "pm:c:<data>".
func (b *Bot) promoCreatePress(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, step, data string) {
	loc := id.Locale()
	if st.Draft == nil || !strings.HasPrefix(step, "c_") {
		b.promoGone(ctx, chatID, editMsgID, loc)
		return
	}
	switch data {
	case "cancel":
		b.promoRenderList(ctx, chatID, editMsgID, id, jwt, promoListState(st), b.texts.T(loc, "bot.promo.cancelled", nil)+"\n\n")
		return
	case "go":
		if st.Draft.Step == pmStepConfirm {
			b.promoCreate(ctx, chatID, editMsgID, from, id, jwt, st)
			return
		}
	}
	b.promoDraftInput(ctx, chatID, editMsgID, from, id, jwt, st, "", data)
}

// promoGone answers a press with no live dialog behind it.
func (b *Bot) promoGone(ctx context.Context, chatID int64, editMsgID *int, loc string) {
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.promo.gone", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.promo.btn", nil), CallbackData: "pm:l"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// promoListState is the list the dialog was opened from: same event scope,
// first page, no draft.
func promoListState(st promoDialog) promoDialog {
	out := newPromoDialog(st.OrgID)
	out.EventID, out.EventName, out.MsgID = st.EventID, st.EventName, st.MsgID
	return out
}

// isCalData reports whether a press belongs to the wizard's date calendar.
func isCalData(data string) bool {
	return data == calNoop || data == confirmRetry ||
		strings.HasPrefix(data, calCallback) || strings.HasPrefix(data, pickCallback) || strings.HasPrefix(data, confirmPrefix)
}

// promoButtonOnly reports a step whose answer is a button: a text typed there
// is answered with a hint instead of being guessed at.
func promoButtonOnly(step string) bool {
	switch step {
	case pmStepType, pmStepScope, pmStepEvent, pmStepPick, pmStepStatus, pmStepConfirm:
		return true
	}
	return false
}

// promoDraftInput feeds a typed text or a button's data to the dialog and
// draws the result.
func (b *Bot) promoDraftInput(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, text, data string) {
	loc := id.Locale()
	d := st.Draft
	step := d.Step

	// The expiry is a calendar: a typed date is confirmed, a pressed day is final.
	if step == pmStepExpiry && ((data == "" && text != "") || isCalData(data)) {
		if d.Cal == nil {
			d.Cal = &Draft{Version: draftSchemaVersion, Step: stSDate}
		}
		out, handled, note := b.wizard.dateInput(loc, d.Cal, text, data)
		if handled {
			b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, noteLine(note))
			return
		}
		iso, ok := ParseDate(out)
		if !ok {
			b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.wz.err_date", nil)+"\n\n")
			return
		}
		text, data = "", "date:"+iso
	}
	// The currencies on sale are read when a fixed discount is chosen, so a
	// percent code costs no extra calls.
	if step == pmStepType && data == "fix" {
		d.CurOpts = b.promoCurrencies(ctx, jwt, id.Current.OrgID, d)
	}
	key := promoApply(d, text, data, d.existing())
	note := ""
	switch {
	case key != "":
		note = b.texts.T(loc, key, nil) + "\n\n"
	case text != "" && promoButtonOnly(step):
		note = b.texts.T(loc, "bot.promo.use_buttons", nil) + "\n\n"
	}
	b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, note)
}

func noteLine(note string) string {
	if note == "" {
		return ""
	}
	return note + "\n\n"
}

// promoPickPress handles "pm:k:<data>": the picker of the dialog or of the
// edit, whichever is open.
func (b *Bot) promoPickPress(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, step, data string) {
	loc := id.Locale()
	switch {
	case st.Draft != nil && (step == pmStepEvent || step == pmStepPick):
		b.promoDraftInput(ctx, chatID, editMsgID, from, id, jwt, st, "", data)
	case st.Edit != nil && (step == pmStepEditEv || step == pmStepEditPick):
		b.promoEditPress(ctx, chatID, editMsgID, from, id, jwt, st, step, data)
	default:
		b.promoGone(ctx, chatID, editMsgID, loc)
	}
}

// ─── drawing the dialog ───────────────────────────────────────────────────────

// promoShowDraft makes sure the step has what it shows (the events, the
// sessions), stores the state and draws the step.
func (b *Bot) promoShowDraft(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	d := st.Draft
	if d.Step == pmStepEvent && len(d.Events) == 0 {
		events, err := b.promoLoadEvents(ctx, jwt, orgID)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "pm:b")
			return
		}
		if len(events) == 0 {
			d.back()
			note += b.texts.T(loc, "bot.promo.err_no_events", nil) + "\n\n"
		}
		d.Events, d.EvPage = events, 1
	}
	if d.Step == pmStepPick && d.Pick == nil && d.EventID != nil {
		opts, err := b.promoLoadSessionOptions(ctx, jwt, orgID, *d.EventID)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "pm:b")
			return
		}
		if len(opts) == 0 {
			// Nothing to tick: back to where the sessions were chosen.
			d.EventID, d.EventName = nil, ""
			d.Step = pmStepScope
			note += b.texts.T(loc, "bot.promo.err_no_sessions", nil) + "\n\n"
		} else {
			d.Pick = newPromoPick(opts, nil)
		}
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, d.Step)
	text, rows := b.promoDraftScreen(loc, d)
	b.reply(ctx, chatID, editMsgID, clipMessage(note+text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// pmBtn is a button of the dialog; data without a "pm:" prefix is an answer
// of the dialog itself ("pm:c:<data>").
func pmBtn(label, data string) models.InlineKeyboardButton {
	if !strings.HasPrefix(data, "pm:") {
		data = "pm:c:" + data
	}
	return models.InlineKeyboardButton{Text: label, CallbackData: data}
}

// promoNav is the Back / Cancel row under every step. The first step has no
// Back.
func (b *Bot) promoNav(loc string, d *promoDraft) []models.InlineKeyboardButton {
	cancel := pmBtn(b.texts.T(loc, "bot.promo.cancel_btn", nil), "cancel")
	if len(d.Hist) == 0 {
		return []models.InlineKeyboardButton{cancel}
	}
	return []models.InlineKeyboardButton{pmBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "back"), cancel}
}

// promoDraftScreen draws the current step of the dialog. It reads the draft
// only, so a test can render every step in every language.
func (b *Bot) promoDraftScreen(loc string, d *promoDraft) (string, [][]models.InlineKeyboardButton) {
	t := func(key string, vals map[string]any) string { return b.texts.T(loc, key, vals) }
	sofar := ""
	if d.Code != "" {
		sofar = "\n✔ <b>" + Esc(d.Code) + "</b>"
		if d.Value > 0 {
			sofar += " · " + b.promoDiscountText(loc, d.item())
		}
	}
	head := t("bot.promo.c_head", map[string]any{"Text": sofar}) + "\n\n"
	nav := b.promoNav(loc, d)
	skip := func() []models.InlineKeyboardButton {
		return []models.InlineKeyboardButton{pmBtn(t("bot.promo.skip_btn", nil), "skip")}
	}

	switch d.Step {
	case pmStepCode:
		return head + t("bot.promo.q_code", nil), [][]models.InlineKeyboardButton{nav}
	case pmStepType:
		return head + t("bot.promo.q_type", nil), [][]models.InlineKeyboardButton{
			{pmBtn(t("bot.promo.type_pct_btn", nil), "pct"), pmBtn(t("bot.promo.type_fix_btn", nil), "fix")}, nav,
		}
	case pmStepPercent:
		return head + t("bot.promo.q_percent", nil), [][]models.InlineKeyboardButton{nav}
	case pmStepAmount:
		return head + t("bot.promo.q_amount", nil), [][]models.InlineKeyboardButton{nav}
	case pmStepCurrency:
		rows := [][]models.InlineKeyboardButton{}
		var line []models.InlineKeyboardButton
		for _, c := range d.CurOpts {
			line = append(line, pmBtn(c, "cur:"+c))
		}
		if len(line) > 0 {
			rows = append(rows, line)
		}
		return head + t("bot.promo.q_currency", nil), append(rows, nav)
	case pmStepScope:
		q := t("bot.promo.q_scope", nil)
		if d.EventID != nil {
			q = t("bot.promo.q_scope_event", map[string]any{"Name": Esc(d.EventName)})
		}
		return head + q, [][]models.InlineKeyboardButton{
			{pmBtn(t("bot.promo.scope_all_btn", nil), "all")},
			{pmBtn(t("bot.promo.scope_pick_btn", nil), "pick")}, nav,
		}
	case pmStepEvent:
		text, rows := b.promoEventScreen(loc, &d.promoPicker, "bot.promo.q_event")
		return head + text, append(rows, nav)
	case pmStepPick:
		cur := ""
		if d.fixed() {
			cur = d.Currency
		}
		text, rows := b.promoPickScreen(loc, &d.promoPicker, cur, "")
		return head + text, append(rows, nav)
	case pmStepTotal:
		return head + t("bot.promo.q_total", nil), [][]models.InlineKeyboardButton{skip(), nav}
	case pmStepPer:
		return head + t("bot.promo.q_per", nil), [][]models.InlineKeyboardButton{skip(), nav}
	case pmStepExpiry:
		if d.Cal == nil {
			d.Cal = &Draft{Version: draftSchemaVersion, Step: stSDate}
		}
		scr := b.wizard.dateQuestion(loc, d.Cal, head, t("bot.promo.q_expiry", nil), func(rows ...[]Button) [][]Button {
			out := append([][]Button{}, rows...)
			if len(d.Cal.Scratch.DatePending) == 0 {
				out = append(out, []Button{{Label: t("bot.promo.skip_exp_btn", nil), Data: "skip"}})
			}
			return append(out, []Button{{Label: "« " + t("bot.btn_back", nil), Data: "back"}, {Label: t("bot.promo.cancel_btn", nil), Data: "cancel"}})
		})
		rows := make([][]models.InlineKeyboardButton, 0, len(scr.Buttons))
		for _, r := range scr.Buttons {
			line := make([]models.InlineKeyboardButton, 0, len(r))
			for _, bt := range r {
				line = append(line, pmBtn(bt.Label, bt.Data))
			}
			rows = append(rows, line)
		}
		return scr.Text, rows
	case pmStepStatus:
		return head + t("bot.promo.q_status", nil), [][]models.InlineKeyboardButton{
			{pmBtn(t("bot.promo.status_active_btn", nil), "st:active"), pmBtn(t("bot.promo.status_paused_btn", nil), "st:paused")}, nav,
		}
	case pmStepConfirm:
		return t("bot.promo.summary", map[string]any{"Text": b.promoSummaryText(loc, d)}), [][]models.InlineKeyboardButton{
			{pmBtn(t("bot.promo.create_btn", nil), "go")}, nav,
		}
	}
	return head, [][]models.InlineKeyboardButton{nav}
}

// promoEventScreen is the event chooser: five running events a page. The
// presses are "pm:k:ev:<index>" over the whole list.
func (b *Bot) promoEventScreen(loc string, p *promoPicker, key string) (string, [][]models.InlineKeyboardButton) {
	if p.EvPage < 1 {
		p.EvPage = 1
	}
	pages := PagesFor(int64(len(p.Events)), listPageSize)
	if p.EvPage > pages {
		p.EvPage = pages
	}
	start := (p.EvPage - 1) * listPageSize
	end := start + listPageSize
	if end > len(p.Events) {
		end = len(p.Events)
	}
	var rows [][]models.InlineKeyboardButton
	for i := start; i < end; i++ {
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(p.Events[i].Label, 48), CallbackData: ItemCallback("pm:k:ev", i)}})
	}
	if nav := b.promoPager(loc, Pager{Prefix: "pm:k:evp", Page: p.EvPage, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	return b.texts.T(loc, key, nil), rows
}

// promoPickScreen is the session checkboxes of one event, five a page. A
// session in another currency than a fixed code's is marked and cannot be
// ticked. note is a line under the question.
func (b *Bot) promoPickScreen(loc string, p *promoPicker, fixedCur, note string) (string, [][]models.InlineKeyboardButton) {
	pick := p.Pick
	if pick == nil {
		return b.texts.T(loc, "bot.promo.err_no_sessions", nil), nil
	}
	pages := pick.pages()
	if pick.Page < 1 {
		pick.Page = 1
	}
	if pick.Page > pages {
		pick.Page = pages
	}
	start := (pick.Page - 1) * listPageSize
	end := start + listPageSize
	if end > len(pick.Options) {
		end = len(pick.Options)
	}
	var rows [][]models.InlineKeyboardButton
	for i := start; i < end; i++ {
		o := pick.Options[i]
		mark := "☐ "
		if pick.Sel[i] {
			mark = "☑ "
		}
		label := mark + o.Label
		if fixedCur != "" && o.Currency != "" && !strings.EqualFold(o.Currency, fixedCur) {
			label = "🚫 " + o.Label + " · " + o.Currency
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: ItemCallback("pm:k:t", i)}})
	}
	if nav := b.promoPager(loc, Pager{Prefix: "pm:k:pg", Page: pick.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: b.texts.T(loc, "bot.promo.pick_done_btn", map[string]any{"N": len(pick.checked())}), CallbackData: "pm:k:ok"},
		{Text: b.texts.T(loc, "bot.promo.pick_event_btn", nil), CallbackData: "pm:k:chev"},
	})
	text := b.texts.T(loc, "bot.promo.q_pick", map[string]any{"Name": Esc(p.EventName)})
	if fixedCur != "" {
		text += "\n" + b.texts.T(loc, "bot.promo.only_currency", map[string]any{"Currency": fixedCur})
	}
	return note + text, rows
}

// ─── the calls ────────────────────────────────────────────────────────────────

// promoLoadEvents lists the events a code can be pointed at: the running ones,
// in the order of the events list.
func (b *Bot) promoLoadEvents(ctx context.Context, jwt string, orgID uuid.UUID) ([]promoOption, error) {
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		return nil, err
	}
	var out []promoOption
	for _, e := range FilterEvents(events, evFilterRun, "") {
		if len(out) >= maxPromoEvents {
			break
		}
		out = append(out, promoOption{ID: e.Id, Label: e.Name})
	}
	return out, nil
}

// promoLoadSessionOptions lists the sessions of an event that a code can still
// be used on: not cancelled and not over. The zone of each comes from the
// event's summary; without it the time is shown in UTC and says so.
func (b *Bot) promoLoadSessionOptions(ctx context.Context, jwt string, orgID, eventID uuid.UUID) ([]promoOption, error) {
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		return nil, err
	}
	zones := map[uuid.UUID]string{}
	if sum, err := b.arena.EventSummary(ctx, jwt, orgID, eventID); err != nil {
		b.logger.Warn("eventbot: promo session zones unavailable", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
	} else {
		for _, s := range sum.Sessions {
			if s.VenueTimezone != nil {
				zones[s.Id] = *s.VenueTimezone
			}
		}
	}
	now := time.Now()
	var out []promoOption
	for _, s := range sessions {
		if string(s.Status) == "cancelled" || !s.EndAt.After(now) {
			continue
		}
		out = append(out, promoSessionOption(s, zones[s.Id]))
	}
	return out, nil
}

// promoCurrencies finds the currencies a fixed discount can be in: those of
// the event the code starts on, else those of the first running events. An
// organization that sells in one currency is never asked.
func (b *Bot) promoCurrencies(ctx context.Context, jwt string, orgID uuid.UUID, d *promoDraft) []string {
	var seen []string
	add := func(sessions []openapi.SessionItem) {
		for _, s := range sessions {
			c := strings.ToUpper(strings.TrimSpace(s.Currency))
			if c == "" {
				continue
			}
			dup := false
			for _, x := range seen {
				dup = dup || x == c
			}
			if !dup {
				seen = append(seen, c)
			}
		}
	}
	if d.EventID != nil {
		sessions, err := b.arena.ListSessions(ctx, jwt, orgID, *d.EventID)
		if err != nil {
			b.logger.Warn("eventbot: promo currencies unavailable", slog.String("error", err.Error()))
			return nil
		}
		add(sessions)
	} else {
		events, err := b.arena.ListEvents(ctx, jwt, orgID)
		if err != nil {
			b.logger.Warn("eventbot: promo currencies unavailable", slog.String("error", err.Error()))
			return nil
		}
		for i, e := range FilterEvents(events, evFilterRun, "") {
			if i >= maxPromoCurrencyEvents {
				break
			}
			sessions, err := b.arena.ListSessions(ctx, jwt, orgID, e.Id)
			if err != nil {
				continue
			}
			add(sessions)
		}
	}
	if len(seen) > maxPromoCurrencyButtons {
		seen = seen[:maxPromoCurrencyButtons]
	}
	return seen
}

// promoCreate makes the call and shows what came of it: the new code's card,
// or the plain reason it was refused.
func (b *Bot) promoCreate(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog) {
	loc := id.Locale()
	d := st.Draft
	in, ok := d.create()
	if !ok {
		b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.promo.err_incomplete", nil)+"\n\n")
		return
	}
	it, err := b.arena.CreatePromoCode(ctx, jwt, id.Current.OrgID, in)
	if err != nil {
		code := APIErrorCode(err)
		if key := promoAPIErrKey(code, d.fixed()); key != "" {
			// The refusal is told in words and the dialog returns to the
			// question it is about. A taken name goes on the list, so the same
			// one is not tried twice.
			if code == promoCodeDuplicate {
				d.Names = append(d.Names, d.Code)
				d.Step = pmStepCode
				d.Hist = nil
			} else if code == promoCodeBadValue && !d.fixed() {
				d.Step = pmStepPercent
			} else if code == promoCodeBadValue {
				d.Step = pmStepAmount
			}
			b.promoShowDraft(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, key, nil)+"\n\n")
			return
		}
		b.ecError(ctx, chatID, editMsgID, id, err, "pm:b")
		return
	}
	next := promoListState(st)
	b.promoShowCard(ctx, chatID, editMsgID, id, jwt, next, it.Id, b.texts.T(loc, "bot.promo.created", map[string]any{"Code": Esc(it.Code)})+"\n\n")
}

// ─── the Sessions edit ────────────────────────────────────────────────────────

// promoShowSessions is the card's "Sessions" screen: where the code works now,
// and the two ways to change it.
func (b *Bot) promoShowSessions(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog, codeID uuid.UUID, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	it, err := b.arena.GetPromoCode(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	lines, more := b.promoSessionLines(ctx, jwt, loc, orgID, it)
	var sb strings.Builder
	sb.WriteString(b.promoSessionsText(loc, it))
	for _, l := range lines {
		sb.WriteString("\n" + l)
	}
	if more > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.promo.card_sess_more", map[string]any{"N": more}))
	}
	text := note + b.texts.T(loc, "bot.promo.sess_head", map[string]any{"Code": Esc(it.Code), "Text": sb.String()})

	st.CardID, st.CardCode, st.CardUses = &it.Id, it.Code, it.Uses
	st.Draft, st.Edit = nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, pmStepEdit)

	allLabel := b.texts.T(loc, "bot.promo.sess_all_btn", nil)
	onlyLabel := b.texts.T(loc, "bot.promo.sess_only_btn", nil)
	if promoIsClub(it) {
		allLabel = "✔ " + allLabel
	} else {
		onlyLabel = "✔ " + onlyLabel
	}
	code := it.Id.String()
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: allLabel, CallbackData: "pm:sa:" + code}},
		{{Text: onlyLabel, CallbackData: "pm:so:" + code}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pm:v:" + code}, {Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// promoEditStart opens the event chooser of the "only the checked sessions"
// edit.
func (b *Bot) promoEditStart(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, codeID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	it, err := b.arena.GetPromoCode(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	events, err := b.promoLoadEvents(ctx, jwt, orgID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	if len(events) == 0 {
		b.promoShowSessions(ctx, chatID, editMsgID, id, jwt, st, codeID, b.texts.T(loc, "bot.promo.err_no_events", nil)+"\n\n")
		return
	}
	e := &promoEdit{CodeID: it.Id, Code: it.Code, Existing: append([]uuid.UUID{}, it.AppliesToSessionIds...)}
	e.Fixed = string(it.DiscountType) == promoTypeFixed
	if it.Currency != nil {
		e.Currency = strings.ToUpper(*it.Currency)
	}
	e.Events, e.EvPage = events, 1
	st.Edit, st.Draft = e, nil
	st.CardID, st.CardCode, st.CardUses = &it.Id, it.Code, it.Uses
	b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditEv, "")
}

// promoShowEdit stores the state and draws the edit's current screen.
func (b *Bot) promoShowEdit(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st promoDialog, step, note string) {
	loc := id.Locale()
	e := st.Edit
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, step)
	code := e.CodeID.String()
	back := []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pm:se:" + code},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	}
	var text string
	var rows [][]models.InlineKeyboardButton
	if step == pmStepEditEv {
		text, rows = b.promoEventScreen(loc, &e.promoPicker, "bot.promo.q_event_edit")
	} else {
		cur := ""
		if e.Fixed {
			cur = e.Currency
		}
		text, rows = b.promoPickScreen(loc, &e.promoPicker, cur, "")
		text += "\n" + b.texts.T(loc, "bot.promo.pick_edit_note", nil)
	}
	head := b.texts.T(loc, "bot.promo.e_head", map[string]any{"Code": Esc(e.Code)}) + "\n\n"
	b.reply(ctx, chatID, editMsgID, clipMessage(note+head+text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: append(rows, back)})
}

// promoEditPress applies one press of the edit's picker and, on "Done",
// writes the new session list.
func (b *Bot) promoEditPress(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, step, data string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	e := st.Edit
	cur := ""
	if e.Fixed {
		cur = e.Currency
	}
	allowEmpty := false
	var eventSessions map[uuid.UUID]bool
	if e.Pick != nil {
		ids := make([]uuid.UUID, 0, len(e.Pick.Options))
		for _, o := range e.Pick.Options {
			ids = append(ids, o.ID)
		}
		eventSessions = uuidSet(ids)
		// Removing the event's own sessions is fine while the code keeps some
		// on other events; an empty list would silently make it a club code.
		allowEmpty = len(promoMergeSessions(e.Existing, eventSessions, nil)) > 0
	}
	outcome, key := e.press(data, cur, allowEmpty)
	note := ""
	if key != "" {
		note = b.texts.T(loc, key, nil) + "\n\n"
	}
	switch outcome {
	case pickEvent:
		opts, err := b.promoLoadSessionOptions(ctx, jwt, orgID, *e.EventID)
		if err != nil {
			b.promoError(ctx, chatID, editMsgID, id, err)
			return
		}
		if len(opts) == 0 {
			e.EventID, e.EventName = nil, ""
			b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditEv, b.texts.T(loc, "bot.promo.err_no_sessions", nil)+"\n\n")
			return
		}
		e.Pick = newPromoPick(opts, uuidSet(e.Existing))
		b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditPick, "")
	case pickChevent:
		b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditEv, "")
	case pickDone:
		merged := promoMergeSessions(e.Existing, eventSessions, e.Pick.checked())
		if len(merged) == 0 {
			b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditPick, b.texts.T(loc, "bot.promo.err_pick_none", nil)+"\n\n")
			return
		}
		it, err := b.arena.SetPromoSessions(ctx, jwt, orgID, e.CodeID, merged)
		if err != nil {
			if IsAPIError(err, http.StatusUnprocessableEntity) {
				b.promoShowEdit(ctx, chatID, editMsgID, id, st, pmStepEditPick, b.texts.T(loc, "bot.promo.err_session_foreign", nil)+"\n\n")
				return
			}
			b.promoError(ctx, chatID, editMsgID, id, err)
			return
		}
		b.promoShowCard(ctx, chatID, editMsgID, id, jwt, promoListState(st), it.Id,
			b.texts.T(loc, "bot.promo.sess_saved", map[string]any{"Code": Esc(it.Code), "N": len(it.AppliesToSessionIds)})+"\n\n")
	default:
		b.promoShowEdit(ctx, chatID, editMsgID, id, st, step, note)
	}
}
