package eventbot

// sessions_dialog.go — the "Sessions" screens of an event: pick one date,
// MOVE it to another date and time, or CANCEL it (or the whole event).
//
// Moving a date is not adding one. The event card's "+ Date" creates a second
// session; this dialog rewrites the EXISTING session in place, so the tickets
// already sold stay in it and the event never shows two sessions. The change
// goes through PATCH of that session, whose server side
// (internal/platform/sessionchange) queues the buyers' letters in the same
// transaction — the bot therefore always shows the numbers first (the dry run
// GET .../change-impact), lets the organizer edit the message the buyers will
// read, and writes only after one explicit press.
//
// Unlike the event wizard this dialog is not a draft: every move or cancel is
// applied at once and cannot be "saved for later", so its state lives in
// memory for sesDialogTTL and a restart merely asks the person to open the
// sessions again. It reuses the wizard's calendar and typed-date confirmation
// (wizard_dates.go) through a throwaway Draft that carries only the venue
// zone, so a date can never be mistyped into the past.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

const sesDialogTTL = 30 * time.Minute

// Dialog modes.
const (
	sesModeMove      = "move"
	sesModeCancel    = "cancel"
	sesModeCancelAll = "cancel_all"
)

// Dialog steps.
const (
	sesStepList    = "list"
	sesStepCard    = "card"
	sesStepDate    = "date"
	sesStepTime    = "time"
	sesStepConfirm = "confirm"
	// sesStepType is the last step of a CANCELLATION: the organizer has to
	// type the cancel word. A button press alone never cancels a session —
	// buyers are emailed and there is no undo.
	sesStepType  = "type"
	sesStepMsg   = "msg"
	sesStepEmail = "email"
	sesStepPhone = "phone"
	sesStepHide  = "hide"
	sesStepDone  = "done"
)

// sesItem is one session of the open event.
type sesItem struct {
	ID     uuid.UUID
	Start  time.Time
	Status string
	Tz     string
	Active int64 // live tickets; -1 when the summary could not be read
	Raw    openapi.SessionItem
}

func (i sesItem) cancelled() bool { return i.Status == "cancelled" }

// sesImpact is the dry run, merged over the sessions a cancel-all touches.
type sesImpact struct {
	Kinds      []string
	Orders     int
	Tickets    int
	NoAddress  int
	SiteOrders int
	Blocked    string
	Contact    openapi.OrganizerContact
	Default    string
	Old, New   string // formatted dates (move)
}

type sessionDialog struct {
	OrgID     uuid.UUID
	EventID   uuid.UUID
	EventName string
	Items     []sesItem
	Cur       int
	Mode      string
	Step      string
	// Cal is the throwaway draft the wizard's calendar runs on.
	Cal        *Draft
	Date, Time string // the chosen local date (YYYY-MM-DD) and time (HH:MM)
	// Message is the organizer's text for the buyers; MessageSet is true once
	// they edited or cleared it (until then it is the suggested default).
	Message    string
	MessageSet bool
	Impact     *sesImpact
	Email      string
	Phone      string
	MsgID      int
	// Result is what the last write reported, for the done screen.
	Result string
	// CanSales: the person may read sales figures, so the card offers the
	// summary and the export of the date (event_summary.go, event_csv.go).
	CanSales bool
	expires  time.Time
}

// sessionDialogs is still process memory, so a bot restart drops a move or
// cancellation half-way; it is due to move onto DialogStore (dialogs.go, the
// bot_dialogs table) the way the team invite already has.
type sessionDialogs struct {
	mu     sync.Mutex
	byID   map[int64]*sessionDialog
	lapsed map[int64]bool
}

func newSessionDialogs() *sessionDialogs {
	return &sessionDialogs{byID: map[int64]*sessionDialog{}, lapsed: map[int64]bool{}}
}

func (s *sessionDialogs) put(id int64, d *sessionDialog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.expires = time.Now().Add(sesDialogTTL)
	delete(s.lapsed, id)
	s.byID[id] = d
}

func (s *sessionDialogs) get(id int64) (*sessionDialog, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(d.expires) {
		delete(s.byID, id)
		s.lapsed[id] = true
		return nil, false
	}
	d.expires = time.Now().Add(sesDialogTTL)
	return d, true
}

func (s *sessionDialogs) clear(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *sessionDialogs) takeLapsed(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.lapsed[id]
	delete(s.lapsed, id)
	return was
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// sesStart turns the chosen local date and time into the instant the session
// starts at, in the venue's zone.
func sesStart(date, hhmm, tz string) (time.Time, error) {
	loc := time.UTC
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, err
		}
		loc = l
	}
	// allow:timeformat: parsing the organizer's own date and time, not a wire timestamp
	return time.ParseInLocation("2006-01-02 15:04", date+" "+hhmm, loc)
}

func (i sesItem) localTime(tz string) string {
	loc := time.UTC
	if l, err := time.LoadLocation(tz); tz != "" && err == nil {
		loc = l
	}
	// allow:timeformat: clock time shown on a chat button, not a wire timestamp
	return i.Start.In(loc).Format("15:04")
}

// sesKeyboard turns wizard-style buttons into inline buttons whose callbacks
// carry the "ses:" prefix.
func sesKeyboard(rows [][]Button) *models.InlineKeyboardMarkup {
	out := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, r := range rows {
		line := make([]models.InlineKeyboardButton, 0, len(r))
		for _, b := range r {
			line = append(line, models.InlineKeyboardButton{Text: b.Label, CallbackData: "ses:" + b.Data})
		}
		out = append(out, line)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: out}
}

func contactLabel(c openapi.OrganizerContact) string {
	parts := []string{}
	if c.Name != "" {
		parts = append(parts, Esc(c.Name))
	}
	if c.Email != "" {
		parts = append(parts, Esc(c.Email))
	}
	if c.Phone != "" && !c.PhoneHidden {
		parts = append(parts, Esc(c.Phone))
	}
	return strings.Join(parts, ", ")
}

func (b *Bot) sesT(loc, key string, vals map[string]any) string { return b.texts.T(loc, key, vals) }

// sesBtn builds a button labelled by a catalog key.
func (b *Bot) sesBtn(loc, key, data string, vals map[string]any) Button {
	return Button{Label: b.sesT(loc, key, vals), Data: data}
}

func (b *Bot) sesNav(loc string, back string) []Button {
	row := []Button{}
	if back != "" {
		row = append(row, Button{Label: "« " + b.sesT(loc, "bot.btn_back", nil), Data: back})
	}
	return row
}

// sesHome is the row every screen ends with. It uses the plain "home" callback
// of the bot, so its prefix is rewritten by sesKeyboard — handled by "home" in
// sessionsCallback.
func (b *Bot) sesHomeRow(loc string) []Button {
	return []Button{{Label: b.sesT(loc, "bot.btn_home", nil), Data: "home"}}
}

// ─── loading ──────────────────────────────────────────────────────────────────

// loadSesItems lists the event's sessions with each one's zone and live
// ticket count (best effort: a summary that fails leaves the count unknown).
func (b *Bot) loadSesItems(ctx context.Context, jwt string, orgID, eventID uuid.UUID) ([]sesItem, error) {
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		return nil, err
	}
	items := make([]sesItem, 0, len(sessions))
	for _, s := range sessions {
		it := sesItem{ID: s.Id, Start: s.StartAt, Status: string(s.Status), Active: -1, Raw: s}
		if sum, err := b.arena.SessionSummary(ctx, jwt, orgID, s.Id); err == nil {
			if sum.Session.VenueTimezone != nil {
				it.Tz = *sum.Session.VenueTimezone
			}
			it.Active = sum.Tickets.Active
		} else {
			b.logger.Warn("eventbot: session summary failed", slog.String("session_id", s.Id.String()), slog.String("error", err.Error()))
		}
		items = append(items, it)
	}
	return items, nil
}

// ─── entry ────────────────────────────────────────────────────────────────────

// sessionsOpen starts the dialog for an event and shows its sessions.
func (b *Bot) sessionsOpen(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	orgID := id.Current.OrgID
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		b.replyAPIError(ctx, chatID, editMsgID, id, err)
		return
	}
	name := ""
	for _, e := range events {
		if e.Id == eventID {
			name = e.Name
		}
	}
	if name == "" {
		b.showEvents(ctx, chatID, editMsgID, from, 1)
		return
	}
	items, err := b.loadSesItems(ctx, jwt, orgID, eventID)
	if err != nil {
		b.replyAPIError(ctx, chatID, editMsgID, id, err)
		return
	}
	dlg := &sessionDialog{OrgID: orgID, EventID: eventID, EventName: name, Items: items, Cur: -1, Step: sesStepList, CanSales: canViewSales(id)}
	if editMsgID != nil {
		dlg.MsgID = *editMsgID
	}
	b.sessions.put(from.ID, dlg)
	b.sesShow(ctx, chatID, editMsgID, id, jwt, from, dlg, "")
}

// sesReply edits the dialog's one message (or sends it).
func (b *Bot) sesReply(ctx context.Context, chatID int64, msgID *int, dlg *sessionDialog, text string, rows [][]Button) {
	if msgID == nil && dlg.MsgID != 0 {
		m := dlg.MsgID
		msgID = &m
	}
	b.reply(ctx, chatID, msgID, text, sesKeyboard(rows))
}

// sesShow renders the screen of the dialog's current step.
func (b *Bot) sesShow(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, _ *models.User, dlg *sessionDialog, note string) {
	loc := id.Locale()
	prefix := ""
	if note != "" {
		prefix = note + "\n\n"
	}
	switch dlg.Step {
	case sesStepList:
		b.sesShowList(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepCard:
		b.sesShowCard(ctx, chatID, msgID, jwt, loc, dlg, prefix)
	case sesStepDate:
		b.sesShowDate(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepTime:
		b.sesShowTime(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepConfirm:
		b.sesShowConfirm(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepMsg:
		b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.msg_ask", nil), [][]Button{
			{b.sesBtn(loc, "bot.ses.msg_clear_btn", "nomsg", nil), b.sesBtn(loc, "bot.ses.msg_default_btn", "defmsg", nil)},
			b.sesNav(loc, "confirm"),
		})
	case sesStepType:
		b.sesShowType(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepSalesEnd:
		b.sesShowSalesEnd(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepDoors:
		b.sesShowDoors(ctx, chatID, msgID, loc, dlg, prefix)
	case sesStepEmail:
		name := ""
		if dlg.Impact != nil {
			name = dlg.Impact.Contact.TargetName
		}
		b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.contact_ask_email", map[string]any{"Name": Esc(name)}), [][]Button{
			b.sesNav(loc, "card"), b.sesHomeRow(loc),
		})
	case sesStepPhone:
		b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.contact_ask_phone", nil), [][]Button{
			{b.sesBtn(loc, "bot.ses.contact_no_phone_btn", "nophone", nil)},
			b.sesNav(loc, "card"),
		})
	case sesStepHide:
		b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.contact_show_phone_q", nil), [][]Button{
			{b.sesBtn(loc, "bot.ses.contact_show_btn", "hide:0", nil), b.sesBtn(loc, "bot.ses.contact_hide_btn", "hide:1", nil)},
		})
	case sesStepDone:
		b.sesReply(ctx, chatID, msgID, dlg, prefix+dlg.Result, [][]Button{
			{{Label: b.sesT(loc, "bot.ses.to_sessions_btn", nil), Data: "list:" + dlg.EventID.String()}},
			b.sesHomeRow(loc),
		})
	}
}

func (b *Bot) sesShowList(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	text := prefix + b.sesT(loc, "bot.ses.list_title", map[string]any{"Name": Esc(dlg.EventName)})
	rows := [][]Button{}
	if len(dlg.Items) == 0 {
		text += "\n\n" + b.sesT(loc, "bot.ses.list_empty", nil)
	} else {
		text += "\n\n" + b.sesT(loc, "bot.ses.card_hint", nil)
	}
	active := 0
	for n, it := range dlg.Items {
		label := FormatWhen(it.Start, it.Tz)
		if it.cancelled() {
			label = "✖ " + label + " · " + b.sesT(loc, "bot.session_cancelled", nil)
		} else {
			active++
			if it.Active > 0 {
				label += " · 🎟 " + fmt.Sprint(it.Active)
			}
		}
		rows = append(rows, []Button{{Label: label, Data: fmt.Sprintf("o:%d", n)}})
	}
	if active > 1 {
		rows = append(rows, []Button{b.sesBtn(loc, "bot.ses.cancel_all_btn", "cxall", nil)})
	}
	rows = append(rows, []Button{{Label: "« " + b.sesT(loc, "bot.btn_back", nil), Data: "event"}}, b.sesHomeRow(loc))
	b.sesReply(ctx, chatID, msgID, dlg, text, rows)
}

func (b *Bot) sesShowCard(ctx context.Context, chatID int64, msgID *int, jwt, loc string, dlg *sessionDialog, prefix string) {
	if dlg.Cur < 0 || dlg.Cur >= len(dlg.Items) {
		dlg.Step = sesStepList
		b.sesShowList(ctx, chatID, msgID, loc, dlg, prefix)
		return
	}
	it := dlg.Items[dlg.Cur]
	text := prefix + "<b>" + Esc(dlg.EventName) + "</b>\n" + b.sessionLine(ctx, jwt, loc, dlg.OrgID, it.Raw)
	rows := [][]Button{}
	if dlg.CanSales {
		rows = append(rows, []Button{b.sesBtn(loc, "bot.ec.summary_btn", "sm", nil), b.sesBtn(loc, "bot.ec.csv_btn", "csv", nil)})
		rows = append(rows, []Button{b.sesBtn(loc, "bot.ord.btn", "or", nil)})
	}
	if !it.cancelled() {
		text += "\n" + b.sesSaleTimesLine(loc, it)
		text += "\n\n" + b.sesT(loc, "bot.ses.card_hint", nil)
		if dlg.CanSales {
			rows = append(rows, []Button{b.sesBtn(loc, "bot.cat.btn", "ct", nil)}) // categories of the date (EC-15)
		}
		rows = append(rows,
			[]Button{b.sesBtn(loc, "bot.ses.move_btn", "mv", nil)},
			[]Button{b.sesBtn(loc, "bot.ses.sales_end_btn", "se", nil), b.sesBtn(loc, "bot.ses.doors_btn", "dr", nil)},
			[]Button{b.sesBtn(loc, "bot.ses.cancel_btn", "cx", nil)},
		)
	}
	rows = append(rows, b.sesNav(loc, "list:"+dlg.EventID.String()), b.sesHomeRow(loc))
	b.sesReply(ctx, chatID, msgID, dlg, text, rows)
}

func (b *Bot) sesShowDate(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	it := dlg.Items[dlg.Cur]
	question := b.sesT(loc, "bot.ses.move_date_q", map[string]any{"Now": Esc(FormatWhen(it.Start, it.Tz))})
	nav := func(rows ...[]Button) [][]Button {
		return append(rows, b.sesNav(loc, "card"), b.sesHomeRow(loc))
	}
	scr := b.wizard.dateQuestion(loc, dlg.Cal, prefix, question, nav)
	b.sesReply(ctx, chatID, msgID, dlg, scr.Text, scr.Buttons)
}

func (b *Bot) sesShowTime(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	it := dlg.Items[dlg.Cur]
	cur := it.localTime(it.Tz)
	b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.move_time_q", map[string]any{
		"Date": Esc(DisplayDate(dlg.Date)), "Time": cur,
	}), [][]Button{
		{b.sesBtn(loc, "bot.ses.keep_time_btn", "t:"+cur, map[string]any{"Time": cur})},
		b.sesNav(loc, "mv"), b.sesHomeRow(loc),
	})
}

func (b *Bot) sesShowConfirm(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	imp := dlg.Impact
	if imp == nil {
		dlg.Step = sesStepCard
		return
	}
	var head string
	switch dlg.Mode {
	case sesModeMove:
		head = b.sesT(loc, "bot.ses.confirm_move", map[string]any{"Old": Esc(imp.Old), "New": Esc(imp.New)})
	case sesModeCancelAll:
		head = b.sesT(loc, "bot.ses.confirm_cancel_all", map[string]any{"Name": Esc(dlg.EventName), "N": b.activeCount(dlg)})
	default:
		head = b.sesT(loc, "bot.ses.confirm_cancel", map[string]any{"Old": Esc(imp.Old)})
	}
	text := prefix + head
	back := b.sesNav(loc, "card")
	if dlg.Mode == sesModeCancelAll {
		back = b.sesNav(loc, "list:"+dlg.EventID.String())
	}
	rows := [][]Button{}

	switch {
	case dlg.Mode == sesModeMove && len(imp.Kinds) == 0:
		text += "\n\n" + b.sesT(loc, "bot.ses.no_change", nil)
		rows = append(rows, back, b.sesHomeRow(loc))
	case imp.Blocked == sessionchange.BlockedSiteRoute:
		text += "\n\n" + b.sesT(loc, "bot.ses.site_blocked", map[string]any{"N": imp.SiteOrders})
		rows = append(rows, back, b.sesHomeRow(loc))
	case imp.Orders == 0:
		text += "\n\n" + b.sesT(loc, "bot.ses.no_buyers", nil)
		rows = append(rows, []Button{b.sesBtn(loc, b.goKey(dlg, false), "go", nil)}, back, b.sesHomeRow(loc))
	default:
		buyersKey := "bot.ses.buyers_move"
		if dlg.Mode != sesModeMove {
			buyersKey = "bot.ses.buyers_cancel"
		}
		text += "\n\n" + b.sesT(loc, buyersKey, map[string]any{
			"Tickets": imp.Tickets, "Orders": imp.Orders, "Contact": contactLabel(imp.Contact),
		})
		if imp.NoAddress > 0 {
			text += "\n" + b.sesT(loc, "bot.ses.no_address", map[string]any{"N": imp.NoAddress})
		}
		if dlg.Message != "" {
			text += "\n\n" + b.sesT(loc, "bot.ses.message_label", map[string]any{"Text": Esc(dlg.Message)})
		} else {
			text += "\n\n" + b.sesT(loc, "bot.ses.message_none", nil)
		}
		if dlg.Mode != sesModeMove {
			text += "\n\n" + b.sesT(loc, "bot.ses.refund_note", nil)
		}
		rows = append(rows,
			[]Button{b.sesBtn(loc, b.goKey(dlg, true), "go", nil)},
			[]Button{b.sesBtn(loc, "bot.ses.msg_edit_btn", "msg", nil)},
			back, b.sesHomeRow(loc))
	}
	b.sesReply(ctx, chatID, msgID, dlg, text, rows)
}

// sesShowType asks for the cancel word. It names what is about to be
// cancelled and how many buyers are written to, and says plainly that nothing
// happens until the word is sent.
func (b *Bot) sesShowType(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	subject := ""
	switch dlg.Mode {
	case sesModeCancelAll:
		subject = b.sesT(loc, "bot.ses.type_subject_all", map[string]any{"Name": Esc(dlg.EventName), "N": b.activeCount(dlg)})
	default:
		if dlg.Impact != nil {
			subject = b.sesT(loc, "bot.ses.type_subject_one", map[string]any{"Old": Esc(dlg.Impact.Old)})
		}
	}
	orders := 0
	if dlg.Impact != nil {
		orders = dlg.Impact.Orders
	}
	b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.type_ask", map[string]any{
		"Subject": subject, "Orders": orders, "Word": b.sesCancelWord(loc),
	}), [][]Button{b.sesNav(loc, "confirm"), b.sesHomeRow(loc)})
}

// sesCancelWord is the word the organizer types to cancel, in their language.
func (b *Bot) sesCancelWord(loc string) string {
	return b.sesT(loc, "bot.ses.cancel_word", nil)
}

// sesIsCancelWord reports whether the typed text is the explicit command: the
// word of the organizer's language, or the English one in any language.
func (b *Bot) sesIsCancelWord(loc, text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "\"'«».!"))
	return strings.EqualFold(text, b.sesCancelWord(loc)) || strings.EqualFold(text, "cancel")
}

func (b *Bot) goKey(dlg *sessionDialog, notify bool) string {
	switch dlg.Mode {
	case sesModeMove:
		if notify {
			return "bot.ses.go_move_btn"
		}
		return "bot.ses.go_move_free_btn"
	case sesModeCancelAll:
		if notify {
			return "bot.ses.go_cancel_all_btn"
		}
		return "bot.ses.go_cancel_all_free_btn"
	default:
		if notify {
			return "bot.ses.go_cancel_btn"
		}
		return "bot.ses.go_cancel_free_btn"
	}
}

func (b *Bot) activeCount(dlg *sessionDialog) int {
	n := 0
	for _, it := range dlg.Items {
		if !it.cancelled() {
			n++
		}
	}
	return n
}

// ─── the dry run ──────────────────────────────────────────────────────────────

// sesComputeImpact asks arena-api what the pending move/cancel would do and
// merges the answers (a cancel of the whole event asks once per session).
func (b *Bot) sesComputeImpact(ctx context.Context, jwt, loc string, dlg *sessionDialog) (*sesImpact, error) {
	out := &sesImpact{Kinds: []string{}}
	ask := func(it sesItem, start *time.Time, cancel bool) (openapi.SessionChangeImpact, error) {
		return b.arena.ChangeImpact(ctx, jwt, dlg.OrgID, it.ID, start, cancel, loc)
	}
	merge := func(r openapi.SessionChangeImpact) {
		out.Orders += r.Orders
		out.Tickets += r.Tickets
		out.NoAddress += r.NoAddress
		out.SiteOrders += r.SiteOrders
		out.Contact = r.Contact
		if out.Default == "" {
			out.Default = r.DefaultMessage
		}
		if out.Blocked == "" || r.Blocked == sessionchange.BlockedSiteRoute {
			if r.Blocked != "" {
				out.Blocked = r.Blocked
			}
		}
		if len(r.Kinds) > 0 {
			out.Kinds = r.Kinds
		}
	}
	switch dlg.Mode {
	case sesModeMove:
		it := dlg.Items[dlg.Cur]
		start, err := sesStart(dlg.Date, dlg.Time, it.Tz)
		if err != nil {
			return nil, err
		}
		r, err := ask(it, &start, false)
		if err != nil {
			return nil, err
		}
		merge(r)
		out.Old, out.New = FormatWhen(it.Start, it.Tz), FormatWhen(start, it.Tz)
	case sesModeCancel:
		it := dlg.Items[dlg.Cur]
		r, err := ask(it, nil, true)
		if err != nil {
			return nil, err
		}
		merge(r)
		out.Old = FormatWhen(it.Start, it.Tz)
	case sesModeCancelAll:
		for _, it := range dlg.Items {
			if it.cancelled() {
				continue
			}
			r, err := ask(it, nil, true)
			if err != nil {
				return nil, err
			}
			merge(r)
		}
	}
	return out, nil
}

// sesToConfirm runs the dry run and moves the dialog to the right screen: the
// organizer-contact questions when buyers have nobody to write to, otherwise
// the confirmation.
func (b *Bot) sesToConfirm(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, note string) {
	loc := id.Locale()
	imp, err := b.sesComputeImpact(ctx, jwt, loc, dlg)
	if err != nil {
		b.sesFail(ctx, chatID, msgID, id, dlg, err)
		return
	}
	dlg.Impact = imp
	if !dlg.MessageSet {
		dlg.Message = imp.Default
	}
	dlg.Step = sesStepConfirm
	if imp.Blocked == sessionchange.BlockedContactMissing {
		dlg.Step = sesStepEmail
	}
	b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, note)
}

// sesFail answers an API failure without losing the dialog.
func (b *Bot) sesFail(ctx context.Context, chatID int64, msgID *int, id *Identity, dlg *sessionDialog, err error) {
	loc := id.Locale()
	if IsAPIError(err, http.StatusForbidden) {
		b.sesReply(ctx, chatID, msgID, dlg, b.sesT(loc, "bot.wz.err_forbidden", nil), [][]Button{b.sesHomeRow(loc)})
		return
	}
	b.logger.Warn("eventbot: sessions dialog call failed", slog.String("error", err.Error()))
	b.sesReply(ctx, chatID, msgID, dlg, b.sesT(loc, "bot.ses.failed", nil), [][]Button{
		b.sesNav(loc, "card"), b.sesHomeRow(loc),
	})
}

// ─── callbacks ────────────────────────────────────────────────────────────────

// sessionsCallback handles every "ses:<data>" button.
func (b *Bot) sessionsCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	if strings.HasPrefix(data, "list:") {
		if eventID, err := uuid.Parse(strings.TrimPrefix(data, "list:")); err == nil {
			b.sessionsOpen(ctx, chatID, &msgID, from, eventID)
		}
		return
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, &msgID, id)
		return
	}
	loc := id.Locale()
	dlg, ok := b.sessions.get(from.ID)
	if !ok || dlg.OrgID != id.Current.OrgID {
		b.sessions.takeLapsed(from.ID)
		b.reply(ctx, chatID, &msgID, b.sesT(loc, "bot.ses.expired", nil), b.backKeyboard(loc, "events:1"))
		return
	}
	dlg.MsgID = msgID

	// The calendar's own callbacks.
	if dlg.Step == sesStepDate && (data == calNoop || strings.HasPrefix(data, calCallback) ||
		strings.HasPrefix(data, pickCallback) || strings.HasPrefix(data, confirmPrefix)) {
		b.sesDateInput(ctx, chatID, &msgID, id, jwt, from, dlg, "", data)
		return
	}

	switch {
	case data == "event":
		b.sessions.clear(from.ID)
		b.showEvent(ctx, chatID, &msgID, from, dlg.EventID, 1)
	case data == "home":
		b.sessions.clear(from.ID)
		b.showHome(ctx, chatID, &msgID, from, "")
	case data == "card":
		dlg.Step, dlg.Mode, dlg.Impact = sesStepCard, "", nil
		dlg.MessageSet = false
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case strings.HasPrefix(data, "o:"):
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(data, "o:"), "%d", &n); err != nil || n < 0 || n >= len(dlg.Items) {
			return
		}
		dlg.Cur, dlg.Step, dlg.Mode, dlg.Impact, dlg.MessageSet = n, sesStepCard, "", nil, false
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "or":
		if dlg.Cur < 0 || dlg.Cur >= len(dlg.Items) || !canViewSales(id) {
			return
		}
		it := dlg.Items[dlg.Cur]
		session, event := it.ID, dlg.EventID
		b.showOrdersFresh(ctx, chatID, &msgID, from, ordersScope{
			SessionID: &session, BackEvent: &event, Name: dlg.EventName + " · " + FormatWhen(it.Start, it.Tz),
		})
	case data == "ct":
		if dlg.Cur < 0 || dlg.Cur >= len(dlg.Items) || !canCategories(id) || dlg.Items[dlg.Cur].cancelled() {
			return
		}
		session, event, name := dlg.Items[dlg.Cur].ID, dlg.EventID, dlg.EventName
		b.sessions.clear(from.ID)
		b.categoriesOpen(ctx, chatID, &msgID, id, jwt, event, session, name)
	case data == "sm" || data == "csv":
		if dlg.Cur < 0 || dlg.Cur >= len(dlg.Items) || !canViewSales(id) {
			return
		}
		if data == "csv" {
			b.sendCSV(ctx, chatID, from, csvSessionSales, dlg.Items[dlg.Cur].ID)
			return
		}
		b.showSessionSummary(ctx, chatID, &msgID, from, dlg.Items[dlg.Cur].ID, "ses:card")
	case data == "mv":
		if !b.sesCurOpen(dlg) {
			return
		}
		it := dlg.Items[dlg.Cur]
		dlg.Mode, dlg.Step, dlg.Impact, dlg.MessageSet = sesModeMove, sesStepDate, nil, false
		dlg.Cal = &Draft{Version: draftSchemaVersion, Step: stSDate, Sessions: []DraftSession{{Timezone: it.Tz}}}
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "se" || data == "dr":
		if !b.sesCurOpen(dlg) {
			return
		}
		dlg.Mode, dlg.Impact, dlg.Step = "", nil, sesStepSalesEnd
		if data == "dr" {
			dlg.Step = sesStepDoors
		}
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case (strings.HasPrefix(data, "se:") && dlg.Step == sesStepSalesEnd) || (strings.HasPrefix(data, "dr:") && dlg.Step == sesStepDoors):
		b.sesSaleTimesInput(ctx, chatID, &msgID, id, jwt, from, dlg, "", data)
	case data == "cx":
		if !b.sesCurOpen(dlg) {
			return
		}
		dlg.Mode, dlg.MessageSet = sesModeCancel, false
		b.sesToConfirm(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "cxall":
		dlg.Mode, dlg.MessageSet = sesModeCancelAll, false
		b.sesToConfirm(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "confirm":
		dlg.Step = sesStepConfirm
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case strings.HasPrefix(data, "t:") && dlg.Step == sesStepTime:
		b.sesTimeInput(ctx, chatID, &msgID, id, jwt, from, dlg, strings.TrimPrefix(data, "t:"))
	case data == "msg":
		dlg.Step = sesStepMsg
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "nomsg":
		dlg.Message, dlg.MessageSet, dlg.Step = "", true, sesStepConfirm
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "defmsg":
		dlg.Message, dlg.MessageSet, dlg.Step = "", false, sesStepConfirm
		if dlg.Impact != nil {
			dlg.Message = dlg.Impact.Default
		}
		b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
	case data == "nophone":
		dlg.Phone = ""
		b.sesSaveContact(ctx, chatID, &msgID, id, jwt, from, dlg, false)
	case strings.HasPrefix(data, "hide:") && dlg.Step == sesStepHide:
		b.sesSaveContact(ctx, chatID, &msgID, id, jwt, from, dlg, data == "hide:1")
	case data == "go":
		// A cancellation is never one button: the press only opens the step
		// that asks for the cancel word (sessionsText carries it out).
		if dlg.Mode != sesModeMove && dlg.Step == sesStepConfirm && dlg.Impact != nil {
			dlg.Step = sesStepType
			b.sesShow(ctx, chatID, &msgID, id, jwt, from, dlg, "")
			return
		}
		b.sesGo(ctx, chatID, &msgID, id, jwt, from, dlg)
	}
	_ = loc
}

func (b *Bot) sesCurOpen(dlg *sessionDialog) bool {
	return dlg.Cur >= 0 && dlg.Cur < len(dlg.Items) && !dlg.Items[dlg.Cur].cancelled()
}

// sesDateInput feeds a typed date or a calendar press to the wizard's date
// machinery and moves on once a date is settled.
func (b *Bot) sesDateInput(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, text, data string) {
	loc := id.Locale()
	out, handled, note := b.wizard.dateInput(loc, dlg.Cal, text, data)
	if handled {
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, note)
		return
	}
	iso, ok := ParseDate(out)
	if !ok {
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.wz.err_date", nil))
		return
	}
	dlg.Date, dlg.Step = iso, sesStepTime
	b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
}

func (b *Bot) sesTimeInput(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, raw string) {
	loc := id.Locale()
	hhmm, ok := ParseTime(raw)
	if !ok {
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.wz.err_time", nil))
		return
	}
	dlg.Time = hhmm
	b.sesToConfirm(ctx, chatID, msgID, id, jwt, from, dlg, "")
}

// sesSaveContact writes the contact the organizer typed and returns to the
// dry run.
func (b *Bot) sesSaveContact(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, hidePhone bool) {
	loc := id.Locale()
	if _, err := b.arena.SetEventContact(ctx, jwt, dlg.OrgID, dlg.EventID, dlg.Email, dlg.Phone, hidePhone); err != nil {
		if APIErrorCode(err) == "contact.invalid_email" {
			dlg.Step = sesStepEmail
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.contact_bad_email", nil))
			return
		}
		b.sesFail(ctx, chatID, msgID, id, dlg, err)
		return
	}
	dlg.Email, dlg.Phone = "", ""
	b.sesToConfirm(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.contact_saved", nil))
}

// sesGo applies the move or the cancellation.
func (b *Bot) sesGo(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog) {
	loc := id.Locale()
	if dlg.Impact == nil || dlg.Step != sesStepConfirm {
		return
	}
	message := dlg.Message
	letters := 0
	var done int
	var err error
	switch dlg.Mode {
	case sesModeMove:
		it := dlg.Items[dlg.Cur]
		var start time.Time
		start, err = sesStart(dlg.Date, dlg.Time, it.Tz)
		if err == nil {
			var env openapi.SessionEnvelope
			env, err = b.arena.MoveSession(ctx, jwt, dlg.OrgID, dlg.EventID, it.ID, start, message)
			if err == nil {
				done = 1
				if env.Change != nil {
					letters = env.Change.Queued
				}
			}
		}
	case sesModeCancel:
		var env openapi.SessionEnvelope
		env, err = b.arena.CancelSession(ctx, jwt, dlg.OrgID, dlg.EventID, dlg.Items[dlg.Cur].ID, message)
		if err == nil {
			done = 1
			if env.Change != nil {
				letters = env.Change.Queued
			}
		}
	case sesModeCancelAll:
		for _, it := range dlg.Items {
			if it.cancelled() {
				continue
			}
			var env openapi.SessionEnvelope
			env, err = b.arena.CancelSession(ctx, jwt, dlg.OrgID, dlg.EventID, it.ID, message)
			if err != nil {
				break
			}
			done++
			if env.Change != nil {
				letters += env.Change.Queued
			}
		}
	}
	if err != nil {
		b.sesGoFailed(ctx, chatID, msgID, id, jwt, from, dlg, err, done)
		return
	}

	lettersText := ""
	if letters > 0 {
		lettersText = " " + b.sesT(loc, "bot.ses.letters", map[string]any{"N": letters})
	}
	switch dlg.Mode {
	case sesModeMove:
		dlg.Result = b.sesT(loc, "bot.ses.done_move", map[string]any{"New": Esc(dlg.Impact.New), "Letters": lettersText})
	case sesModeCancel:
		dlg.Result = b.sesT(loc, "bot.ses.done_cancel", map[string]any{"Letters": lettersText})
	default:
		dlg.Result = b.sesT(loc, "bot.ses.done_cancel_all", map[string]any{"N": done, "Letters": lettersText})
	}
	// A cancellation emails the buyers and moves no money: say so again once it
	// is done, so the organizer does not assume the refunds went out.
	if dlg.Mode != sesModeMove && letters > 0 {
		dlg.Result += "\n\n" + b.sesT(loc, "bot.ses.done_refund_note", nil)
	}
	// Refresh the list so the next screen shows the new date or the cancellation.
	if items, lerr := b.loadSesItems(ctx, jwt, dlg.OrgID, dlg.EventID); lerr == nil {
		dlg.Items = items
	}
	dlg.Step, dlg.Mode, dlg.Impact, dlg.MessageSet, dlg.Cur = sesStepDone, "", nil, false, -1
	b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
}

// sesGoFailed explains a refused write. Everything the server can refuse
// leaves the session as it was, so the person is sent back to the right
// question rather than to an error page.
func (b *Bot) sesGoFailed(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, err error, done int) {
	loc := id.Locale()
	switch APIErrorCode(err) {
	case "organization.contact_missing":
		dlg.Step = sesStepEmail
		if dlg.Impact == nil {
			dlg.Impact = &sesImpact{}
		}
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
		return
	case "session.change_site_unsupported":
		b.sesReply(ctx, chatID, msgID, dlg, b.sesT(loc, "bot.ses.site_blocked", map[string]any{"N": 1}), [][]Button{b.sesNav(loc, "card"), b.sesHomeRow(loc)})
		return
	case "session.change_message_too_long":
		dlg.Step = sesStepMsg
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.msg_too_long", map[string]any{"N": utf8.RuneCountInString(dlg.Message)}))
		return
	}
	if done > 0 {
		// Part of a cancel-all went through before the failure.
		dlg.Result = b.sesT(loc, "bot.ses.partial", map[string]any{"N": done})
		dlg.Step = sesStepDone
		if items, lerr := b.loadSesItems(ctx, jwt, dlg.OrgID, dlg.EventID); lerr == nil {
			dlg.Items = items
		}
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
		return
	}
	b.sesFail(ctx, chatID, msgID, id, dlg, err)
}

// ─── typed text ───────────────────────────────────────────────────────────────

// sessionsText consumes a typed answer of the dialog: a date, a time, the
// message to buyers, the organizer's e-mail or phone. It reports whether the
// text was taken.
func (b *Bot) sessionsText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	dlg, ok := b.sessions.get(from.ID)
	if !ok {
		return false
	}
	switch dlg.Step {
	case sesStepDate, sesStepTime, sesStepMsg, sesStepEmail, sesStepPhone, sesStepType, sesStepSalesEnd, sesStepDoors:
	default:
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != dlg.OrgID {
		b.sessions.clear(from.ID)
		return false
	}
	loc := id.Locale()
	var msgID *int
	if dlg.MsgID != 0 {
		m := dlg.MsgID
		msgID = &m
	}
	text = strings.TrimSpace(text)
	switch dlg.Step {
	case sesStepType:
		if !b.sesIsCancelWord(loc, text) {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.type_wrong", map[string]any{"Word": b.sesCancelWord(loc)}))
			return true
		}
		dlg.Step = sesStepConfirm
		b.sesGo(ctx, chatID, msgID, id, jwt, from, dlg)
	case sesStepDate:
		b.sesDateInput(ctx, chatID, msgID, id, jwt, from, dlg, text, "")
	case sesStepTime:
		b.sesTimeInput(ctx, chatID, msgID, id, jwt, from, dlg, text)
	case sesStepSalesEnd, sesStepDoors:
		b.sesSaleTimesInput(ctx, chatID, msgID, id, jwt, from, dlg, text, "")
	case sesStepMsg:
		msg, err := sessionchange.NormalizeMessage(text)
		if err != nil {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.msg_too_long", map[string]any{"N": utf8.RuneCountInString(text)}))
			return true
		}
		dlg.Message, dlg.MessageSet, dlg.Step = msg, true, sesStepConfirm
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
	case sesStepEmail:
		email := strings.ToLower(text)
		if !looksLikeEmail(email) {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.contact_bad_email", nil))
			return true
		}
		dlg.Email, dlg.Step = email, sesStepPhone
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
	case sesStepPhone:
		dlg.Phone, dlg.Step = text, sesStepHide
		b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, "")
	}
	return true
}
