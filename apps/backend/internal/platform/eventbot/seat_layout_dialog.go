package eventbot

// seat_layout_dialog.go — the "Seat layout" screens of a seated date (task
// S3c of spec 40, spec 26 §4): the organizer cuts the hall into the date's
// categories by how good the seats are, without leaving the chat.
//
//  1. The date's card ("Seat layout") opens the start screen: the categories
//     that take part, the way of cutting (one button per algorithm of the
//     registry - the bot never names one) and, when the last change of the
//     seats can be taken back, its "Undo" button.
//  2. The options screen asks only what the algorithm needs: where the stage
//     is (when the algorithm requires it - it is never guessed, the person
//     picks the side of the plan as drawn) and up to two switches or choices
//     of the algorithm's own.
//  3. The preview shows the proposal and writes NOTHING: how many seats go to
//     each category, how many stay as they are because they are sold or held.
//  4. "Apply" asks for the proposal again and compares it with the one shown
//     (loFingerprint). Only the same proposal is applied; a hall that changed
//     meanwhile shows the new preview and waits for another press.
//  5. The result carries "Undo". A category change of unsold seats is
//     reversible (seat batches, S1c), so no typed word is asked.
//
// The state lives in ONE bot_dialogs row of kind "seatlayout". Presses are
// "sl:<kind>:<arg>":
//
//	sl:a:<algorithm>   choose a way       sl:sd:<side>   the stage side
//	sl:pm:<i>:<j>      option i = value j (a switch: j is 1 or 0)
//	sl:op              the options        sl:go          show the proposal
//	sl:ap:<fp>         apply the one shown
//	sl:rv:<batch>      undo a batch       sl:st          the start screen
//	sl:back            up to the date     sl:home        the main menu
//	sl:nc:<n>          n categories       sl:nq          ask the split again
//	sl:o:<session>     open the screen of a date (splitSeatsByPriceButton)
//
// A date with exactly ONE open seated category cannot be cut by itself, so
// after the way is chosen the person is asked (steps "cats", "top", "bottom":
// buttons for the number of categories, then two typed prices) and the slots
// of the proposal become NEW categories, created by the same batch.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const seatLayoutDialogKind = "seatlayout"

// The dialog's steps.
const (
	loStepStart   = "start"
	loStepOptions = "options"
	loStepPreview = "preview"
	loStepDone    = "done"
	// The questions of a date with one category.
	loStepCats   = "cats"
	loStepTop    = "top"
	loStepBottom = "bottom"
)

// seatLayoutDialog is the screens' state, stored as the bot_dialogs row's JSON.
type seatLayoutDialog struct {
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	SessionID uuid.UUID `json:"session_id"`
	VenueID   uuid.UUID `json:"venue_id"`
	VersionID uuid.UUID `json:"version_id"`
	EventName string    `json:"event_name"`
	When      string    `json:"when"`
	Tz        string    `json:"tz,omitempty"`
	MsgID     int       `json:"msg_id,omitempty"`
	// Algo is the chosen algorithm, Side the stage side, Choice the person's
	// answers to the algorithm's own options (a string or a bool each).
	Algo   string         `json:"algo,omitempty"`
	Side   string         `json:"side,omitempty"`
	Choice map[string]any `json:"choice,omitempty"`
	// Bounds is the box the plan's seats fill, read once for the stage.
	Bounds *PlanBounds `json:"bounds,omitempty"`
	// Fingerprint identifies the proposal on screen (loFingerprint).
	Fingerprint string `json:"fingerprint,omitempty"`
	// Batch is the batch just applied, for the result's "Undo".
	Batch string `json:"batch,omitempty"`
	// MadeTiers is the batch that created categories: its undo keeps them.
	MadeTiers string `json:"made_tiers,omitempty"`
	// Cur is the date's currency, Cats, Top and Bottom the answers of a date
	// with one category (the prices in minor units; 0 while unanswered).
	Cur    string `json:"cur,omitempty"`
	Cats   int    `json:"cats,omitempty"`
	Top    int64  `json:"top,omitempty"`
	Bottom int64  `json:"bottom,omitempty"`
}

// split is the answers of a date with one category.
func (st seatLayoutDialog) split() loNew { return loNew{Cats: st.Cats, Top: st.Top, Bottom: st.Bottom} }

// splitReady reports whether the three answers are given and consistent.
func (st seatLayoutDialog) splitReady() bool {
	return st.Cats >= loMinCats && st.Cats <= loMaxCats && st.Top > 0 && st.Bottom > 0 && st.Bottom < st.Top
}

// canSeatLayout reports whether the person may re-categorise seats: the owner,
// the manager and the platform operator hold session.seats.manage (the agent
// role does not). The API still decides every call.
func canSeatLayout(id *Identity) bool { return canViewSales(id) }

// isSeatedSession reports whether the session sells places from a plan.
func isSeatedSession(s openapi.SessionItem) bool {
	return s.SeatingPlanVersionId != nil && string(s.AdmissionMode) != "general_admission"
}

func (b *Bot) loadSeatLayoutState(ctx context.Context, tg int64, orgID uuid.UUID) (st seatLayoutDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, seatLayoutDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: seat layout dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return seatLayoutDialog{}, "", false, false
	}
	if found && st.OrgID != orgID {
		return seatLayoutDialog{}, "", false, false
	}
	return st, step, found, expired
}

func (b *Bot) saveSeatLayoutState(ctx context.Context, tg int64, st seatLayoutDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, seatLayoutDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: seat layout dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveSeatLayout ends the dialog.
func (b *Bot) leaveSeatLayout(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, seatLayoutDialogKind); err != nil {
		b.logger.Warn("eventbot: seat layout dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// loKeyboard turns wizard-style buttons into inline buttons whose callbacks
// carry the "sl:" prefix.
func loKeyboard(rows [][]Button) *models.InlineKeyboardMarkup {
	out := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, r := range rows {
		line := make([]models.InlineKeyboardButton, 0, len(r))
		for _, bt := range r {
			line = append(line, models.InlineKeyboardButton{Text: bt.Label, CallbackData: "sl:" + bt.Data})
		}
		out = append(out, line)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: out}
}

// loLabel is a label from the API's i18n key; the fallback stands in for a key
// no catalog has (an algorithm added after this bot was written).
func (b *Bot) loLabel(loc, key, fallback string) string {
	if key == "" {
		return fallback
	}
	if s := b.texts.T(loc, key, nil); s != key {
		return s
	}
	return fallback
}

func (b *Bot) loNavRows(loc, back string) [][]Button {
	return [][]Button{
		{{Label: "« " + b.texts.T(loc, "bot.btn_back", nil), Data: back}},
		{{Label: b.texts.T(loc, "bot.btn_home", nil), Data: "home"}},
	}
}

// ─── entry ────────────────────────────────────────────────────────────────────

// seatLayoutOpen starts the dialog on one date and shows the start screen. The
// date is looked up in the event's own list, so a session that is not the
// event's (or the organization's) reads as not found; a date without a plan
// has no seats to lay out.
func (b *Bot) seatLayoutOpen(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID, sessionID uuid.UUID, eventName string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	var found *openapi.SessionItem
	for i := range sessions {
		if sessions[i].Id == sessionID {
			found = &sessions[i]
		}
	}
	if found == nil || !isSeatedSession(*found) || string(found.Status) == "cancelled" {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.sl.not_seated", nil), loKeyboard(b.loNavRows(loc, "back")))
		return
	}
	st := seatLayoutDialog{
		OrgID: orgID, EventID: eventID, SessionID: sessionID, EventName: eventName,
		VenueID: found.VenueId, VersionID: *found.SeatingPlanVersionId,
	}
	if sum, err := b.arena.SessionSummary(ctx, jwt, orgID, sessionID); err == nil && sum.Session.VenueTimezone != nil {
		st.Tz = *sum.Session.VenueTimezone
	}
	st.When = FormatWhen(found.StartAt, st.Tz)
	b.seatLayoutShowStart(ctx, chatID, editMsgID, id, jwt, st, "")
}

// ─── the start screen ─────────────────────────────────────────────────────────

func (b *Bot) loHead(loc string, st seatLayoutDialog) string {
	return b.texts.T(loc, "bot.sl.head", map[string]any{"Name": Esc(st.EventName), "When": Esc(st.When)})
}

func (b *Bot) seatLayoutShowStart(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st seatLayoutDialog, note string) {
	loc := id.Locale()
	tiers, err := b.arena.ListTiers(ctx, jwt, st.OrgID, st.EventID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	targets, closed := loTargets(tiers)
	st.Algo, st.Side, st.Choice, st.Fingerprint, st.Batch = "", "", nil, "", ""
	st.Cats, st.Top, st.Bottom, st.Cur = 0, 0, 0, tierCurrency(tiers)
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}

	var sb strings.Builder
	sb.WriteString(note + b.loHead(loc, st))
	var rows [][]Button
	switch {
	case len(targets) < 1:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.need_one", nil))
	default:
		intro := "bot.sl.intro"
		if len(targets) == 1 {
			intro = "bot.sl.intro_one"
		}
		sb.WriteString("\n\n" + b.texts.T(loc, intro, nil))
		for _, t := range targets {
			sb.WriteString("\n" + b.texts.T(loc, "bot.sl.cat_line", map[string]any{
				"Name": Esc(t.Name), "Price": Esc(FormatMoney(t.Price, tierCurrency(tiers), loc)),
			}))
		}
		if len(targets) == 1 {
			// Empty open categories a split may fill instead of making new ones.
			for _, t := range loEmptyOpen(tiers) {
				sb.WriteString("\n" + b.texts.T(loc, "bot.sl.cat_line", map[string]any{
					"Name": Esc(t.Name), "Price": Esc(FormatMoney(t.Price, tierCurrency(tiers), loc)),
				}))
			}
		}
		if closed > 0 {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.closed_note", map[string]any{"Count": closed}))
		}
		algs, err := b.arena.SeatLayoutAlgorithms(ctx, jwt, st.OrgID, st.SessionID)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "home")
			return
		}
		for _, a := range algs.Algorithms {
			rows = append(rows, []Button{{Label: b.loLabel(loc, a.LabelKey, a.Id), Data: "a:" + a.Id}})
		}
		if len(algs.Algorithms) == 0 {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.no_algorithms", nil))
		}
	}
	// The newest batch, when it can still be taken back.
	if last, err := b.arena.LastSeatBatch(ctx, jwt, st.OrgID, st.SessionID); err == nil && last != nil &&
		last.RevertedAt == nil && last.Kind != "revert" && last.ChangedCount > 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.last_batch", map[string]any{
			"When": Esc(FormatWhen(last.AppliedAt, st.Tz)), "Count": last.ChangedCount,
		}))
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.undo_btn", nil), Data: "rv:" + last.Id.String()}})
	}
	rows = append(rows, b.loNavRows(loc, "back")...)
	b.saveSeatLayoutState(ctx, id.Link.TelegramUserID, st, loStepStart)
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), loKeyboard(rows))
}

// tierCurrency is the currency of the date's categories (one per date).
func tierCurrency(tiers []openapi.TicketTierItem) string {
	for _, t := range tiers {
		if t.Currency != "" {
			return t.Currency
		}
	}
	return ""
}

// ─── callbacks ────────────────────────────────────────────────────────────────

// seatLayoutCallback handles every "sl:<data>" press.
func (b *Bot) seatLayoutCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canSeatLayout(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "home" {
		b.leaveSeatLayout(ctx, from.ID)
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}
	if kind == "o" {
		b.loOpenByPress(ctx, chatID, &msgID, id, jwt, arg)
		return
	}
	st, step, found, expired := b.loadSeatLayoutState(ctx, from.ID, id.Current.OrgID)
	if !found {
		key := "bot.sl.gone"
		if expired {
			key = "bot.dialog_expired"
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, "events:1"))
		return
	}
	st.MsgID = msgID

	switch kind {
	case "back":
		b.leaveSeatLayout(ctx, from.ID)
		b.sessionsOpen(ctx, chatID, &msgID, from, st.EventID)
	case "st":
		b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
	case "a":
		b.loChooseAlgorithm(ctx, chatID, &msgID, id, jwt, st, arg)
	case "nq":
		if st.Algo == "" {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.loAskCats(ctx, chatID, &msgID, id, st, "")
	case "nc":
		n, err := strconv.Atoi(arg)
		if st.Algo == "" || err != nil || n < loMinCats || n > loMaxCats {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		st.Cats, st.Top, st.Bottom = n, 0, 0
		b.loAskTop(ctx, chatID, &msgID, id, st, "")
	case "sd":
		if step != loStepOptions && step != loStepPreview {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		if containsString(loStageSides, arg) {
			st.Side = arg
		}
		b.loShowOptions(ctx, chatID, &msgID, id, jwt, st, "")
	case "pm":
		if step != loStepOptions && step != loStepPreview {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.loSetOption(ctx, chatID, &msgID, id, jwt, st, arg)
	case "op":
		if st.Algo == "" {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.loShowOptions(ctx, chatID, &msgID, id, jwt, st, "")
	case "go":
		if st.Algo == "" {
			b.seatLayoutShowStart(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.loPreview(ctx, chatID, &msgID, id, jwt, st, "")
	case "ap":
		b.loApply(ctx, chatID, &msgID, id, jwt, st, step, arg)
	case "rv":
		b.loRevert(ctx, chatID, &msgID, id, jwt, st, arg)
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// loAlgorithm finds the chosen algorithm in a fresh list.
func (b *Bot) loAlgorithm(ctx context.Context, jwt string, st seatLayoutDialog) (openapi.SeatLayoutAlgorithm, bool, error) {
	algs, err := b.arena.SeatLayoutAlgorithms(ctx, jwt, st.OrgID, st.SessionID)
	if err != nil {
		return openapi.SeatLayoutAlgorithm{}, false, err
	}
	for _, a := range algs.Algorithms {
		if a.Id == st.Algo {
			return a, true, nil
		}
	}
	return openapi.SeatLayoutAlgorithm{}, false, nil
}

func (b *Bot) loChooseAlgorithm(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, algoID string) {
	st.Algo, st.Choice, st.Fingerprint = algoID, nil, ""
	alg, ok, err := b.loAlgorithm(ctx, jwt, st)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !ok {
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(id.Locale(), "bot.sl.algorithm_gone", nil)+"\n\n")
		return
	}
	st.Cats, st.Top, st.Bottom = 0, 0, 0
	tiers, err := b.arena.ListTiers(ctx, jwt, st.OrgID, st.EventID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	targets, _ := loTargets(tiers)
	switch len(targets) {
	case 0:
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, "")
		return
	case 1:
		// One category: the person says how to cut it into several.
		if !loSupportsNew(alg) {
			b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(id.Locale(), "bot.sl.err_method_one", nil)+"\n\n")
			return
		}
		st.Cur = tierCurrency(tiers)
		b.loAskCats(ctx, chatID, msgID, id, st, "")
		return
	}
	b.loAfterQuestions(ctx, chatID, msgID, id, jwt, st, alg)
}

// loAfterQuestions goes on from the way chosen (and, for one category, the
// split answered): the options when the algorithm asks something, else the
// proposal at once.
func (b *Bot) loAfterQuestions(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, alg openapi.SeatLayoutAlgorithm) {
	if !loNeedsStage(alg) && len(loAsked(alg)) == 0 {
		b.loPreview(ctx, chatID, msgID, id, jwt, st, "")
		return
	}
	b.loShowOptions(ctx, chatID, msgID, id, jwt, st, "")
}

// ─── the split of one category ────────────────────────────────────────────────

func (b *Bot) loSaveReply(ctx context.Context, chatID int64, msgID *int, id *Identity, st seatLayoutDialog, step, text string, rows [][]Button) {
	if msgID != nil {
		st.MsgID = *msgID
	}
	st.Fingerprint = ""
	b.saveSeatLayoutState(ctx, id.Link.TelegramUserID, st, step)
	b.reply(ctx, chatID, msgID, clipMessage(text, maxMessageRunes), loKeyboard(rows))
}

// loAskCats asks into how many categories the seats are cut (buttons).
func (b *Bot) loAskCats(ctx context.Context, chatID int64, msgID *int, id *Identity, st seatLayoutDialog, note string) {
	loc := id.Locale()
	st.Cats, st.Top, st.Bottom = 0, 0, 0
	var line []Button
	for n := loMinCats; n <= loMaxCats; n++ {
		line = append(line, Button{Label: strconv.Itoa(n), Data: "nc:" + strconv.Itoa(n)})
	}
	rows := [][]Button{line}
	rows = append(rows, b.loNavRows(loc, "st")...)
	text := note + b.loHead(loc, st) + "\n\n" + b.texts.T(loc, "bot.sl.q_cats", nil)
	b.loSaveReply(ctx, chatID, msgID, id, st, loStepCats, text, rows)
}

func (b *Bot) loQuestionNav(loc string) [][]Button {
	return [][]Button{
		{{Label: "« " + b.texts.T(loc, "bot.btn_back", nil), Data: "nq"}},
		{{Label: b.texts.T(loc, "bot.btn_home", nil), Data: "home"}},
	}
}

// loAskTop asks the price of the best seats (a typed answer).
func (b *Bot) loAskTop(ctx context.Context, chatID int64, msgID *int, id *Identity, st seatLayoutDialog, note string) {
	loc := id.Locale()
	text := note + b.loHead(loc, st) + "\n\n" + b.texts.T(loc, "bot.sl.q_top", map[string]any{
		"Cats": st.Cats, "Currency": Esc(strings.ToUpper(st.Cur)),
	})
	b.loSaveReply(ctx, chatID, msgID, id, st, loStepTop, text, b.loQuestionNav(loc))
}

// loAskBottom asks the price of the cheapest seats (a typed answer).
func (b *Bot) loAskBottom(ctx context.Context, chatID int64, msgID *int, id *Identity, st seatLayoutDialog, note string) {
	loc := id.Locale()
	text := note + b.loHead(loc, st) + "\n\n" + b.texts.T(loc, "bot.sl.q_bottom", map[string]any{
		"Top": Esc(FormatMoney(st.Top, st.Cur, loc)), "Currency": Esc(strings.ToUpper(st.Cur)),
	})
	b.loSaveReply(ctx, chatID, msgID, id, st, loStepBottom, text, b.loQuestionNav(loc))
}

// loOpenByPress opens the screen of the date named by "sl:o:<session>", the
// press of splitSeatsByPriceButton. The date's event and name are read from the
// session's summary, which answers 404 for a date of another organization.
func (b *Bot) loOpenByPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt, arg string) {
	loc := id.Locale()
	sessionID, err := uuid.Parse(arg)
	if err != nil {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sl.gone", nil), b.backKeyboard(loc, "events:1"))
		return
	}
	sum, err := b.arena.SessionSummary(ctx, jwt, id.Current.OrgID, sessionID)
	if err != nil {
		if IsAPIError(err, http.StatusNotFound) {
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sl.gone", nil), b.backKeyboard(loc, "events:1"))
			return
		}
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if sum.Session.OrgId != id.Current.OrgID {
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sl.gone", nil), b.backKeyboard(loc, "events:1"))
		return
	}
	b.seatLayoutOpen(ctx, chatID, msgID, id, jwt, sum.Session.EventId, sessionID, sum.Session.EventName)
}

// splitSeatsByPriceButton is the entry point of the "Split seats by price"
// step (spec 64 section 2.4): a button whose press opens the seat layout
// screen of a date, the same screen as the "Seat layout" button of the date's
// card. The press goes through the "sl:" router. The callback carries the
// session only (Telegram limits it to 64 bytes, two UUIDs do not fit): the
// event is read back from the session, so eventID is accepted for the caller's
// convenience and not sent.
func (b *Bot) splitSeatsByPriceButton(loc string, _, sessionID uuid.UUID) Button {
	return Button{Label: b.texts.T(loc, "bot.sl.split_btn", nil), Data: "o:" + sessionID.String()}
}

// seatLayoutText takes the typed prices of a date with one category. It
// reports whether the text was taken.
func (b *Bot) seatLayoutText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st seatLayoutDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, seatLayoutDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: seat layout dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "el:new"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != loStepTop && step != loStepBottom) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canSeatLayout(id) {
		b.leaveSeatLayout(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	price, ok := loParsePrice(text)
	switch {
	case !ok:
		note := b.texts.T(loc, "bot.sl.err_price", nil) + "\n\n"
		if step == loStepTop {
			b.loAskTop(ctx, chatID, edit, id, st, note)
		} else {
			b.loAskBottom(ctx, chatID, edit, id, st, note)
		}
	case step == loStepTop:
		st.Top, st.Bottom = price, 0
		b.loAskBottom(ctx, chatID, edit, id, st, "")
	case price >= st.Top:
		b.loAskBottom(ctx, chatID, edit, id, st, b.texts.T(loc, "bot.sl.err_bottom", map[string]any{
			"Top": Esc(FormatMoney(st.Top, st.Cur, loc)),
		})+"\n\n")
	default:
		st.Bottom = price
		alg, found, err := b.loAlgorithm(ctx, jwt, st)
		if err != nil {
			b.ecError(ctx, chatID, edit, id, err, "home")
			return true
		}
		if !found {
			b.seatLayoutShowStart(ctx, chatID, edit, id, jwt, st, b.texts.T(loc, "bot.sl.algorithm_gone", nil)+"\n\n")
			return true
		}
		b.loAfterQuestions(ctx, chatID, edit, id, jwt, st, alg)
	}
	return true
}

// ─── the options screen ───────────────────────────────────────────────────────

var loSideKeys = map[string]string{
	"top": "bot.sl.stage_top", "bottom": "bot.sl.stage_bottom", "left": "bot.sl.stage_left", "right": "bot.sl.stage_right",
}

func (b *Bot) loShowOptions(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, note string) {
	loc := id.Locale()
	alg, ok, err := b.loAlgorithm(ctx, jwt, st)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !ok {
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.algorithm_gone", nil)+"\n\n")
		return
	}
	var sb strings.Builder
	sb.WriteString(note + b.loHead(loc, st) + "\n\n<b>" + Esc(b.loLabel(loc, alg.LabelKey, alg.Id)) + "</b>")
	if st.splitReady() {
		sb.WriteString("\n" + b.loSplitLine(loc, st))
	}
	if alg.DescriptionKey != nil {
		if d := b.loLabel(loc, *alg.DescriptionKey, ""); d != "" {
			sb.WriteString("\n" + Esc(d))
		}
	}
	var rows [][]Button
	if loNeedsStage(alg) {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.q_stage", nil))
		line := []Button{}
		for _, side := range loStageSides {
			label := b.texts.T(loc, loSideKeys[side], nil)
			if st.Side == side {
				label = "✔ " + label
			}
			line = append(line, Button{Label: label, Data: "sd:" + side})
			if len(line) == 2 {
				rows, line = append(rows, line), nil
			}
		}
		if len(line) > 0 {
			rows = append(rows, line)
		}
	}
	for i, p := range loAsked(alg) {
		cur := loChoiceValue(p, st.Choice)
		label := b.loLabel(loc, p.LabelKey, p.Name)
		sb.WriteString("\n\n<b>" + Esc(label) + "</b>")
		if p.HintKey != nil {
			if h := b.loLabel(loc, *p.HintKey, ""); h != "" {
				sb.WriteString("\n" + Esc(h))
			}
		}
		switch p.Type {
		case "boolean":
			on, _ := cur.(bool)
			mark, next := "☐ ", 1
			if on {
				mark, next = "☑ ", 0
			}
			rows = append(rows, []Button{{Label: mark + label, Data: fmt.Sprintf("pm:%d:%d", i, next)}})
		case "enum":
			var line []Button
			if p.Options != nil {
				for j, o := range *p.Options {
					ol := b.loLabel(loc, o.LabelKey, o.Value)
					if s, _ := cur.(string); s == o.Value {
						ol = "✔ " + ol
					}
					line = append(line, Button{Label: ol, Data: fmt.Sprintf("pm:%d:%d", i, j)})
					if len(line) == 2 {
						rows, line = append(rows, line), nil
					}
				}
			}
			if len(line) > 0 {
				rows = append(rows, line)
			}
		}
	}
	rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.show_btn", nil), Data: "go"}})
	if st.splitReady() {
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.split_params_btn", nil), Data: "nq"}})
	}
	rows = append(rows, b.loNavRows(loc, "st")...)
	st.Fingerprint = ""
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveSeatLayoutState(ctx, id.Link.TelegramUserID, st, loStepOptions)
	b.reply(ctx, chatID, msgID, clipMessage(sb.String(), maxMessageRunes), loKeyboard(rows))
}

// loSetOption records one answer ("<i>:<j>") and draws the screen again.
func (b *Bot) loSetOption(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, arg string) {
	alg, ok, err := b.loAlgorithm(ctx, jwt, st)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !ok {
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(id.Locale(), "bot.sl.algorithm_gone", nil)+"\n\n")
		return
	}
	var i, j int
	if _, err := fmt.Sscanf(arg, "%d:%d", &i, &j); err == nil {
		asked := loAsked(alg)
		if i >= 0 && i < len(asked) {
			p := asked[i]
			if st.Choice == nil {
				st.Choice = map[string]any{}
			}
			switch p.Type {
			case "boolean":
				st.Choice[p.Name] = j == 1
			case "enum":
				if p.Options != nil && j >= 0 && j < len(*p.Options) {
					st.Choice[p.Name] = (*p.Options)[j].Value
				}
			}
		}
	}
	b.loShowOptions(ctx, chatID, msgID, id, jwt, st, "")
}

// ─── the proposal ─────────────────────────────────────────────────────────────

// loProposal is a proposal tied to the categories it would fill.
type loProposal struct {
	Alg     openapi.SeatLayoutAlgorithm
	Res     openapi.SeatLayoutSuggestResponse
	Targets []loTarget
	Tiers   []openapi.TicketTierItem
	Plan    loPlan
	Print   string
	// Split is true when the date has one category and the proposal is cut
	// by the person's answers (new categories).
	Split bool
}

// loSplitLine tells the answers of a date with one category.
func (b *Bot) loSplitLine(loc string, st seatLayoutDialog) string {
	return b.texts.T(loc, "bot.sl.split_line", map[string]any{
		"Cats": st.Cats, "Top": Esc(FormatMoney(st.Top, st.Cur, loc)), "Bottom": Esc(FormatMoney(st.Bottom, st.Cur, loc)),
	})
}

// loTierNames lists the names of all categories of the date.
func loTierNames(tiers []openapi.TicketTierItem) []string {
	out := make([]string, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, t.Name)
	}
	return out
}

// loErrorKey maps the API's refusals of a proposal to the screen's words. An
// unknown refusal returns "".
func loErrorKey(err error) string {
	switch APIErrorCode(err) {
	case seatLayoutCodeNoPlan, seatLayoutCodeNoSeats:
		return "bot.sl.not_seated"
	case seatLayoutCodeAnnotation:
		return "bot.sl.err_stage"
	case "seat_layout.invalid_param", "seat_layout.invalid_annotation":
		return "bot.sl.err_param"
	}
	return ""
}

// loPropose asks arena-api for a proposal on the current options and ties it
// to the date's categories. ok is false when the screen already answered the
// person (a refusal, too few categories, a hall too large for a chat).
func (b *Bot) loPropose(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st *seatLayoutDialog) (p loProposal, ok bool) {
	loc := id.Locale()
	tiers, err := b.arena.ListTiers(ctx, jwt, st.OrgID, st.EventID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return p, false
	}
	targets, _ := loTargets(tiers)
	if len(targets) < 1 {
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, *st, "")
		return p, false
	}
	split := len(targets) == 1
	alg, found, err := b.loAlgorithm(ctx, jwt, *st)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return p, false
	}
	if !found {
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, *st, b.texts.T(loc, "bot.sl.algorithm_gone", nil)+"\n\n")
		return p, false
	}
	req := openapi.SeatLayoutSuggestRequest{Algorithm: alg.Id}
	var params map[string]any
	if split {
		if !loSupportsNew(alg) {
			b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, *st, b.texts.T(loc, "bot.sl.err_method_one", nil)+"\n\n")
			return p, false
		}
		if !st.splitReady() {
			// Answers missing (a date that lost its categories meanwhile): ask again.
			st.Cur = tierCurrency(tiers)
			b.loAskCats(ctx, chatID, msgID, id, *st, "")
			return p, false
		}
		params = loRequestParamsNew(alg, st.split(), st.Choice)
	} else {
		params = loRequestParams(alg, targets, st.Choice)
	}
	req.Params = &params
	if loNeedsStage(alg) {
		if st.Side == "" {
			b.loShowOptions(ctx, chatID, msgID, id, jwt, *st, b.texts.T(loc, "bot.sl.err_stage", nil)+"\n\n")
			return p, false
		}
		if st.Bounds == nil {
			bounds, ok, err := b.arena.PlanVersionBounds(ctx, jwt, st.VenueID, st.VersionID)
			if err != nil {
				b.ecError(ctx, chatID, msgID, id, err, "home")
				return p, false
			}
			if !ok {
				b.loShowOptions(ctx, chatID, msgID, id, jwt, *st, b.texts.T(loc, "bot.sl.err_plan", nil)+"\n\n")
				return p, false
			}
			st.Bounds = &bounds
		}
		x, y, _ := loStagePoint(*st.Bounds, st.Side)
		req.Annotations = &openapi.SeatLayoutAnnotations{FocalPoint: &openapi.SeatLayoutPoint{X: float32(x), Y: float32(y)}}
	}
	res, err := b.arena.SeatLayoutSuggest(ctx, jwt, st.OrgID, st.SessionID, req)
	if err != nil {
		if key := loErrorKey(err); key != "" {
			b.loShowOptions(ctx, chatID, msgID, id, jwt, *st, b.texts.T(loc, key, nil)+"\n\n")
			return p, false
		}
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return p, false
	}
	var plan loPlan
	if split {
		plan, err = loBuildPlanNew(res, targets, loEmptyOpen(tiers), loTierNames(tiers), func(n int) string {
			return b.texts.T(loc, "bot.sl.new_cat_name", map[string]any{"N": n})
		})
	} else {
		plan, err = loBuildPlan(res, targets)
	}
	if err != nil {
		if errors.Is(err, errLoTooManyTiers) {
			b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sl.err_too_many_tiers", nil), loKeyboard(b.loNavRows(loc, "st")))
			return p, false
		}
		b.logger.Error("eventbot: seat layout proposal does not fit the categories", slog.String("error", err.Error()))
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sl.err_fit", nil), loKeyboard(b.loNavRows(loc, "st")))
		return p, false
	}
	return loProposal{
		Alg: alg, Res: res, Targets: targets, Tiers: tiers, Plan: plan, Split: split,
		Print: loFingerprint(res.Algorithm, plan, res.Layout.FrozenSeatKeys),
	}, true
}

func (b *Bot) loPreview(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, note string) {
	p, ok := b.loPropose(ctx, chatID, msgID, id, jwt, &st)
	if !ok {
		return
	}
	text, rows := b.loPreviewScreen(id.Locale(), st, p, note)
	st.Fingerprint = p.Print
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveSeatLayoutState(ctx, id.Link.TelegramUserID, st, loStepPreview)
	b.reply(ctx, chatID, msgID, clipMessage(text, maxMessageRunes), loKeyboard(rows))
}

// loPreviewScreen is the text and the buttons of the proposal's preview: per
// category the name, seats, price and the revenue at a full house (new ones
// marked), the full house total and the realistic estimate.
func (b *Bot) loPreviewScreen(loc string, st seatLayoutDialog, p loProposal, note string) (string, [][]Button) {
	cur := tierCurrency(p.Tiers)
	m := p.Res.Metrics
	var sb strings.Builder
	sb.WriteString(note + b.loHead(loc, st) + "\n\n" + b.texts.T(loc, "bot.sl.preview_title", nil))
	bySlot := map[string]openapi.SeatLayoutSlotMetrics{}
	for _, s := range m.BySlot {
		bySlot[s.SlotKey] = s
	}
	if p.Split {
		sb.WriteString("\n" + b.loSplitLine(loc, st) + "\n")
	}
	for i, s := range p.Res.Layout.Slots {
		sm := bySlot[s.Key]
		key := "bot.sl.preview_line"
		if p.Plan.IsNew(i) {
			if sm.Seats == 0 {
				continue // a new category nobody sits in is not created
			}
			key = "bot.sl.preview_line_new"
		}
		sb.WriteString("\n" + b.texts.T(loc, key, map[string]any{
			"Name": Esc(p.Plan.Name[i]), "Price": Esc(FormatMoney(p.Plan.Price[i], cur, loc)),
			"Seats": sm.Seats, "Share": int(float64(sm.Share)*100 + 0.5),
			"Money": Esc(FormatMoney(sm.RevenueMinor, cur, loc)),
		}))
	}
	if m.SoldFrozen > 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.preview_frozen", map[string]any{"Sold": m.SoldSeats, "Held": m.HeldSeats}))
	}
	if m.ClosedSeats > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.sl.preview_closed", map[string]any{"Count": m.ClosedSeats}))
	}
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.preview_estimate", map[string]any{
		"Money": Esc(FormatMoney(m.RevenueEstimateMinor, cur, loc)),
	}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.sl.preview_realistic", map[string]any{
		"Money": Esc(FormatMoney(m.RealisticRevenueMinor, cur, loc)),
	}))
	var rows [][]Button
	body, err := json.Marshal(p.Plan.batchRequest())
	switch {
	case len(p.Plan.Changes) == 0:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.preview_nothing", nil))
	case len(p.Plan.Changes) > loMaxChanges || err != nil || len(body) > loMaxBody:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.err_too_big", map[string]any{"Max": loMaxChanges}))
	default:
		if n := len(p.Plan.NewTiers); n > 0 {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.preview_hint_new", map[string]any{"Count": len(p.Plan.Changes), "Cats": n}))
		} else {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.sl.preview_hint", map[string]any{"Count": len(p.Plan.Changes)}))
		}
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.apply_btn", nil), Data: "ap:" + loFingerprintShort(p.Print)}})
	}
	if loNeedsStage(p.Alg) || len(loAsked(p.Alg)) > 0 {
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.options_btn", nil), Data: "op"}})
	}
	if p.Split {
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.split_params_btn", nil), Data: "nq"}})
	}
	rows = append(rows, b.loNavRows(loc, "st")...)
	return sb.String(), rows
}

// ─── apply and undo ───────────────────────────────────────────────────────────

// loApply applies the proposal the person was shown, and only that one: the
// proposal is asked for again and must have the shown fingerprint, otherwise
// the new one is shown and nothing is written.
func (b *Bot) loApply(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, step, fp string) {
	loc := id.Locale()
	if step != loStepPreview || st.Fingerprint == "" || loFingerprintShort(st.Fingerprint) != fp {
		// An old button, or a second press after the layout was applied.
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.out_of_date", nil)+"\n\n")
		return
	}
	shown := st.Fingerprint
	p, ok := b.loPropose(ctx, chatID, msgID, id, jwt, &st)
	if !ok {
		return
	}
	if p.Print != shown {
		b.loPreview(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.stale", nil)+"\n\n")
		return
	}
	if len(p.Plan.Changes) == 0 || len(p.Plan.Changes) > loMaxChanges {
		b.loPreview(ctx, chatID, msgID, id, jwt, st, "")
		return
	}
	req := p.Plan.batchRequest()
	if body, err := json.Marshal(req); err != nil || len(body) > loMaxBody {
		b.loPreview(ctx, chatID, msgID, id, jwt, st, "")
		return
	}
	batch, err := b.arena.CreateSeatBatch(ctx, jwt, st.OrgID, st.SessionID, req)
	if err != nil {
		switch APIErrorCode(err) {
		case seatBatchCodeTooMany:
			b.loPreview(ctx, chatID, msgID, id, jwt, st, "")
		case seatBatchCodeTierNameTaken:
			// Somebody made a category of that name meanwhile: the next proposal
			// picks other names.
			b.loPreview(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.err_name_taken", nil)+"\n\n")
		case seatBatchCodeTooManyTiers:
			b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.err_too_many_tiers", nil)+"\n\n")
		default:
			if req.NewTiers != nil && IsAPIError(err, http.StatusForbidden) {
				// New categories need tier.create, which a manager does not hold.
				b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.err_tier_forbidden", nil)+"\n\n")
				return
			}
			if IsAPIError(err, http.StatusUnprocessableEntity) {
				b.loPreview(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.sl.err_apply", nil)+"\n\n")
				return
			}
			b.ecError(ctx, chatID, msgID, id, err, "home")
		}
		return
	}
	text, rows, madeTiers := b.loAppliedScreen(loc, st, p, batch)
	st.Batch, st.Fingerprint = batch.Id.String(), ""
	if madeTiers {
		st.MadeTiers = batch.Id.String()
	}
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveSeatLayoutState(ctx, id.Link.TelegramUserID, st, loStepDone)
	b.reply(ctx, chatID, msgID, clipMessage(text, maxMessageRunes), loKeyboard(rows))
}

// loAppliedScreen is the message after an apply: how many seats moved, the
// categories the batch created, an old category left with no seat (said, never
// closed from here) and the hint about "Undo".
func (b *Bot) loAppliedScreen(loc string, st seatLayoutDialog, p loProposal, batch openapi.SeatBatch) (text string, rows [][]Button, madeTiers bool) {
	unchanged := 0
	if batch.UnchangedSeatKeys != nil {
		unchanged = len(*batch.UnchangedSeatKeys)
	}
	text = b.loHead(loc, st) + "\n\n" + b.texts.T(loc, "bot.sl.applied", map[string]any{
		"Changed": len(batch.Changes), "Same": unchanged,
	})
	if n := len(batch.Skipped); n > 0 {
		text += "\n" + b.texts.T(loc, "bot.sl.applied_skipped", map[string]any{"Count": n})
	}
	created := []openapi.SeatBatchCreatedTier{}
	if batch.CreatedTiers != nil {
		created = *batch.CreatedTiers
	}
	if len(created) > 0 {
		text += "\n\n" + b.texts.T(loc, "bot.sl.created_title", nil)
		for _, c := range created {
			text += "\n" + b.texts.T(loc, "bot.sl.cat_line", map[string]any{
				"Name": Esc(c.Name), "Price": Esc(FormatMoney(c.PriceAmount, tierCurrency(p.Tiers), loc)),
			})
		}
		// An old category the layout left with no seat: said, never closed here.
		if p.Res.Metrics.SoldFrozen == 0 && len(batch.Skipped) == 0 {
			for _, t := range p.Targets {
				if !loPlanKeeps(p.Plan, t.ID) {
					text += "\n\n" + b.texts.T(loc, "bot.sl.old_empty", map[string]any{"Name": Esc(t.Name)})
				}
			}
		}
	}
	if len(batch.Changes) > 0 {
		rows = append(rows, []Button{{Label: b.texts.T(loc, "bot.sl.undo_btn", nil), Data: "rv:" + batch.Id.String()}})
		undoHint := "bot.sl.undo_hint"
		if len(created) > 0 {
			undoHint = "bot.sl.undo_hint_new"
		}
		text += "\n\n" + b.texts.T(loc, undoHint, nil)
	}
	rows = append(rows, b.loNavRows(loc, "st")...)
	return text, rows, len(created) > 0
}

// loRevert puts a batch back. The press names the batch, so an old message
// keeps meaning THAT batch; the API refuses one that was already undone.
func (b *Bot) loRevert(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st seatLayoutDialog, arg string) {
	loc := id.Locale()
	batchID, err := uuid.Parse(arg)
	if err != nil {
		return
	}
	batch, err := b.arena.RevertSeatBatch(ctx, jwt, st.OrgID, st.SessionID, batchID)
	if err != nil {
		key := ""
		switch APIErrorCode(err) {
		case seatBatchCodeNothingToRevert:
			key = "bot.sl.err_undo_nothing"
		case seatBatchCodeAlreadyReverted:
			key = "bot.sl.err_undo_done"
		case seatBatchCodeNotFound:
			key = "bot.sl.err_undo_gone"
		}
		if key == "" {
			b.ecError(ctx, chatID, msgID, id, err, "home")
			return
		}
		b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, key, nil)+"\n\n")
		return
	}
	note := b.texts.T(loc, "bot.sl.undone", map[string]any{"Count": len(batch.Changes)})
	if st.MadeTiers != "" && st.MadeTiers == arg {
		note += "\n" + b.texts.T(loc, "bot.sl.undone_new", nil)
	}
	if n := len(batch.Skipped); n > 0 {
		note += "\n" + b.texts.T(loc, "bot.sl.undone_skipped", map[string]any{"Count": n})
	}
	b.seatLayoutShowStart(ctx, chatID, msgID, id, jwt, st, note+"\n\n")
}

// loSchemePath is the event center path of the seat map screen of one date.
// It is built from three UUIDs only, so it always starts with /o/ and holds no
// character that could end a path segment.
func loSchemePath(orgID, eventID, sessionID uuid.UUID) string {
	return fmt.Sprintf("/o/%s/events/%s/sessions/%s/seats?embed=1", orgID, eventID, sessionID)
}

// loSchemeURL is the address a "Seat map" button opens for one date. A plain
// link goes straight to the screen (the person signs in there). A Mini App goes
// through /tg, which signs the person in from the Telegram launch data and then
// opens `next`; the path is query-escaped as one value.
func loSchemeURL(base string, orgID, eventID, sessionID uuid.UUID, webApp bool) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	path := loSchemePath(orgID, eventID, sessionID)
	if !webApp {
		return base + path
	}
	return base + "/tg?next=" + url.QueryEscape(path)
}

// schemeButton is the "Seat map" button of a seated date's card: a link to the
// web event center's seat map screen of the date, or the same as a Telegram
// Mini App button. It is shown only when BOT_SCHEME_BUTTON asks for it, the
// event center's address is known, and the chat is a private one (a web_app
// button works nowhere else).
func (b *Bot) schemeButton(chatID int64, loc string, orgID, eventID, sessionID uuid.UUID) (Button, bool) {
	if chatID <= 0 || (b.schemeMode != "url" && b.schemeMode != "web_app") {
		return Button{}, false
	}
	link := loSchemeURL(b.eventCenterURL, orgID, eventID, sessionID, b.schemeMode == "web_app")
	if link == "" {
		return Button{}, false
	}
	return Button{Label: b.texts.T(loc, "bot.sl.scheme_btn", nil), URL: link, WebApp: b.schemeMode == "web_app"}, true
}
