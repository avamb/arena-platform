package eventbot

// textfmt_dialog.go — the loop "type the text as usual, see it formatted,
// decide" (spec 44, RT-05). The organizer types plain text. The bot says it is
// formatting (only when the answer takes a moment), asks arena-api to mark the
// text up, and shows the result as Telegram renders it, with four buttons:
// save as shown, without formatting, format again, cancel. The preview lives in
// bot_dialogs (DialogStore, kind "textfmt", 30 minutes), so a restart of the
// bot between the preview and the press loses nothing.
//
// The loop serves two texts: the description of an event (the wizard's
// question, kind "description") and the organizer's own text of the buyer
// letters (the event card's "Text in the letter" screen, textfmt_note.go).
//
// Whatever happens to the model — it is off, slow, over its daily cap, it
// changed a word — the organizer ends up with a valid text: the plain
// conversion of what they typed (tfDecide). The bot never shows an error for
// that, only that the text is shown as it was typed.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

const (
	// tfDialogKind is the dialog of the loop in bot_dialogs.
	tfDialogKind = "textfmt"
	// tfStepAwait waits for the text of the buyer letters to be typed.
	tfStepAwait = "await"
	// tfStepPreview waits for a button under a preview.
	tfStepPreview = "preview"
)

// tfDialog is the state of the loop between two messages.
type tfDialog struct {
	Kind  string `json:"kind"` // tfKindDescription or tfKindEmailNote
	OrgID string `json:"org_id"`
	// EventID is the event whose letter text is being written (email note).
	EventID string `json:"event_id,omitempty"`
	// Text is what the organizer typed, cleaned (the old notation).
	Text string `json:"text,omitempty"`
	// Art is the text shown in the preview: what "Save as shown" stores.
	Art string `json:"art,omitempty"`
	// Applied says Art carries the model's markup.
	Applied bool `json:"applied,omitempty"`
}

func (d tfDialog) kind() tfKind { return tfKind(d.Kind) }

// tfSave writes the dialog and slides its 30 minutes.
func (b *Bot) tfSave(ctx context.Context, tg int64, orgID uuid.UUID, step string, dlg tfDialog) error {
	dlg.OrgID = orgID.String()
	return b.dialogs.Save(ctx, tg, &orgID, tfDialogKind, step, dlg, dialogTTL)
}

// tfLoad reads the dialog. A failed read is logged and read as no dialog.
func (b *Bot) tfLoad(ctx context.Context, tg int64) (dlg tfDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, tfDialogKind, &dlg)
	if err != nil {
		b.logger.Error("eventbot: text formatting dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return tfDialog{}, "", false, false
	}
	return dlg, step, found, expired
}

// clearTextFmtDialog ends the loop, if any.
func (b *Bot) clearTextFmtDialog(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, tfDialogKind); err != nil {
		b.logger.Warn("eventbot: text formatting dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// tfFormat checks a typed text, asks arena-api to format it and decides what
// to show. refusal is set when the text is not sent at all (too long, a
// forbidden character, too many links). The "Formatting…" notice goes out only
// when the answer takes longer than posterReadingDelay.
func (b *Bot) tfFormat(ctx context.Context, chatID int64, loc, jwt string, orgID uuid.UUID, kind tfKind, text string) (clean string, out tfOutcome, refusal *tfRefusal) {
	clean, plain, ref := tfCheck(text, kind)
	if ref != nil {
		return clean, tfOutcome{}, ref
	}
	done := make(chan struct{})
	notice := time.AfterFunc(posterReadingDelay, func() {
		defer close(done)
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.formatting", nil), nil)
	})
	res, err := b.arena.FormatText(ctx, jwt, orgID, clean, string(kind), loc)
	if !notice.Stop() {
		<-done // the notice is already on its way: let it finish first
	}
	out = tfDecide(clean, plain, res, err, kind)
	// The reason and the lengths only: the text is the organizer's, not ours.
	b.logger.Info("eventbot: text formatted",
		slog.String("kind", string(kind)), slog.Bool("ai_applied", out.Applied), slog.String("reason", out.Reason),
		slog.Int("typed_chars", len([]rune(clean))), slog.Int("visible_chars", richtext.VisibleLen(out.Art)))
	return clean, out, nil
}

// tfRefusalText is the answer to a text the bot will not send.
func (b *Bot) tfRefusalText(loc string, ref *tfRefusal) string {
	return b.texts.T(loc, ref.Key, map[string]any{"Limit": ref.Limit, "Max": ref.Links})
}

// tfTyped is the loop for one typed text: refuse it, or format it, remember the
// preview and show it. editMsgID names the message a "format again" press came
// from (nil for a typed text).
func (b *Bot) tfTyped(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, dlg tfDialog, text string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	clean, out, ref := b.tfFormat(ctx, chatID, loc, jwt, orgID, dlg.kind(), text)
	if ref != nil {
		// Nothing was sent to the model. The dialog stays as it was (a letter
		// text still waits for its text; a description waits in the wizard).
		b.reply(ctx, chatID, editMsgID, b.tfRefusalText(loc, ref), nil)
		return
	}
	dlg.Text, dlg.Art, dlg.Applied = clean, out.Art, out.Applied
	if err := b.tfSave(ctx, from.ID, orgID, tfStepPreview, dlg); err != nil {
		b.logger.Error("eventbot: text formatting dialog save failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	b.tfShowPreview(ctx, chatID, editMsgID, loc, dlg)
}

// tfKeyboard is the four buttons under a preview.
func (b *Bot) tfKeyboard(loc string) *models.InlineKeyboardMarkup {
	t := func(key string) string { return b.texts.T(loc, key, nil) }
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: t("bot.tf.save_btn"), CallbackData: "tf:s"}},
		{{Text: t("bot.tf.plain_btn"), CallbackData: "tf:p"}, {Text: t("bot.tf.again_btn"), CallbackData: "tf:a"}},
		{{Text: t("bot.tf.cancel_btn"), CallbackData: "tf:c"}},
	}}
}

// tfShowPreview sends the formatted text as Telegram renders it, then the
// question with the buttons. A long text goes in messages of its own and the
// buttons follow, so no message passes 4096 characters.
func (b *Bot) tfShowPreview(ctx context.Context, chatID int64, editMsgID *int, loc string, dlg tfDialog) {
	note := ""
	if !dlg.Applied {
		note = b.texts.T(loc, "bot.tf.not_applied_note", nil) + "\n\n"
	}
	preview := richtext.ToTelegramHTML(dlg.Art)
	texts, last := tfScreens(preview, note, b.texts.T(loc, "bot.tf.preview_ask", nil))
	for _, part := range texts {
		if err := b.sendChecked(ctx, chatID, part, nil); err != nil {
			// Telegram refused the markup (it should not): the plain text.
			b.logger.Warn("eventbot: preview markup refused", slog.String("error", err.Error()))
			b.send(ctx, chatID, Esc(truncate(richtext.PlainText(dlg.Art), tgMessageMax)), nil)
			break
		}
	}
	if err := b.replyChecked(ctx, chatID, editMsgID, last, b.tfKeyboard(loc)); err != nil && len(texts) == 0 {
		b.logger.Warn("eventbot: preview markup refused", slog.String("error", err.Error()))
		plain := Esc(truncate(richtext.PlainText(dlg.Art), tgMessageMax-600))
		b.reply(ctx, chatID, editMsgID, plain+"\n\n"+note+b.texts.T(loc, "bot.tf.preview_ask", nil), b.tfKeyboard(loc))
	}
}

// descriptionTyped takes the text an organizer typed at the wizard's
// description question: it is formatted and previewed, not stored yet.
func (b *Bot) descriptionTyped(ctx context.Context, chatID int64, from *models.User, id *Identity, jwt string, text string) {
	dlg := tfDialog{Kind: string(tfKindDescription)}
	b.tfTyped(ctx, chatID, nil, from, id, jwt, dlg, text)
}

// tfText takes a typed text for the loop: a new text while a preview is open,
// or the text of the buyer letters when it is awaited. It reports whether it
// took the text.
func (b *Bot) tfText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	dlg, step, found, expired := b.tfLoad(ctx, from.ID)
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.expired", nil), nil)
		return true
	}
	if !found || (step != tfStepAwait && step != tfStepPreview) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || dlg.OrgID != id.Current.OrgID.String() {
		b.clearTextFmtDialog(ctx, from.ID) // another organization now: not theirs to answer
		return false
	}
	b.tfTyped(ctx, chatID, nil, from, id, jwt, dlg, text)
	return true
}

// tfCallback handles every "tf:<data>" button.
func (b *Bot) tfCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	parts := strings.Split(data, ":")
	switch parts[0] {
	case "s", "p", "a", "c":
	default:
		return
	}
	dlg, step, found, _ := b.tfLoad(ctx, from.ID)
	if !found || step != tfStepPreview || dlg.OrgID != id.Current.OrgID.String() {
		// An older preview, or the 30 minutes ran out: the person types again.
		b.clearTextFmtDialog(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.tf.expired", nil), b.backKeyboard(loc, "home"))
		return
	}
	switch parts[0] {
	case "a":
		b.tfTyped(ctx, chatID, &msgID, from, id, jwt, dlg, dlg.Text)
	case "c":
		b.clearTextFmtDialog(ctx, from.ID)
		b.tfCancelled(ctx, chatID, msgID, from, id, jwt, dlg)
	default:
		art := dlg.Art
		if parts[0] == "p" {
			if _, plain, ref := tfCheck(dlg.Text, dlg.kind()); ref == nil {
				art = plain
			}
		}
		b.tfCommit(ctx, chatID, msgID, from, id, jwt, dlg, art)
	}
}

// tfCommit stores the text the organizer chose.
func (b *Bot) tfCommit(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, _ tfDialog, art string) {
	b.tfCommitDescription(ctx, chatID, msgID, from, id, jwt, art)
}

// tfCommitDescription gives the approved description to the wizard, which
// moves on to its next question.
func (b *Bot) tfCommitDescription(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, art string) {
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil {
		b.wizardFail(ctx, chatID, &msgID, ws.Locale, err)
		return
	}
	b.clearTextFmtDialog(ctx, from.ID)
	if d == nil {
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}
	if d.Step != stXDescription {
		// The draft has moved on since the preview: show where it is.
		b.wizardRender(ctx, chatID, &msgID, ws, d, "")
		return
	}
	// The filter once more: what is stored is what the server will accept.
	clean, err := richtext.Normalize(art, richtext.FormatART, richtext.ProfileDescription)
	if err != nil || clean == "" {
		b.wizardRender(ctx, chatID, &msgID, ws, d, b.texts.T(ws.Locale, "bot.tf.err_generic", nil))
		return
	}
	b.wizardApply(ctx, chatID, &msgID, from, ws, d, WizInput{Art: clean})
}

// tfCancelled answers "Cancel" under a preview: nothing changes, and the
// organizer is back where they were.
func (b *Bot) tfCancelled(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, _ tfDialog) {
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil {
		b.wizardFail(ctx, chatID, &msgID, ws.Locale, err)
		return
	}
	if d == nil {
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}
	b.wizardRender(ctx, chatID, &msgID, ws, d, b.texts.T(ws.Locale, "bot.tf.cancelled", nil))
}
