package eventbot

// scanner_passes.go — the controller passes of the bot (spec 69, MA-12). On the
// event card, Sales and orders has "Controllers": the passes of the event, one
// card per pass, and a new pass in four steps (name, dates, phones, then the
// link and its QR picture to forward). The rules (dates and categories of the
// organization, the window, the phone limit, what a closed pass still does)
// are arena-api's: the bot reads the answers and says the refusals in words.
//
// The wizard keeps ONE bot_dialogs row of kind "scanner_pass". Presses are
// "sp:<kind>:<arg>". A press that names a pass or a phone carries that one
// UUID and works after the dialog ran out:
//
//	sp:l:<event>      the passes of the event      sp:n:<event>  a new pass
//	sp:x:<event>      cancel the wizard            sp:t:<i>      a date on or off
//	sp:nx             dates done, ask for phones   sp:d:<n>      phones, makes the pass
//	sp:v:<pass>       the card                     sp:off:<pass> turn off
//	sp:ln:<pass>      a new link                   sp:ex:<pass>:<hours>  extend the window
//	sp:dv:<pass>      the phones of the pass       sp:rv:<device> remove a phone
//	sp:me             "Control app" screen         sp:pc  link and QR for my phone
//	sp:md             my devices (scanner_app_devices.go)

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

const scannerPassDialogKind = "scanner_pass"

// The wizard's steps.
const (
	spStepLabel   = "label"   // waits for the typed name
	spStepDates   = "dates"   // the dates are on screen
	spStepDevices = "devices" // the phone count is on screen
)

const (
	spMaxLabelRunes = 80 // scanner_passes.label, as the API
	spMaxDates      = 20 // dates offered as switches, soonest first
	spListMax       = 10 // passes shown in the list
)

// spDeviceChoices are the phone-count buttons.
var spDeviceChoices = []int{1, 2, 3, 5, 10}

// spExtendHours are the "extend" buttons.
var spExtendHours = []int{2, 6}

var scannerPassKeys = []string{
	"bot.scan.ctrl_btn", "bot.scan.app_btn",
	"bot.scan.list_head", "bot.scan.list_empty", "bot.scan.list_more", "bot.scan.new_btn", "bot.scan.list_btn",
	"bot.scan.cancel_btn", "bot.scan.next_btn",
	"bot.scan.label_q", "bot.scan.err_label_empty", "bot.scan.err_label_long",
	"bot.scan.dates_q", "bot.scan.dates_none", "bot.scan.err_dates_none", "bot.scan.devices_q",
	"bot.scan.st_on", "bot.scan.st_off", "bot.scan.st_closed",
	"bot.scan.line", "bot.scan.line_window", "bot.scan.line_phones", "bot.scan.line_dates",
	"bot.scan.note_off", "bot.scan.note_closed",
	"bot.scan.off_btn", "bot.scan.link_btn", "bot.scan.ext_btn", "bot.scan.devs_btn", "bot.scan.back_card_btn",
	"bot.scan.created", "bot.scan.link_text", "bot.scan.link_done", "bot.scan.off_done", "bot.scan.ext_done", "bot.scan.ext_none",
	"bot.scan.devs_head", "bot.scan.devs_none", "bot.scan.dev_line", "bot.scan.dev_never", "bot.scan.dev_unnamed",
	"bot.scan.dev_remove_btn", "bot.scan.dev_removed",
	"bot.scan.err_revoked", "bot.scan.err_window_ended", "bot.scan.err_window", "bot.scan.err_session", "bot.scan.err_membership",
	"bot.scan.gone", "bot.scan.dev_gone",
	"bot.scan.app_text", "bot.scan.app_pair_btn", "bot.scan.app_devs_btn", "bot.scan.app_code_text", "bot.scan.app_code_sent",
	"bot.scan.mydev_head", "bot.scan.mydev_none", "bot.scan.mydev_removed",
}

func init() { MessageKeys = append(MessageKeys, scannerPassKeys...) }

// canScannerPasses reports whether the person may manage controller passes and
// pair a phone (scanner_pass.manage and scan.perform): the owner, the manager
// of the organization. A platform superadmin without such a membership sees
// no button. The API still decides every call.
func canScannerPasses(id *Identity) bool {
	if id == nil || id.Current == nil {
		return false
	}
	role := id.Current.Role
	if id.Superadmin { // the superadmin's list is synthetic owners: look at the real membership
		role = id.OwnRoles[id.Current.OrgID]
	}
	return role == membershipRoleOwner || role == membershipRoleManager
}

// spSess is one date on offer; On is the switch.
type spSess struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
	On    bool      `json:"on"`
}

// scannerPassDialog is the wizard's state.
type scannerPassDialog struct {
	OrgID     uuid.UUID `json:"org_id"`
	MsgID     int       `json:"msg_id,omitempty"`
	EventID   uuid.UUID `json:"event_id"`
	EventName string    `json:"event_name,omitempty"`
	Label     string    `json:"label,omitempty"`
	Sessions  []spSess  `json:"sessions,omitempty"`
}

func (b *Bot) loadScannerPassState(ctx context.Context, tg int64, orgID uuid.UUID) (st scannerPassDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, scannerPassDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: scanner pass dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return scannerPassDialog{OrgID: orgID}, "", false, false
	}
	if !found || st.OrgID != orgID {
		return scannerPassDialog{OrgID: orgID}, "", false, expired
	}
	return st, step, true, false
}

func (b *Bot) saveScannerPassState(ctx context.Context, tg int64, st scannerPassDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, scannerPassDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: scanner pass dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

func (b *Bot) leaveScannerPass(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, scannerPassDialogKind); err != nil {
		b.logger.Warn("eventbot: scanner pass dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// spRows helps to write keyboards: one button per row.
func spBtn(text, data string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: text, CallbackData: data}}
}

func (b *Bot) spHomeRow(loc string) []models.InlineKeyboardButton {
	return spBtn(b.texts.T(loc, "bot.btn_home", nil), "home")
}

// ─── the press router ─────────────────────────────────────────────────────────

// scannerPassCallback handles every "sp:<data>" press.
func (b *Bot) scannerPassCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canScannerPasses(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{b.spHomeRow(loc)}})
		return
	}
	kind, arg, _ := strings.Cut(data, ":")
	switch kind {
	case "t", "nx", "d":
		b.spWizardPress(ctx, chatID, msgID, from, id, jwt, kind, arg)
		return
	}
	// Every other press ends a question that waits for typed text.
	b.leaveScannerPass(ctx, from.ID)
	switch kind {
	case "l", "n", "x":
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		if kind == "n" {
			b.spStart(ctx, chatID, &msgID, id, jwt, eventID)
			return
		}
		b.spList(ctx, chatID, &msgID, id, jwt, eventID, "")
	case "v", "off", "ln", "dv":
		passID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.spPassPress(ctx, chatID, &msgID, id, jwt, kind, passID, 0)
	case "ex":
		pass, hours, _ := strings.Cut(arg, ":")
		passID, err := uuid.Parse(pass)
		h, herr := strconv.Atoi(hours)
		if err != nil || herr != nil || (h != 2 && h != 6) {
			return
		}
		b.spPassPress(ctx, chatID, &msgID, id, jwt, kind, passID, h)
	case "rv":
		if deviceID, err := uuid.Parse(arg); err == nil {
			b.spRemoveDevice(ctx, chatID, &msgID, id, jwt, deviceID)
		}
	case "me":
		b.spShowApp(ctx, chatID, &msgID, id, "")
	case "pc":
		b.spPairMyPhone(ctx, chatID, &msgID, id, jwt)
	case "md":
		b.spShowMyDevices(ctx, chatID, &msgID, id, jwt, "")
	}
}

// ─── the list ─────────────────────────────────────────────────────────────────

// spStatus is "on", "off" (turned off by a person) or "closed" (the window is over).
func spStatus(p ScannerPass, now time.Time) string {
	switch {
	case p.RevokedAt != nil:
		return "off"
	case p.State != nil && *p.State == "closed":
		return "closed"
	case p.ValidUntil != nil && !now.Before(*p.ValidUntil):
		return "closed"
	}
	return "on"
}

// spTimezone is the zone of the venue of the pass's first date, "" when it
// cannot be read (the times are then shown in UTC and say so).
func (b *Bot) spTimezone(ctx context.Context, jwt string, id *Identity, p ScannerPass) string {
	if len(p.Sessions) == 0 {
		return ""
	}
	sum, err := b.arena.SessionSummary(ctx, jwt, id.Current.OrgID, p.Sessions[0].SessionId)
	if err != nil || sum.Session.VenueTimezone == nil {
		return ""
	}
	return *sum.Session.VenueTimezone
}

// spWindow is the pass's window as "from to until" in the venue's zone, or ""
// for a pass without one.
func (b *Bot) spWindow(loc string, p ScannerPass, tz string) string {
	if p.ValidFrom == nil || p.ValidUntil == nil {
		return ""
	}
	return b.texts.T(loc, "bot.scan.line_window", map[string]any{"When": FormatWhen(*p.ValidFrom, tz), "Until": FormatWhen(*p.ValidUntil, tz)})
}

func (b *Bot) spStateText(loc, status string) string {
	return b.texts.T(loc, "bot.scan.st_"+status, nil)
}

// spLine is one pass in the list and the head of its card.
func (b *Bot) spLine(loc string, p ScannerPass, tz string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.scan.line", map[string]any{"Label": Esc(p.Label), "State": b.spStateText(loc, spStatus(p, now))}))
	if w := b.spWindow(loc, p, tz); w != "" {
		sb.WriteString("\n" + w)
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.scan.line_phones", map[string]any{"N": p.Accepted, "Total": p.MaxDevices}))
	return sb.String()
}

// spList draws the passes of one event.
func (b *Bot) spList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	event, err := b.cardEvent(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	passes, err := b.arena.ListScannerPasses(ctx, jwt, orgID, &eventID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "ec:o:"+eventID.String())
		return
	}
	now := time.Now()
	text := note + b.texts.T(loc, "bot.scan.list_head", map[string]any{"Name": Esc(event.Name)})
	var rows [][]models.InlineKeyboardButton
	if len(passes) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.scan.list_empty", nil)
	}
	shown := passes
	if len(shown) > spListMax {
		shown = shown[:spListMax]
	}
	tz := ""
	if len(shown) > 0 {
		tz = b.spTimezone(ctx, jwt, id, shown[0])
	}
	for _, p := range shown {
		text += "\n\n" + b.spLine(loc, p, tz, now)
		rows = append(rows, spBtn(truncate(p.Label, 40)+" · "+b.spStateText(loc, spStatus(p, now)), "sp:v:"+p.Id.String()))
	}
	if len(passes) > len(shown) {
		text += "\n\n" + b.texts.T(loc, "bot.scan.list_more", map[string]any{"Count": len(passes) - len(shown)})
	}
	rows = append(rows, spBtn(b.texts.T(loc, "bot.scan.new_btn", nil), "sp:n:"+eventID.String()))
	rows = append(rows, b.cardBackRows(loc, eventID)...)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── the wizard ───────────────────────────────────────────────────────────────

// spStart opens the wizard: it reads the coming dates of the event and asks
// for the name.
func (b *Bot) spStart(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	event, err := b.cardEvent(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "ec:o:"+eventID.String())
		return
	}
	now := time.Now()
	var coming []spSess
	var first *uuid.UUID
	var whenOf []time.Time
	for _, s := range sessions {
		if string(s.Status) == "cancelled" || !s.EndAt.After(now) || len(coming) >= spMaxDates {
			continue
		}
		sid := s.Id
		if first == nil {
			first = &sid
		}
		coming = append(coming, spSess{ID: sid, On: true})
		whenOf = append(whenOf, s.StartAt)
	}
	if len(coming) == 0 {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.scan.dates_none", map[string]any{"Name": Esc(event.Name)}),
			&models.InlineKeyboardMarkup{InlineKeyboard: b.spBackToList(loc, eventID)})
		return
	}
	tz := ""
	if sum, err := b.arena.SessionSummary(ctx, jwt, orgID, *first); err == nil && sum.Session.VenueTimezone != nil {
		tz = *sum.Session.VenueTimezone
	}
	for i := range coming {
		coming[i].Label = FormatWhen(whenOf[i], tz)
	}
	st := scannerPassDialog{OrgID: orgID, EventID: eventID, EventName: event.Name, Sessions: coming}
	b.spAskLabel(ctx, chatID, editMsgID, id, st, "")
}

func (b *Bot) spBackToList(loc string, eventID uuid.UUID) [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "sp:l:"+eventID.String()),
		b.spHomeRow(loc),
	}
}

func (b *Bot) spCancelRow(loc string, eventID uuid.UUID) []models.InlineKeyboardButton {
	return spBtn(b.texts.T(loc, "bot.scan.cancel_btn", nil), "sp:x:"+eventID.String())
}

func (b *Bot) spAskLabel(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st scannerPassDialog, prefix string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveScannerPassState(ctx, id.Link.TelegramUserID, st, spStepLabel)
	b.reply(ctx, chatID, editMsgID, prefix+b.texts.T(loc, "bot.scan.label_q", map[string]any{"Name": Esc(st.EventName)}),
		&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{b.spCancelRow(loc, st.EventID)}})
}

// parseScannerPassLabel reads the typed name; the second result is the key of
// the reason when it is refused.
func parseScannerPassLabel(raw string) (string, string) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return "", "bot.scan.err_label_empty"
	}
	if utf8.RuneCountInString(s) > spMaxLabelRunes {
		return "", "bot.scan.err_label_long"
	}
	return s, ""
}

// scannerPassText takes the typed name and reports whether it was taken.
func (b *Bot) scannerPassText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st scannerPassDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, scannerPassDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: scanner pass dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		if step != spStepLabel {
			return false
		}
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{b.spHomeRow(loc)}})
		return true
	}
	if !found || step != spStepLabel {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canScannerPasses(id) {
		b.leaveScannerPass(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		m := st.MsgID
		edit = &m
	}
	label, errKey := parseScannerPassLabel(text)
	if errKey != "" {
		b.spAskLabel(ctx, chatID, edit, id, st, b.texts.T(loc, errKey, nil)+"\n\n")
		return true
	}
	st.Label = label
	b.spRenderDates(ctx, chatID, edit, id, st, "")
	return true
}

func (b *Bot) spRenderDates(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st scannerPassDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveScannerPassState(ctx, id.Link.TelegramUserID, st, spStepDates)
	on := 0
	var rows [][]models.InlineKeyboardButton
	for i, s := range st.Sessions {
		mark := "⬜ "
		if s.On {
			mark = "✅ "
			on++
		}
		rows = append(rows, spBtn(mark+s.Label, "sp:t:"+strconv.Itoa(i)))
	}
	rows = append(rows, spBtn(b.texts.T(loc, "bot.scan.next_btn", nil), "sp:nx"), b.spCancelRow(loc, st.EventID))
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.scan.dates_q", map[string]any{"Label": Esc(st.Label), "Count": on}), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// spWizardPress handles the presses that need the wizard's state.
func (b *Bot) spWizardPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	// One press of the wizard at a time per person: the second tap of a double
	// tap waits, then finds the dialog deleted by spCreate and says it ended.
	defer b.spLocks.lock(uuid.NewSHA1(uuid.NameSpaceOID, []byte("sp:"+strconv.FormatInt(from.ID, 10))))()
	st, step, found, _ := b.loadScannerPassState(ctx, from.ID, id.Current.OrgID)
	if !found || step == spStepLabel {
		// The wizard ran out (or was ended by another screen): say so, rather than
		// act on a press of rows that may be stale.
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{b.spHomeRow(loc)}})
		return
	}
	e := &msgID
	switch kind {
	case "t":
		if i, err := strconv.Atoi(arg); err == nil && i >= 0 && i < len(st.Sessions) {
			st.Sessions[i].On = !st.Sessions[i].On
		}
		b.spRenderDates(ctx, chatID, e, id, st, "")
	case "nx":
		for _, s := range st.Sessions {
			if s.On {
				st.MsgID = msgID
				b.saveScannerPassState(ctx, from.ID, st, spStepDevices)
				var row []models.InlineKeyboardButton
				for _, n := range spDeviceChoices {
					row = append(row, models.InlineKeyboardButton{Text: strconv.Itoa(n), CallbackData: "sp:d:" + strconv.Itoa(n)})
				}
				b.reply(ctx, chatID, e, b.texts.T(loc, "bot.scan.devices_q", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
					row,
					spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "sp:t:-1"),
					b.spCancelRow(loc, st.EventID),
				}})
				return
			}
		}
		b.spRenderDates(ctx, chatID, e, id, st, b.texts.T(loc, "bot.scan.err_dates_none", nil)+"\n\n")
	case "d":
		n, err := strconv.Atoi(arg)
		if err != nil || step != spStepDevices || !spValidDeviceChoice(n) {
			b.spRenderDates(ctx, chatID, e, id, st, "")
			return
		}
		b.spCreate(ctx, chatID, e, id, jwt, st, n)
	}
}

func spValidDeviceChoice(n int) bool {
	for _, c := range spDeviceChoices {
		if c == n {
			return true
		}
	}
	return false
}

// spCreate makes the pass, then answers with the link, the QR picture and the card.
func (b *Bot) spCreate(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st scannerPassDialog, devices int) {
	loc := id.Locale()
	var ids []uuid.UUID
	for _, s := range st.Sessions {
		if s.On {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		b.spRenderDates(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.scan.err_dates_none", nil)+"\n\n")
		return
	}
	created, err := b.arena.CreateScannerPass(ctx, jwt, id.Current.OrgID, ScannerPassCreate{Label: st.Label, SessionIDs: ids, MaxDevices: devices})
	if err != nil {
		b.leaveScannerPass(ctx, id.Link.TelegramUserID)
		b.spError(ctx, chatID, editMsgID, id, err, "sp:l:"+st.EventID.String())
		return
	}
	b.leaveScannerPass(ctx, id.Link.TelegramUserID)
	tz := b.spTimezone(ctx, jwt, id, created.Pass)
	linkText := b.texts.T(loc, "bot.scan.link_text", map[string]any{
		"Label": Esc(created.Pass.Label), "URL": Esc(created.Pairing.Link), "Code": Esc(created.Pairing.Code), "N": created.Pass.MaxDevices,
	})
	prefix := b.texts.T(loc, "bot.scan.created", map[string]any{"Label": Esc(created.Pass.Label)}) + "\n\n"
	if !b.spSendQR(ctx, chatID, created.Pairing.QrPng, "pass-"+created.Pairing.Code+".png", linkText) {
		prefix = linkText + "\n\n"
	}
	b.reply(ctx, chatID, editMsgID, prefix+b.spCardText(loc, created.Pass, tz), b.spCardKeyboard(loc, created.Pass))
}

// spSendQR sends the QR picture as a photo whose caption is the text to
// forward. It reports whether the photo went out; without one (no picture, a
// failed send) the caller shows the text itself.
func (b *Bot) spSendQR(ctx context.Context, chatID int64, qr *string, filename, caption string) bool {
	if qr == nil || *qr == "" {
		return false
	}
	png, err := base64.StdEncoding.DecodeString(*qr)
	if err != nil || len(png) == 0 {
		b.logger.Warn("eventbot: scanner pairing qr not decoded")
		return false
	}
	if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(png)},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		b.logger.Warn("eventbot: send scanner qr failed", slog.Int64("chat_id", chatID), slog.String("error", unwrapURLError(err).Error()))
		return false
	}
	return true
}

// ─── the card ─────────────────────────────────────────────────────────────────

func (b *Bot) spCardText(loc string, p ScannerPass, tz string) string {
	now := time.Now()
	status := spStatus(p, now)
	text := b.spLine(loc, p, tz, now)
	text += "\n" + b.texts.T(loc, "bot.scan.line_dates", map[string]any{"Count": len(p.Sessions)})
	switch status {
	case "off":
		text += "\n\n" + b.texts.T(loc, "bot.scan.note_off", nil)
	case "closed":
		text += "\n\n" + b.texts.T(loc, "bot.scan.note_closed", nil)
	}
	return text
}

// spPassEvent is the event the pass's first date belongs to: where the card goes back to.
func spPassEvent(p ScannerPass) (uuid.UUID, bool) {
	if len(p.Sessions) == 0 {
		return uuid.Nil, false
	}
	return p.Sessions[0].EventId, true
}

func (b *Bot) spCardKeyboard(loc string, p ScannerPass) *models.InlineKeyboardMarkup {
	pid := p.Id.String()
	status := spStatus(p, time.Now())
	var rows [][]models.InlineKeyboardButton
	var first []models.InlineKeyboardButton
	if status != "off" {
		first = append(first, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.scan.off_btn", nil), CallbackData: "sp:off:" + pid})
	}
	first = append(first, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.scan.link_btn", nil), CallbackData: "sp:ln:" + pid})
	rows = append(rows, first)
	if status != "off" && p.ValidUntil != nil {
		var ext []models.InlineKeyboardButton
		for _, h := range spExtendHours {
			ext = append(ext, models.InlineKeyboardButton{
				Text: b.texts.T(loc, "bot.scan.ext_btn", map[string]any{"Hours": h}), CallbackData: "sp:ex:" + pid + ":" + strconv.Itoa(h),
			})
		}
		rows = append(rows, ext)
	}
	rows = append(rows, spBtn(b.texts.T(loc, "bot.scan.devs_btn", map[string]any{"N": p.Accepted}), "sp:dv:"+pid))
	if ev, ok := spPassEvent(p); ok {
		rows = append(rows, spBtn("« "+b.texts.T(loc, "bot.scan.list_btn", nil), "sp:l:"+ev.String()))
	}
	rows = append(rows, b.spHomeRow(loc))
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// spShowCard reads the pass afresh and draws its card.
func (b *Bot) spShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, passID uuid.UUID, note string) {
	pass, err := b.arena.GetScannerPass(ctx, jwt, id.Current.OrgID, passID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	b.spReplyCard(ctx, chatID, editMsgID, id, jwt, pass, note)
}

func (b *Bot) spReplyCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, pass ScannerPass, note string) {
	loc := id.Locale()
	tz := b.spTimezone(ctx, jwt, id, pass)
	b.reply(ctx, chatID, editMsgID, note+b.spCardText(loc, pass, tz), b.spCardKeyboard(loc, pass))
}

// spPassPress handles the presses that name a pass.
func (b *Bot) spPassPress(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt, kind string, passID uuid.UUID, hours int) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	switch kind {
	case "v":
		b.spShowCard(ctx, chatID, editMsgID, id, jwt, passID, "")
	case "off":
		if err := b.arena.RevokeScannerPass(ctx, jwt, orgID, passID); err != nil {
			b.spError(ctx, chatID, editMsgID, id, err, "home")
			return
		}
		b.spShowCard(ctx, chatID, editMsgID, id, jwt, passID, b.texts.T(loc, "bot.scan.off_done", nil)+"\n\n")
	case "ln":
		link, err := b.arena.LinkScannerPass(ctx, jwt, orgID, passID)
		if err != nil {
			b.spError(ctx, chatID, editMsgID, id, err, "sp:v:"+passID.String())
			return
		}
		pass, err := b.arena.GetScannerPass(ctx, jwt, orgID, passID)
		if err != nil {
			b.spError(ctx, chatID, editMsgID, id, err, "home")
			return
		}
		linkText := b.texts.T(loc, "bot.scan.link_text", map[string]any{
			"Label": Esc(pass.Label), "URL": Esc(link.Link), "Code": Esc(link.Code), "N": pass.MaxDevices,
		})
		note := b.texts.T(loc, "bot.scan.link_done", nil) + "\n\n"
		if !b.spSendQR(ctx, chatID, link.QrPng, "pass-"+link.Code+".png", linkText) {
			note = linkText + "\n\n"
		}
		b.spReplyCard(ctx, chatID, editMsgID, id, jwt, pass, note)
	case "ex":
		pass, err := b.arena.GetScannerPass(ctx, jwt, orgID, passID)
		if err != nil {
			b.spError(ctx, chatID, editMsgID, id, err, "home")
			return
		}
		if pass.ValidUntil == nil {
			b.spReplyCard(ctx, chatID, editMsgID, id, jwt, pass, b.texts.T(loc, "bot.scan.ext_none", nil)+"\n\n")
			return
		}
		base := *pass.ValidUntil
		if now := time.Now(); base.Before(now) {
			base = now
		}
		updated, err := b.arena.ExtendScannerPass(ctx, jwt, orgID, passID, base.Add(time.Duration(hours)*time.Hour))
		if err != nil {
			b.spError(ctx, chatID, editMsgID, id, err, "sp:v:"+passID.String())
			return
		}
		tz := b.spTimezone(ctx, jwt, id, updated)
		until := ""
		if updated.ValidUntil != nil {
			until = FormatWhen(*updated.ValidUntil, tz)
		}
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.scan.ext_done", map[string]any{"Until": until})+"\n\n"+b.spCardText(loc, updated, tz), b.spCardKeyboard(loc, updated))
	case "dv":
		b.spShowPassDevices(ctx, chatID, editMsgID, id, jwt, passID, "")
	}
}

// ─── the phones of a pass ─────────────────────────────────────────────────────

// spDeviceName is what a person calls a phone: the controller's name, else the
// phone's own name, else its platform.
func (b *Bot) spDeviceName(loc, controller, label, platform string) string {
	for _, s := range []string{controller, label} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	if platform = strings.TrimSpace(platform); platform != "" {
		return platform
	}
	return b.texts.T(loc, "bot.scan.dev_unnamed", nil)
}

func (b *Bot) spLastSeen(loc string, t *time.Time, tz string) string {
	if t == nil {
		return b.texts.T(loc, "bot.scan.dev_never", nil)
	}
	return FormatWhen(*t, tz)
}

func (b *Bot) spShowPassDevices(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, passID uuid.UUID, note string) {
	loc := id.Locale()
	pass, err := b.arena.GetScannerPass(ctx, jwt, id.Current.OrgID, passID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	tz := b.spTimezone(ctx, jwt, id, pass)
	text := note + b.texts.T(loc, "bot.scan.devs_head", map[string]any{"Label": Esc(pass.Label), "N": len(pass.Devices), "Total": pass.MaxDevices})
	var rows [][]models.InlineKeyboardButton
	if len(pass.Devices) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.scan.devs_none", nil)
	}
	for _, d := range pass.Devices {
		name := b.spDeviceName(loc, d.ControllerName, d.Label, d.Platform)
		text += "\n" + b.texts.T(loc, "bot.scan.dev_line", map[string]any{"Name": Esc(name), "When": b.spLastSeen(loc, d.LastSeenAt, tz)})
		rows = append(rows, spBtn(b.texts.T(loc, "bot.scan.dev_remove_btn", map[string]any{"Name": truncate(name, 30)}), "sp:rv:"+d.Id.String()))
	}
	rows = append(rows, spBtn("« "+b.texts.T(loc, "bot.scan.back_card_btn", nil), "sp:v:"+passID.String()), b.spHomeRow(loc))
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// spRemoveDevice removes one phone and goes back to where the press came from:
// the pass's phones, or the person's own.
func (b *Bot) spRemoveDevice(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, deviceID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	devices, err := b.arena.ListScannerDevices(ctx, jwt, orgID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	var passID *uuid.UUID
	found := false
	for _, d := range devices {
		if d.Id == deviceID {
			found, passID = true, d.PassId
		}
	}
	if !found {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.scan.dev_gone", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{b.spHomeRow(loc)}})
		return
	}
	if err := b.arena.RevokeScannerDevice(ctx, jwt, orgID, deviceID); err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if passID != nil {
		b.spShowPassDevices(ctx, chatID, editMsgID, id, jwt, *passID, b.texts.T(loc, "bot.scan.dev_removed", nil)+"\n\n")
		return
	}
	b.spShowMyDevices(ctx, chatID, editMsgID, id, jwt, b.texts.T(loc, "bot.scan.mydev_removed", nil)+"\n\n")
}

// ─── refusals ─────────────────────────────────────────────────────────────────

// spError answers a failed call: the refusals the bot has words for first,
// the standard screens for the rest. back is the callback of the one button.
func (b *Bot) spError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error, back string) {
	loc := id.Locale()
	key := ""
	switch APIErrorCode(err) {
	case "scanner_pass.not_found":
		key = "bot.scan.gone"
	case "scanner_pass.revoked":
		key = "bot.scan.err_revoked"
	case "scanner_pass.window_ended":
		key = "bot.scan.err_window_ended"
	case "scanner_pass.invalid_window":
		key = "bot.scan.err_window"
	case "scanner_pass.invalid_session", "scanner_pass.invalid_tier":
		key = "bot.scan.err_session"
	case "scanner.membership_required":
		key = "bot.scan.err_membership"
	case "scanner.device_not_found":
		key = "bot.scan.dev_gone"
	}
	if key == "" {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	rows := [][]models.InlineKeyboardButton{b.spHomeRow(loc)}
	if back != "" && back != "home" {
		rows = [][]models.InlineKeyboardButton{spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), back), b.spHomeRow(loc)}
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, nil), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
