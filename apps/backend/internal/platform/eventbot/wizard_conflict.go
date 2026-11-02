package eventbot

// wizard_conflict.go — the «changed elsewhere» check of an edited event.
//
// An edit draft keeps the updated_at of the event as it was loaded. Somebody
// else may change the event meanwhile (the web event center, another manager
// in the bot, a site), and the save would write the draft's old copy over it.
// The check runs twice:
//
//  1. When the preview of «Publish changes» opens. The preview is where the
//     organizer decides, so a change made elsewhere is said THERE, with what
//     changed, instead of the preview's own «Publish» button.
//  2. When the save starts (wizardSave), because a change may land between the
//     preview and the press.
//
// A failed read never lets the save through: the person is told the check did
// not work and chooses to retry or to save anyway. On the preview a failed
// read is only a note, because the save checks again.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// editCheck is the outcome of the «changed elsewhere» check.
type editCheck int

const (
	// editUnchanged: updated_at is as loaded, or there is nothing to compare.
	editUnchanged editCheck = iota
	// editChangedElsewhere: the event was saved by somebody else since loading.
	editChangedElsewhere
	// editCheckFailed: the event could not be read, so nothing is known.
	editCheckFailed
)

// checkEditedElsewhere reads the event an edit draft changes and compares its
// updated_at with the one the draft loaded. A create draft, a draft stored
// before updated_at was kept and a draft without a valid event id have nothing
// to compare.
func (b *Bot) checkEditedElsewhere(ctx context.Context, ws WizSession, d *Draft) (openapi.EventItem, editCheck) {
	if d.Mode != ModeEdit || d.Event.UpdatedAt == "" {
		return openapi.EventItem{}, editUnchanged
	}
	eventID, err := uuid.Parse(d.Event.EventID)
	if err != nil {
		return openapi.EventItem{}, editUnchanged
	}
	ev, err := b.arena.GetEvent(ctx, ws.JWT, eventID)
	if err != nil {
		b.logger.Warn("eventbot: the changed-elsewhere check could not read the event",
			slog.String("event_id", eventID.String()), slog.String("error", routeOnly(err)))
		return openapi.EventItem{}, editCheckFailed
	}
	if ev.UpdatedAt.UTC().Format(time.RFC3339Nano) != d.Event.UpdatedAt {
		return ev, editChangedElsewhere
	}
	return ev, editUnchanged
}

// elsewhereChanges names the parts of the event that differ from what the
// draft loaded, in the order of the edit card. Only the parts the event itself
// carries are compared: dates and categories are not in it.
func elsewhereChanges(ev openapi.EventItem, l DraftEvent) []string {
	var out []string
	if ev.Name != l.Name {
		out = append(out, "name")
	}
	format := richtext.FormatText
	if l.DescriptionFormat == richtext.FormatART {
		format = richtext.FormatART
	}
	if descriptionART(ev) != normalizedART(l.Description, format, richtext.ProfileDescription) {
		out = append(out, "description")
	}
	poster, promoter := "", ""
	if ev.PosterMediaId != nil {
		poster = ev.PosterMediaId.String()
	}
	if ev.PromoterId != nil {
		promoter = ev.PromoterId.String()
	}
	if poster != l.PosterMediaID {
		out = append(out, "poster")
	}
	if deref(ev.AgeRating) != l.Age {
		out = append(out, "age")
	}
	if promoter != l.PromoterID {
		out = append(out, "promoter")
	}
	return out
}

// elsewhereText says what was changed elsewhere since the draft was loaded.
// The name and the age are shown with both values; the other parts by name.
func (b *Bot) elsewhereText(loc string, ev openapi.EventItem, d *Draft) string {
	t := func(key string, data map[string]any) string { return b.texts.T(loc, key, data) }
	loaded := d.Event
	if d.Loaded != nil {
		loaded = d.Loaded.Event
	}
	parts := elsewhereChanges(ev, loaded)
	if len(parts) == 0 {
		return t("bot.wz.elsewhere_unknown", nil)
	}
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		var line string
		switch p {
		case "name":
			line = t("bot.wz.edit_chg_name", map[string]any{"Old": Esc(loaded.Name), "New": Esc(ev.Name)})
		case "age":
			was, now := loaded.Age, deref(ev.AgeRating)
			if strings.TrimSpace(was) == "" {
				was = t("bot.wz.edit_chg_none", nil)
			}
			if strings.TrimSpace(now) == "" {
				now = t("bot.wz.edit_chg_none", nil)
			}
			line = t("bot.wz.edit_chg_field", map[string]any{"What": t("bot.wz.edit_field_age", nil), "Old": Esc(was), "New": Esc(now)})
		default:
			line = t("bot.wz.elsewhere_part", map[string]any{"What": t("bot.wz.edit_field_"+p, nil)})
		}
		lines = append(lines, "• "+line)
	}
	return t("bot.wz.elsewhere_list", map[string]any{"List": strings.Join(lines, "\n")})
}

// conflictPreviewText is the preview of «Publish changes» when the event was
// changed elsewhere: the warning, what changed there, then what the draft
// would send, and a footer that names the buttons below it.
func (b *Bot) conflictPreviewText(ws WizSession, d *Draft, ev openapi.EventItem, note string) string {
	loc := ws.Locale
	text := b.texts.T(loc, "bot.wz.edit_changed_meanwhile", map[string]any{"Name": Esc(d.Event.Name)}) +
		"\n\n" + b.elsewhereText(loc, ev, d) +
		"\n\n" + b.wizard.previewBody(loc, ws.OrgName, d) +
		"\n\n" + b.texts.T(loc, "bot.wz.preview_conflict_footer", nil)
	if note != "" {
		text = note + "\n\n" + text
	}
	return clipMessage(text, maxMessageRunes)
}

// conflictKeyboard is the choice after a change made elsewhere: save the
// draft's version over it, or cancel. The preview also keeps its way back to
// the card.
func (b *Bot) conflictKeyboard(loc string, withBack bool) *models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.wz.force_btn", nil), CallbackData: "wz:publish:force"}},
	}
	if withBack {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.wz.preview_back_btn", nil), CallbackData: "wz:e:home"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "wz:cancel"}})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// checkFailedKeyboard follows a check that could not read the event: retry
// the save (which checks again), save anyway, or cancel.
func (b *Bot) checkFailedKeyboard(loc string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: b.texts.T(loc, "bot.wz.retry_btn", nil), CallbackData: "wz:retry"},
			{Text: b.texts.T(loc, "bot.wz.force_btn", nil), CallbackData: "wz:publish:force"},
		},
		{{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "wz:cancel"}},
	}}
}

// renderCheckedPreview draws the preview of an edited event after the
// «changed elsewhere» check. It reports whether it drew the screen; when it
// did not, the caller draws the usual preview with the returned note.
func (b *Bot) renderCheckedPreview(ctx context.Context, chatID int64, editMsgID *int, ws WizSession, d *Draft, note string) (string, bool) {
	ev, res := b.checkEditedElsewhere(ctx, ws, d)
	switch res {
	case editChangedElsewhere:
		b.reply(ctx, chatID, editMsgID, b.conflictPreviewText(ws, d, ev, note), b.conflictKeyboard(ws.Locale, true))
		return note, true
	case editCheckFailed:
		failed := b.texts.T(ws.Locale, "bot.wz.preview_check_failed", nil)
		if note != "" {
			return note + "\n\n" + failed, false
		}
		return failed, false
	}
	return note, false
}

// saveGate runs the check before a save. It reports whether the save may go
// on; when it may not, the person has been told why and what to press.
func (b *Bot) saveGate(ctx context.Context, chatID int64, editMsgID *int, ws WizSession, d *Draft) bool {
	loc := ws.Locale
	ev, res := b.checkEditedElsewhere(ctx, ws, d)
	switch res {
	case editChangedElsewhere:
		text := b.texts.T(loc, "bot.wz.edit_changed_meanwhile", map[string]any{"Name": Esc(d.Event.Name)}) +
			"\n\n" + b.elsewhereText(loc, ev, d)
		b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.conflictKeyboard(loc, false))
		return false
	case editCheckFailed:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.edit_check_failed", map[string]any{"Name": Esc(d.Event.Name)}), b.checkFailedKeyboard(loc))
		return false
	}
	return true
}
