package eventbot

// disputes_screens.go — the "Disputes" screens (chargebacks, spec 51.3
// section 9.2, task DS-11). The bot computes nothing: it shows what
// GET .../disputes says and sends the owner's two decisions to the API.
//
//	dp:l:<group>:<page>   the list (groups open, review, closed), five a page
//	dp:v:<dispute id>     the card
//	dp:pk:<id>            send every file of the package as a document
//	dp:rb:<id>            build the package again (owner)
//	dp:sub:<id>           "Submit answer": the typed word, then the final screen
//	dp:acc:<id>           "Accept dispute": the same two steps
//	dp:ok:<tag>           the final press (dp:okw / dp:okl: the test outcomes)
//	dp:no                 leave the typed-word dialog
//	dp:own:<id>           "I answered myself": the question
//	dp:owny:<id>          yes
//	or:v:<order id>       "Open order" is the Orders card's own press
//
// Reading is for the owner and the manager. Only the owner answers: the
// manager sees the card and a note, and the server refuses him with 403 as
// well. Nothing leaves before the typed word AND the final button; a wrong
// word sends nothing. The list keeps no dialog row (it is re-read on every
// press); the typed-word step is a bot_dialogs row of kind "dispute".
//
// The buyer's name and e-mail are printed only in the private chat of the
// person who pressed the button, and the package files go only there.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	disputeDialogKind = "dispute"
	dpStepWord        = "word"
	dpStepFinal       = "final"

	dpActionSubmit = "submit"
	dpActionAccept = "accept"

	dpDefaultGroup = "open"

	// The states of the API (spec 51.3 section 4).
	dpNeedsResponse = "needs_response"
	dpDraftReady    = "draft_ready"
	dpSubmitted     = "submitted"
	dpUnderReview   = "under_review"

	dpChannelAPI = "api"

	dpMadridZone = "Europe/Madrid"
)

// disputeGroups are the filter chips, in the order they are shown.
var disputeGroups = []string{"open", "review", "closed"}

// disputeReasonGroups are the reason groups that have their own text; any
// other value reads as "other".
var disputeReasonGroups = map[string]bool{
	"fraud": true, "not_received": true, "not_recognized": true, "refund": true,
	"quality": true, "duplicate": true, "other": true,
}

// disputeStates are the states that have their own text.
var disputeStates = map[string]bool{
	"needs_response": true, "draft_ready": true, "submitted": true, "under_review": true,
	"accepted": true, "expired": true, "closed": true,
}

// disputeOutcomes are the outcomes that have their own text.
var disputeOutcomes = map[string]bool{"won": true, "lost": true, "accepted": true, "withdrawn": true, "expired": true}

// disputeGapCodes are the gap codes of the package (disputes/labels.go).
var disputeGapCodes = map[string]bool{
	"gap_no_order": true, "gap_no_letters": true, "gap_no_opens": true, "gap_no_access": true,
	"gap_access_off": true, "gap_no_scans": true, "gap_no_consent": true, "gap_no_terms": true,
	"gap_no_refunds": true, "gap_reissued": true,
}

// ─── pure helpers ─────────────────────────────────────────────────────────────

// disputeOpen reports a dispute that still waits for the organizer's answer.
func disputeOpen(state string) bool { return state == dpNeedsResponse || state == dpDraftReady }

// disputeGroupOf is the list a dispute of that state is in.
func disputeGroupOf(state string) string {
	switch state {
	case dpNeedsResponse, dpDraftReady:
		return "open"
	case dpSubmitted, dpUnderReview:
		return "review"
	}
	return "closed"
}

// canRespondDispute: only the owner answers (spec 51.3 section 10, owner
// decision 2026-11-02). The manager reads.
func canRespondDispute(id *Identity) bool { return isOwner(id) }

// disputeChip is the mark in front of a dispute line.
func disputeChip(d openapi.Dispute) string {
	switch d.State {
	case dpNeedsResponse, dpDraftReady:
		return "⚠️"
	case dpSubmitted, dpUnderReview:
		return "⏳"
	}
	if d.Outcome != nil {
		switch *d.Outcome {
		case "won", "withdrawn":
			return "✅"
		case "lost", "expired":
			return "❌"
		}
	}
	if d.State == "expired" {
		return "❌"
	}
	return "☑️"
}

// disputeKeyTag is the part of a dialog key the final buttons carry, so a
// button of an older final screen never confirms the current one.
func disputeKeyTag(key string) string {
	k := strings.TrimPrefix(key, "dp-")
	if len(k) > 12 {
		k = k[:12]
	}
	return k
}

// disputeFileSize writes a size in bytes the way a person reads it.
func disputeFileSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+(1<<9))>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// disputeDialog is the state of the typed-word step.
type disputeDialog struct {
	OrgID     uuid.UUID `json:"org_id"`
	DisputeID uuid.UUID `json:"dispute_id"`
	Action    string    `json:"action"`
	Num       int64     `json:"num"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Channel   string    `json:"channel"`
	Event     string    `json:"event"`
	// TestOutcome: the payment key is a test one, so the final screen also
	// offers the two test outcomes.
	TestOutcome bool `json:"test_outcome,omitempty"`
	// Key is minted when the final screen is shown; its tag rides on the
	// final buttons.
	Key   string `json:"key,omitempty"`
	MsgID int    `json:"msg_id,omitempty"`
}

// disputeWordKey is the typed word of an action.
func disputeWordKey(action string) string {
	if action == dpActionAccept {
		return disputeAcceptWordKey
	}
	return disputeSubmitWordKey
}

// disputeErrorKey picks the text of a refused or failed call. A 5xx or no
// answer at all leaves the outcome unknown: the text says so and sends the
// person to the card, never to a second press.
func disputeErrorKey(err error) string {
	var ae *APIError
	if !errors.As(err, &ae) {
		return "bot.dp.err_unknown"
	}
	switch {
	case ae.Status == http.StatusForbidden:
		return "bot.dp.err_forbidden"
	case ae.Status == http.StatusNotFound:
		return "bot.ec.not_found"
	case ae.Status == http.StatusConflict && ae.Code == "dispute.already_submitted":
		return "bot.dp.err_already"
	case ae.Status == http.StatusConflict && ae.Code == "dispute.closed_for_evidence":
		return "bot.dp.err_closed"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == "dispute.package_missing":
		return "bot.dp.err_no_package"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == "dispute.key_not_writable":
		return "bot.dp.err_key"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == "dispute.recipient_required":
		return "bot.dp.err_recipient"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == "dispute.test_outcome_live":
		return "bot.dp.err_test_live"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == "dispute.wrong_channel":
		return "bot.dp.err_channel"
	case ae.Status == http.StatusServiceUnavailable || ae.Status >= 500:
		return "bot.dp.err_unknown"
	}
	return "bot.dp.err_refused"
}

// ─── menu and dispatch ────────────────────────────────────────────────────────

// disputeMenuRow is the main menu's row (owner and manager; the platform
// operator works through the bot like on the other order screens).
func (b *Bot) disputeMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canViewSales(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.btn", nil), CallbackData: "dp:l:" + dpDefaultGroup + ":1"}}
}

// disputeCallback handles every "dp:<data>" press.
func (b *Bot) disputeCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	parts := strings.Split(data, ":")
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	private := chatID == from.ID
	switch parts[0] {
	case "l":
		group, page := dpDefaultGroup, 1
		if len(parts) > 1 {
			group = parts[1]
		}
		if len(parts) > 2 {
			page = ParsePage(parts[2])
		}
		b.showDisputeList(ctx, chatID, &msgID, id, jwt, group, page)
	case "v", "pk", "rb", "sub", "acc", "own", "owny":
		did, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		switch parts[0] {
		case "v":
			b.showDisputeByID(ctx, chatID, &msgID, id, jwt, private, did, "")
		case "pk":
			b.sendDisputePackage(ctx, chatID, &msgID, id, jwt, private, did)
		case "rb":
			b.rebuildDispute(ctx, chatID, &msgID, id, jwt, private, did)
		case "sub":
			b.startDisputeAction(ctx, chatID, &msgID, from, id, jwt, did, dpActionSubmit)
		case "acc":
			b.startDisputeAction(ctx, chatID, &msgID, from, id, jwt, did, dpActionAccept)
		case "own":
			b.askDisputeOwn(ctx, chatID, &msgID, id, jwt, did)
		case "owny":
			b.doDisputeOwn(ctx, chatID, &msgID, id, jwt, private, did)
		}
	case "ok", "okw", "okl":
		b.finalDisputePress(ctx, chatID, msgID, from, id, jwt, parts[0], arg)
	case "no":
		b.cancelDisputeDialog(ctx, chatID, &msgID, from, id, jwt, private)
	}
}

// ─── the list ─────────────────────────────────────────────────────────────────

func (b *Bot) showDisputeList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt, group string, page int) {
	loc := id.Locale()
	valid := false
	for _, g := range disputeGroups {
		if g == group {
			valid = true
		}
	}
	if !valid {
		group = dpDefaultGroup
	}
	if page < 1 {
		page = 1
	}
	list, err := b.arena.DisputeList(ctx, jwt, id.Current.OrgID, group, listPageSize, (page-1)*listPageSize)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	pages := (list.Total + listPageSize - 1) / listPageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		b.showDisputeList(ctx, chatID, editMsgID, id, jwt, group, pages)
		return
	}
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.dp.title", map[string]any{"Filter": b.texts.T(loc, "bot.dp.f_"+group, nil), "Count": list.Total}))
	if list.OpenCount > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.dp.open_line", map[string]any{"Count": list.OpenCount}))
	}
	var rows [][]models.InlineKeyboardButton
	var chips []models.InlineKeyboardButton
	for _, g := range disputeGroups {
		label := b.texts.T(loc, "bot.dp.f_"+g, nil)
		if g == group {
			label = "• " + label
		}
		chips = append(chips, models.InlineKeyboardButton{Text: label, CallbackData: "dp:l:" + g + ":1"})
	}
	rows = append(rows, chips)
	if len(list.Disputes) == 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.empty", nil))
	}
	for _, d := range list.Disputes {
		sb.WriteString("\n\n" + b.disputeRowText(loc, d))
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         b.texts.T(loc, "bot.dp.open_btn", map[string]any{"Order": disputeOrderLabel(d)}),
			CallbackData: "dp:v:" + d.Id.String(),
		}})
	}
	if pr := PagerRow(Pager{Prefix: "dp:l:" + group, Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); pr != nil {
		rows = append(rows, pr)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func disputeOrderLabel(d openapi.Dispute) string {
	if d.OrderNumber > 0 {
		return "№" + strconv.FormatInt(d.OrderNumber, 10)
	}
	return "—"
}

// disputeReasonText is the reason in words.
func (b *Bot) disputeReasonText(loc, group string) string {
	if !disputeReasonGroups[group] {
		group = "other"
	}
	return b.texts.T(loc, "bot.dp.reason_"+group, nil)
}

// disputeRowText is one line of the list (and its second line, the deadline,
// while the dispute waits for an answer).
func (b *Bot) disputeRowText(loc string, d openapi.Dispute) string {
	event := ""
	if d.EventName != "" {
		event = " · " + Esc(truncate(d.EventName, 60))
	}
	out := b.texts.T(loc, "bot.dp.row", map[string]any{
		"Chip": disputeChip(d), "Order": disputeOrderLabel(d), "Amount": FormatMoney(d.Amount, d.Currency, loc),
		"Reason": b.disputeReasonText(loc, d.ReasonGroup), "Event": event,
	})
	if disputeOpen(d.State) {
		if when, ok := b.disputeDue(loc, d); ok {
			out += "\n" + b.texts.T(loc, "bot.dp.row_due", map[string]any{"When": when})
		}
	}
	return out
}

// disputeDue is the deadline in Madrid time and, when it differs, the venue's
// zone. ok is false when the dispute has no deadline.
func (b *Bot) disputeDue(loc string, d openapi.Dispute) (string, bool) {
	if d.DueBy == nil || *d.DueBy == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, *d.DueBy)
	if err != nil {
		return "", false
	}
	madrid := FormatWhen(t, dpMadridZone)
	out := b.texts.T(loc, "bot.dp.when_madrid", map[string]any{"When": madrid})
	// The venue's own time is added only when it reads differently.
	if tz := strings.TrimSpace(d.VenueTimezone); tz != "" && tz != dpMadridZone {
		if _, lerr := time.LoadLocation(tz); lerr == nil {
			if venue := FormatWhen(t, tz); venue != madrid {
				out += " · " + b.texts.T(loc, "bot.dp.when_venue", map[string]any{"When": venue, "Zone": Esc(tz)})
			}
		}
	}
	return out, true
}

// ─── the card ─────────────────────────────────────────────────────────────────

func (b *Bot) showDisputeByID(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, private bool, did uuid.UUID, note string) {
	card, err := b.arena.DisputeGet(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "dp:l:"+dpDefaultGroup+":1")
		return
	}
	b.showDisputeCard(ctx, chatID, editMsgID, id, private, card, note)
}

// disputeCardText is the text of the card. The buyer is named only in a
// private chat.
func (b *Bot) disputeCardText(loc string, id *Identity, private bool, card openapi.DisputeCard) string {
	d := card.Dispute
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.dp.card_title", map[string]any{"Order": disputeOrderLabel(d)}))
	if d.EventName != "" {
		sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_event", map[string]any{"Event": Esc(truncate(d.EventName, 120))}))
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_amount", map[string]any{"Amount": FormatMoney(d.Amount, d.Currency, loc)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_reason", map[string]any{"Reason": b.disputeReasonText(loc, d.ReasonGroup)}))
	state := d.State
	if !disputeStates[state] {
		state = "closed"
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_state", map[string]any{"State": b.texts.T(loc, "bot.dp.state_"+state, nil)}))
	if d.Outcome != nil && disputeOutcomes[*d.Outcome] {
		sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_outcome", map[string]any{"State": b.texts.T(loc, "bot.dp.outcome_"+*d.Outcome, nil)}))
	}
	if private && (d.BuyerName != "" || d.BuyerEmail != "") {
		sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_buyer", map[string]any{"Name": Esc(truncate(d.BuyerName, 80)), "Email": Esc(truncate(d.BuyerEmail, 120))}))
	}
	if when, ok := b.disputeDue(loc, d); ok && disputeOpen(d.State) {
		key := "bot.dp.card_due"
		if d.DueBySource == "default" {
			key = "bot.dp.card_due_estimate" // arena did not get a date from the provider
		}
		sb.WriteString("\n" + b.texts.T(loc, key, map[string]any{"When": when}))
		if t, err := time.Parse(time.RFC3339, *d.DueBy); err == nil && t.Before(time.Now()) {
			sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_overdue", nil))
		}
	}
	if disputeOpen(d.State) && card.LowValue.Advised {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.card_lowvalue", map[string]any{"Amount": FormatMoney(card.LowValue.Threshold, d.Currency, loc)}))
	}
	if pkg := card.Package; pkg != nil {
		if len(pkg.Gaps) > 0 {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.card_gaps_head", nil))
			for _, g := range pkg.Gaps {
				sb.WriteString("\n" + b.disputeGapText(loc, g))
			}
		}
		if len(pkg.Files) > 0 {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.card_files_head", nil))
			for _, f := range pkg.Files {
				tag := ""
				if !f.ForProvider {
					tag = " " + b.texts.T(loc, "bot.dp.file_full_tag", nil)
				}
				sb.WriteString("\n" + b.texts.T(loc, "bot.dp.card_file", map[string]any{
					"Name": Esc(truncate(f.Filename, 60)), "Pages": f.Pages, "Size": disputeFileSize(f.Bytes), "Tag": tag,
				}))
			}
		}
	} else if disputeOpen(d.State) {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.no_package", nil))
	}
	if disputeOpen(d.State) && !canRespondDispute(id) {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.dp.card_owner_only", nil))
	}
	return sb.String()
}

// disputeGapText words a gap code; a code this bot does not know is shown
// by its own name rather than hidden.
func (b *Bot) disputeGapText(loc, code string) string {
	if disputeGapCodes[code] {
		return b.texts.T(loc, "bot.dp."+code, nil)
	}
	return b.texts.T(loc, "bot.dp.gap_other", map[string]any{"Code": Esc(truncate(code, 40))})
}

// disputeCardKeyboard builds the buttons of the card by role and state.
func (b *Bot) disputeCardKeyboard(loc string, id *Identity, private bool, card openapi.DisputeCard) *models.InlineKeyboardMarkup {
	d := card.Dispute
	var rows [][]models.InlineKeyboardButton
	var top []models.InlineKeyboardButton
	if private && card.Package != nil && len(card.Package.Files) > 0 {
		top = append(top, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.dp.package_btn", nil), CallbackData: "dp:pk:" + d.Id.String()})
	}
	if d.OrderId != nil && *d.OrderId != "" {
		top = append(top, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.dp.order_btn", nil), CallbackData: "or:v:" + *d.OrderId})
	}
	if len(top) > 0 {
		rows = append(rows, top)
	}
	if disputeOpen(d.State) && canRespondDispute(id) {
		if private {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.submit_btn", nil), CallbackData: "dp:sub:" + d.Id.String()}})
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.accept_btn", nil), CallbackData: "dp:acc:" + d.Id.String()}})
		}
		if d.Channel != dpChannelAPI {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.own_btn", nil), CallbackData: "dp:own:" + d.Id.String()}})
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.rebuild_btn", nil), CallbackData: "dp:rb:" + d.Id.String()}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "dp:l:" + disputeGroupOf(d.State) + ":1"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) showDisputeCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, private bool, card openapi.DisputeCard, note string) {
	loc := id.Locale()
	text := note + b.disputeCardText(loc, id, private, card)
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.disputeCardKeyboard(loc, id, private, card))
}

// disputeBackKeyboard leads back to the card and the list.
func (b *Bot) disputeBackKeyboard(loc string, did uuid.UUID) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	if did != uuid.Nil {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.dp.back_card_btn", nil), CallbackData: "dp:v:" + did.String()}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.back_list_btn", nil), CallbackData: "dp:l:" + dpDefaultGroup + ":1"}})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── the package ──────────────────────────────────────────────────────────────

// sendDisputePackage sends every file of the newest package as a document to
// the private chat. The files hold the buyer's data.
func (b *Bot) sendDisputePackage(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, private bool, did uuid.UUID) {
	loc := id.Locale()
	if !private {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.private_only", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	card, err := b.arena.DisputeGet(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "dp:l:"+dpDefaultGroup+":1")
		return
	}
	if card.Package == nil || len(card.Package.Files) == 0 {
		b.showDisputeCard(ctx, chatID, editMsgID, id, private, card, b.texts.T(loc, "bot.dp.no_package", nil)+"\n\n")
		return
	}
	order := disputeOrderLabel(card.Dispute)
	sent := 0
	for _, f := range card.Package.Files {
		raw, err := b.arena.DisputeFile(ctx, jwt, id.Current.OrgID, did, f.Id)
		if err != nil {
			b.logger.Warn("eventbot: dispute file download failed", slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.dp.files_failed", nil), b.disputeBackKeyboard(loc, did))
			return
		}
		key := "bot.dp.file_caption"
		if !f.ForProvider {
			key = "bot.dp.file_caption_full"
		}
		_, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
			ChatID:    chatID,
			Document:  &models.InputFileUpload{Filename: safeDocName(f.Filename), Data: bytes.NewReader(raw)},
			Caption:   b.texts.T(loc, key, map[string]any{"Order": order, "Name": Esc(truncate(f.Filename, 80))}),
			ParseMode: models.ParseModeHTML,
		})
		if err != nil {
			b.logger.Warn("eventbot: dispute file send failed", slog.Int("bytes", len(raw)), slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.dp.files_failed", nil), b.disputeBackKeyboard(loc, did))
			return
		}
		sent++
	}
	b.send(ctx, chatID, b.texts.T(loc, "bot.dp.files_sent", map[string]any{"Count": sent}), b.disputeBackKeyboard(loc, did))
}

// safeDocName keeps a package file name to safe characters.
func safeDocName(name string) string {
	name = unsafeFileChars.ReplaceAllString(strings.TrimSpace(name), "_")
	if name == "" || name == "_" {
		return "dispute.pdf"
	}
	return name
}

// rebuildDispute builds the package again (owner).
func (b *Bot) rebuildDispute(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, private bool, did uuid.UUID) {
	loc := id.Locale()
	if !canRespondDispute(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_forbidden", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.rebuilding", nil), nil)
	card, err := b.arena.DisputeRebuild(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.reply(ctx, chatID, nil, b.texts.T(loc, disputeErrorKey(err), nil), b.disputeBackKeyboard(loc, did))
		return
	}
	key := "bot.dp.rebuilt"
	switch {
	case card.DraftSaved != nil && *card.DraftSaved:
		key = "bot.dp.rebuilt_draft"
	case card.DraftError != nil && *card.DraftError != "":
		key = "bot.dp.rebuilt_nodraft"
	}
	b.showDisputeCard(ctx, chatID, nil, id, private, card, b.texts.T(loc, key, nil)+"\n\n")
}

// ─── "I answered myself" ──────────────────────────────────────────────────────

func (b *Bot) askDisputeOwn(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, did uuid.UUID) {
	loc := id.Locale()
	if !canRespondDispute(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_forbidden", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	card, err := b.arena.DisputeGet(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "dp:l:"+dpDefaultGroup+":1")
		return
	}
	if !disputeOpen(card.Dispute.State) || card.Dispute.Channel == dpChannelAPI {
		b.showDisputeCard(ctx, chatID, editMsgID, id, chatID == id.Link.TelegramUserID, card, b.texts.T(loc, "bot.dp.not_open", nil)+"\n\n")
		return
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.own_ask", map[string]any{"Order": disputeOrderLabel(card.Dispute)}),
		&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.dp.own_yes_btn", nil), CallbackData: "dp:owny:" + did.String()}},
			{{Text: b.texts.T(loc, "bot.dp.own_no_btn", nil), CallbackData: "dp:v:" + did.String()}},
		}})
}

func (b *Bot) doDisputeOwn(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, private bool, did uuid.UUID) {
	loc := id.Locale()
	if !canRespondDispute(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_forbidden", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	card, err := b.arena.DisputeSentByOrganizer(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, disputeErrorKey(err), nil), b.disputeBackKeyboard(loc, did))
		return
	}
	b.showDisputeCard(ctx, chatID, editMsgID, id, private, card, b.texts.T(loc, "bot.dp.done_own", nil)+"\n\n")
}

// ─── the typed-word dialog ────────────────────────────────────────────────────

func (b *Bot) saveDispute(ctx context.Context, tg int64, st disputeDialog, step string) bool {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, disputeDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: dispute dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return false
	}
	return true
}

func (b *Bot) leaveDispute(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, disputeDialogKind); err != nil {
		b.logger.Warn("eventbot: dispute dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// narrowDispute ends the typed-word dialog on every dp press that is not its
// own (the final buttons and "no"): going back to the card or the list must
// never leave a word waiting that would answer the dispute.
func (b *Bot) narrowDispute(ctx context.Context, tg int64, data string) {
	if strings.HasPrefix(data, "dp:ok") || data == "dp:no" {
		return
	}
	b.leaveDispute(ctx, tg)
}

// startDisputeAction opens the typed-word step after reading the dispute
// afresh. Nothing is sent here.
func (b *Bot) startDisputeAction(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, did uuid.UUID, action string) {
	loc := id.Locale()
	private := chatID == from.ID
	if !canRespondDispute(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_forbidden", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	if !private {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.private_only", nil), b.disputeBackKeyboard(loc, did))
		return
	}
	card, err := b.arena.DisputeGet(ctx, jwt, id.Current.OrgID, did)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "dp:l:"+dpDefaultGroup+":1")
		return
	}
	d := card.Dispute
	if !disputeOpen(d.State) {
		b.showDisputeCard(ctx, chatID, editMsgID, id, private, card, b.texts.T(loc, "bot.dp.not_open", nil)+"\n\n")
		return
	}
	if action == dpActionSubmit && card.Package == nil {
		b.showDisputeCard(ctx, chatID, editMsgID, id, private, card, b.texts.T(loc, "bot.dp.no_package", nil)+"\n\n")
		return
	}
	st := disputeDialog{
		OrgID: id.Current.OrgID, DisputeID: did, Action: action, Num: d.OrderNumber, Amount: d.Amount,
		Currency: d.Currency, Channel: d.Channel, Event: d.EventName, TestOutcome: card.TestOutcomeAvailable,
	}
	b.showDisputeWord(ctx, chatID, editMsgID, id, st, "")
}

func (st disputeDialog) orderLabel() string {
	if st.Num > 0 {
		return "№" + strconv.FormatInt(st.Num, 10)
	}
	return "—"
}

func (b *Bot) disputeWord(loc string, action string) string {
	return b.texts.T(loc, disputeWordKey(action), nil)
}

func (b *Bot) isDisputeWord(loc, action, text string) bool {
	return b.typedPhrase(loc, disputeWordKey(action), text)
}

func (st disputeDialog) vars(loc string) map[string]any {
	return map[string]any{
		"Order": st.orderLabel(), "Amount": FormatMoney(st.Amount, st.Currency, loc), "Event": Esc(truncate(st.Event, 120)),
	}
}

// showDisputeWord asks for the typed word. The text says what will happen,
// in the words of the channel (the bank, or a letter to the provider).
func (b *Bot) showDisputeWord(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st disputeDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	st.Key = ""
	if !b.saveDispute(ctx, id.Link.TelegramUserID, st, dpStepWord) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_unavailable", nil), b.disputeBackKeyboard(loc, st.DisputeID))
		return
	}
	key := "bot.dp.accept_ask"
	if st.Action == dpActionSubmit {
		key = "bot.dp.submit_ask_email"
		if st.Channel == dpChannelAPI {
			key = "bot.dp.submit_ask_api"
		}
	}
	vars := st.vars(loc)
	vars["Word"] = b.disputeWord(loc, st.Action)
	b.reply(ctx, chatID, editMsgID, clipMessage(note+b.texts.T(loc, key, vars), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.dp.cancel_btn", nil), CallbackData: "dp:no"}},
		{{Text: "« " + b.texts.T(loc, "bot.dp.back_card_btn", nil), CallbackData: "dp:v:" + st.DisputeID.String()}},
	}})
}

// showDisputeFinal is the screen after the typed word: the whole decision in
// one place and the buttons. Nothing is sent until a button is pressed.
func (b *Bot) showDisputeFinal(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st disputeDialog, note string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	if st.Key == "" {
		st.Key = "dp-" + uuid.NewString()
	}
	if !b.saveDispute(ctx, id.Link.TelegramUserID, st, dpStepFinal) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.err_unavailable", nil), b.disputeBackKeyboard(loc, st.DisputeID))
		return
	}
	tag := disputeKeyTag(st.Key)
	key, yes := "bot.dp.accept_final", "bot.dp.accept_yes_btn"
	if st.Action == dpActionSubmit {
		key, yes = "bot.dp.submit_final_email", "bot.dp.submit_yes_btn"
		if st.Channel == dpChannelAPI {
			key = "bot.dp.submit_final_api"
		}
	}
	rows := [][]models.InlineKeyboardButton{{{Text: b.texts.T(loc, yes, nil), CallbackData: "dp:ok:" + tag}}}
	if st.Action == dpActionSubmit && st.TestOutcome && st.Channel == dpChannelAPI {
		rows = append(rows,
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.test_win_btn", nil), CallbackData: "dp:okw:" + tag}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.test_lose_btn", nil), CallbackData: "dp:okl:" + tag}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.dp.cancel_btn", nil), CallbackData: "dp:no"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(note+b.texts.T(loc, key, st.vars(loc)), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// disputeText takes the typed word. It reports whether it took the text.
func (b *Bot) disputeText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if chatID != from.ID {
		return false // the dialog lives in the private chat only
	}
	var st disputeDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, disputeDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: dispute dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.dp.expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.dp.back_list_btn", nil), CallbackData: "dp:l:" + dpDefaultGroup + ":1"}},
		}})
		return true
	}
	if !found || step != dpStepWord {
		return false // the final screen takes buttons only
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		return false
	}
	if id.Current == nil || id.Current.OrgID != st.OrgID || !canRespondDispute(id) {
		b.leaveDispute(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if !b.isDisputeWord(loc, st.Action, text) {
		b.showDisputeWord(ctx, chatID, edit, id, st, b.texts.T(loc, "bot.dp.word_wrong", map[string]any{"Word": b.disputeWord(loc, st.Action)})+"\n\n")
		return true
	}
	b.showDisputeFinal(ctx, chatID, edit, id, st, "")
	return true
}

// cancelDisputeDialog handles "Cancel": nothing was sent.
func (b *Bot) cancelDisputeDialog(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, private bool) {
	loc := id.Locale()
	var st disputeDialog
	_, found, _, _ := b.dialogs.Load(ctx, from.ID, disputeDialogKind, &st)
	b.leaveDispute(ctx, from.ID)
	if !found {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.dp.gone", nil), b.disputeBackKeyboard(loc, uuid.Nil))
		return
	}
	b.showDisputeByID(ctx, chatID, editMsgID, id, jwt, private, st.DisputeID, b.texts.T(loc, "bot.dp.cancelled", nil)+"\n\n")
}

// finalDisputePress is the one press that sends the decision. Only the button
// of the final screen that is open now (it carries the tag of its key) sends;
// any other one sends nothing and redraws a screen.
func (b *Bot) finalDisputePress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, tag string) {
	loc := id.Locale()
	var st disputeDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, disputeDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: dispute dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return
	}
	if !found || st.OrgID != id.Current.OrgID {
		key := "bot.dp.gone"
		if expired {
			key = "bot.dp.expired"
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), b.disputeBackKeyboard(loc, uuid.Nil))
		return
	}
	if !canRespondDispute(id) || chatID != from.ID {
		b.leaveDispute(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.dp.err_forbidden", nil), b.disputeBackKeyboard(loc, st.DisputeID))
		return
	}
	if step != dpStepFinal || st.Key == "" {
		b.showDisputeWord(ctx, chatID, &msgID, id, st, "")
		return
	}
	if tag != disputeKeyTag(st.Key) {
		b.showDisputeFinal(ctx, chatID, &msgID, id, st, b.texts.T(loc, "bot.dp.stale_btn", nil)+"\n\n")
		return
	}
	testOutcome := ""
	switch kind {
	case "okw":
		testOutcome = "win"
	case "okl":
		testOutcome = "lose"
	}
	// The dialog ends before the call: the buttons are gone and a second press
	// finds nothing to confirm. The server also refuses a second answer.
	b.leaveDispute(ctx, from.ID)
	b.consumePress(ctx, chatID)
	b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.dp.sending", nil), nil)

	var card openapi.DisputeCard
	if st.Action == dpActionAccept {
		card, err = b.arena.DisputeAccept(ctx, jwt, st.OrgID, st.DisputeID)
	} else {
		card, err = b.arena.DisputeSubmit(ctx, jwt, st.OrgID, st.DisputeID, testOutcome)
	}
	if err != nil {
		key := disputeErrorKey(err)
		if key == "bot.dp.err_unknown" {
			b.logger.Warn("eventbot: dispute answer got no clear result", slog.String("action", st.Action), slog.String("error", err.Error()))
		}
		b.reply(ctx, chatID, nil, b.texts.T(loc, key, nil), b.disputeBackKeyboard(loc, st.DisputeID))
		return
	}
	done := "bot.dp.done_accept"
	if st.Action == dpActionSubmit {
		done = "bot.dp.done_submit_email"
		if st.Channel == dpChannelAPI {
			done = "bot.dp.done_submit_api"
		}
	}
	b.showDisputeCard(ctx, chatID, nil, id, true, card, b.texts.T(loc, done, nil)+"\n\n")
}
