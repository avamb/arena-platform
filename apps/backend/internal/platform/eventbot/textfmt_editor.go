package eventbot

// textfmt_editor.go — the bot says that the mini app editor saved something
// (spec 44, RT-12b: «бот подтверждает сохранение при следующем действии
// организатора»).
//
// The preview and the letter text screen stay in the chat while the organizer
// edits the same text in the Telegram mini app, so after a save they show an
// old text, and a press of «Save as shown» would write the old text over the
// new one. The bot cannot learn about the save (the mini app talks to
// arena-api only), so it asks when the organizer acts next:
//
//  1. At the press of «Править в редакторе» (tfMiniappCallback) the bot reads
//     the event and keeps a fingerprint of the stored text in the dialog
//     (Editor, Seen). The letter screen has no dialog of its own, so the press
//     opens one of the step "editor".
//  2. At the next press under that preview or on that screen, or at the next
//     text typed under the preview, the bot reads the event again. The same
//     fingerprint means nothing was saved and the action goes on as it was.
//  3. Another fingerprint means the editor saved: the bot says «Changes from
//     the editor are saved» ONCE (the marker is cleared), shows the saved text
//     and does NOT carry out the press, so the organizer decides again with the
//     new text in front of them. A typed text is the exception: it replaces
//     the preview on purpose, so it is only preceded by the note.
//
// The fingerprint is of the text as ART (an old-notation text is converted the
// way the readers do), so a plain-text row that the editor rewrote with the
// same words is "no change". A failed read changes nothing: no note, the
// action goes on as before. The dialog lives 30 minutes, as long as the
// session of the mini app.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// editorFingerprint is the fingerprint of a text as ART.
func editorFingerprint(art string) string {
	sum := sha256.Sum256([]byte(art))
	return hex.EncodeToString(sum[:])
}

// descriptionART is the stored description of an event as ART ("" when none).
func descriptionART(ev openapi.EventItem) string {
	if ev.Description == nil || *ev.Description == "" {
		return ""
	}
	if descriptionFormatOf(ev.DescriptionFormat) == richtext.FormatART {
		return *ev.Description
	}
	return richtext.FromLegacy(*ev.Description, richtext.ProfileDescription)
}

// editorTextART is the stored text of the kind, as ART.
func editorTextART(kind tfKind, ev openapi.EventItem) string {
	if kind == tfKindEmailNote {
		return emailNoteART(ev)
	}
	return descriptionART(ev)
}

// tfEditorRead reads the stored text of the event. ok is false when it could
// not be read (the caller then does nothing).
func (b *Bot) tfEditorRead(ctx context.Context, jwt string, orgID, eventID uuid.UUID, kind tfKind) (art string, ok bool) {
	ev, err := b.eventOf(ctx, jwt, orgID, eventID)
	if err != nil {
		b.logger.Debug("eventbot: editor save check could not read the event", slog.String("error", routeOnly(err)))
		return "", false
	}
	return editorTextART(kind, *ev), true
}

// tfEditorMark remembers what the text is, at the press that opens the editor.
// preview is true for the press under a preview (the dialog exists), false for
// the letter screen (the press opens the dialog of the step "editor"). A
// preview or a question the organizer is in the middle of is never replaced by
// the mark of a press on an older screen.
func (b *Bot) tfEditorMark(ctx context.Context, from *models.User, id *Identity, jwt string, kind tfKind, eventID string, preview bool) {
	eid, err := uuid.Parse(eventID)
	if err != nil || id.Current == nil {
		return
	}
	orgID := id.Current.OrgID
	art, ok := b.tfEditorRead(ctx, jwt, orgID, eid, kind)
	if !ok {
		return
	}
	dlg, step, found, _ := b.tfLoad(ctx, from.ID)
	if preview {
		if !found || step != tfStepPreview || dlg.OrgID != orgID.String() {
			return
		}
	} else {
		if found && (step == tfStepPreview || step == tfStepAwait) {
			return
		}
		dlg, step = tfDialog{Kind: string(kind), EventID: eventID}, tfStepEditor
	}
	dlg.Editor, dlg.Seen = true, editorFingerprint(art)
	if err := b.tfSave(ctx, from.ID, orgID, step, dlg); err != nil {
		b.logger.Warn("eventbot: editor mark not saved", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	}
}

// tfEditorChanged reports whether the event holds another text than at the
// press that opened the editor, and the text it holds now.
func (b *Bot) tfEditorChanged(ctx context.Context, id *Identity, jwt string, dlg tfDialog) (art string, changed bool) {
	eid, err := uuid.Parse(dlg.EventID)
	if err != nil || id.Current == nil || dlg.Seen == "" {
		return "", false
	}
	art, ok := b.tfEditorRead(ctx, jwt, id.Current.OrgID, eid, dlg.kind())
	if !ok || editorFingerprint(art) == dlg.Seen {
		return "", false
	}
	return art, true
}

// tfEditorSavedPreview handles a press under a preview after the editor saved.
// It reports whether it took the press: the pressed message becomes the note,
// and the preview is drawn again with the saved text.
func (b *Bot) tfEditorSavedPreview(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, dlg tfDialog) bool {
	art, changed := b.tfEditorChanged(ctx, id, jwt, dlg)
	if !changed {
		return false
	}
	loc := id.Locale()
	note := b.texts.T(loc, "bot.tf.editor_saved", nil)
	if art == "" {
		// The letter text was deleted in the editor: there is nothing to preview.
		eid, err := uuid.Parse(dlg.EventID)
		if err != nil || dlg.kind() != tfKindEmailNote {
			return false
		}
		b.clearTextFmtDialog(ctx, from.ID)
		b.showEmailNote(ctx, chatID, &msgID, id, jwt, eid, note+"\n\n")
		return true
	}
	dlg.Art, dlg.Applied, dlg.Text = art, true, richtext.ToLegacy(art)
	dlg.Editor, dlg.Seen = false, ""
	if err := b.tfSave(ctx, from.ID, id.Current.OrgID, tfStepPreview, dlg); err != nil {
		b.logger.Error("eventbot: text formatting dialog save failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return true
	}
	b.reply(ctx, chatID, &msgID, note, nil)
	b.tfShowPreview(ctx, chatID, nil, loc, dlg)
	return true
}

// tfEditorSavedNote handles a press on the letter screen after the editor
// saved: the screen is drawn again with the saved text and the note, the press
// itself is not carried out. It reports whether it took the press.
func (b *Bot) tfEditorSavedNote(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, eventID uuid.UUID) bool {
	dlg, step, found, _ := b.tfLoad(ctx, from.ID)
	if !found || step != tfStepEditor || !dlg.Editor || dlg.EventID != eventID.String() || id.Current == nil || dlg.OrgID != id.Current.OrgID.String() {
		return false
	}
	if _, changed := b.tfEditorChanged(ctx, id, jwt, dlg); !changed {
		return false
	}
	b.clearTextFmtDialog(ctx, from.ID)
	b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(id.Locale(), "bot.tf.editor_saved", nil)+"\n\n")
	return true
}

// tfEditorSavedTyped precedes a typed text with the note when the editor saved
// since the preview was drawn. The typed text goes on as it would have: it is
// the organizer's own replacement of the preview. The returned dialog has the
// marker cleared when the note was said.
func (b *Bot) tfEditorSavedTyped(ctx context.Context, chatID int64, id *Identity, jwt string, dlg tfDialog) tfDialog {
	if _, changed := b.tfEditorChanged(ctx, id, jwt, dlg); changed {
		b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.tf.editor_saved", nil), nil)
		dlg.Editor, dlg.Seen = false, ""
	}
	return dlg
}
