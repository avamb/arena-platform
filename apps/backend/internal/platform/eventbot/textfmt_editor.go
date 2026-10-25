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
	"time"

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

// normalizedART is a stored text as ART the way the server reads it: through
// richtext.Normalize with the profile of the text (an old-notation row is
// converted, an ART row is sanitised once more). Both sides of a comparison go
// through it, so a plain-text row that the editor rewrote with the same words
// is "no change". A text the server could not normalize stays as stored.
func normalizedART(text, format string, p richtext.Profile) string {
	if text == "" {
		return ""
	}
	art, err := richtext.Normalize(text, format, p)
	if err != nil {
		return text
	}
	return art
}

// descriptionART is the CANONICAL description of an event as ART ("" when
// none). It reads description_base, not the description of the list, which is
// the translation of the request's locale when the event has one.
func descriptionART(ev openapi.EventItem) string {
	format := richtext.FormatText
	if descriptionFormatOf(ev.DescriptionFormat) == richtext.FormatART {
		format = richtext.FormatART
	}
	return normalizedART(eventDescriptionBase(ev), format, richtext.ProfileDescription)
}

// editorTextART is the stored text of the kind, as ART.
func editorTextART(kind tfKind, ev openapi.EventItem) string {
	if kind != tfKindEmailNote {
		return descriptionART(ev)
	}
	if ev.EmailNote == nil {
		return ""
	}
	format := richtext.FormatText
	if ev.EmailNoteFormat != nil && *ev.EmailNoteFormat == richtext.FormatART {
		format = richtext.FormatART
	}
	return normalizedART(*ev.EmailNote, format, richtext.ProfileEmailNote)
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
	if !dlg.Editor || dlg.Seen == "" || dlg.EventID != eventID || dlg.kind() != kind {
		dlg.Editor, dlg.Seen = true, editorFingerprint(art)
	}
	// Else the editor was opened from this screen before and nothing has told
	// the organizer what it saved yet: Seen stays what it was, a second press
	// never overwrites the mark with the saved text (tfMiniappCallback asks
	// first, this is the guard for a save between the two reads).
	if err := b.tfSave(ctx, from.ID, orgID, step, dlg); err != nil {
		b.logger.Warn("eventbot: editor mark not saved", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	}
}

// tfEditorChanged reports whether the event holds another text than at the
// press that opened the editor, and the text it holds now.
func (b *Bot) tfEditorChanged(ctx context.Context, id *Identity, jwt string, dlg tfDialog) (art string, changed bool) {
	art, _, changed = b.tfEditorChangedEvent(ctx, id, jwt, dlg)
	return art, changed
}

// tfEditorChangedEvent is tfEditorChanged that also hands back the event it
// read, for the caller that needs more than the text.
func (b *Bot) tfEditorChangedEvent(ctx context.Context, id *Identity, jwt string, dlg tfDialog) (art string, ev *openapi.EventItem, changed bool) {
	eid, err := uuid.Parse(dlg.EventID)
	if err != nil || id.Current == nil || dlg.Seen == "" {
		return "", nil, false
	}
	ev, err = b.eventOf(ctx, jwt, id.Current.OrgID, eid)
	if err != nil {
		b.logger.Debug("eventbot: editor save check could not read the event", slog.String("error", routeOnly(err)))
		return "", nil, false
	}
	art = editorTextART(dlg.kind(), *ev)
	if editorFingerprint(art) == dlg.Seen {
		return "", nil, false
	}
	return art, ev, true
}

// tfEditorSavedPreview handles a press under a preview after the editor saved.
// It reports whether it took the press: the pressed message becomes the note,
// and the preview is drawn again with the saved text.
func (b *Bot) tfEditorSavedPreview(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, dlg tfDialog) bool {
	art, _, changed := b.tfEditorChangedEvent(ctx, id, jwt, dlg)
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
	b.tfEditorSyncDraft(ctx, from, id, jwt, dlg, art)
	b.reply(ctx, chatID, &msgID, note, nil)
	b.tfShowPreview(ctx, chatID, nil, id, loc, dlg)
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
func (b *Bot) tfEditorSavedTyped(ctx context.Context, chatID int64, from *models.User, id *Identity, jwt string, dlg tfDialog) tfDialog {
	if art, _, changed := b.tfEditorChangedEvent(ctx, id, jwt, dlg); changed {
		b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.tf.editor_saved", nil), nil)
		dlg.Editor, dlg.Seen = false, ""
		b.tfEditorSyncDraft(ctx, from, id, jwt, dlg, art)
	}
	return dlg
}

// tfEditorSyncDraft brings the wizard's edit draft up to the description the
// editor saved. Without it «Publish changes» after a save in the mini app
// always warned «changed elsewhere» (the event's updated_at moved), and «Save
// anyway» wrote the draft's stale copy of the event over the saved one.
//
// The saved ART goes into the draft's description and into the copy of the
// loaded event that "changed" is judged against, so the description is not
// re-sent unless the organizer changes it again. The loaded updated_at is
// refreshed from the same read ONLY when every other part the draft compares
// (name, age, promoter, poster) is what was loaded: then the editor's save is
// the only change since and the warning has nothing to say. If someone else
// edited too, the warning stays. A draft of another event, a new event's draft
// and a failed read leave everything as it was.
func (b *Bot) tfEditorSyncDraft(ctx context.Context, from *models.User, id *Identity, jwt string, dlg tfDialog, art string) {
	if b.queries == nil || dlg.kind() != tfKindDescription || dlg.EventID == "" || id.Current == nil || art == "" {
		return
	}
	eid, err := uuid.Parse(dlg.EventID)
	if err != nil {
		return
	}
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil || d == nil || d.Mode != ModeEdit || d.Loaded == nil || d.Event.EventID != dlg.EventID {
		return
	}
	d.Event.Description, d.Event.DescriptionFormat = art, richtext.FormatART
	d.Loaded.Event.Description, d.Loaded.Event.DescriptionFormat = art, richtext.FormatART
	if ev, err := b.arena.GetEvent(ctx, jwt, eid); err == nil && sameOtherParts(ev, d.Loaded.Event) {
		stamp := ev.UpdatedAt.UTC().Format(time.RFC3339Nano)
		d.Event.UpdatedAt, d.Loaded.Event.UpdatedAt = stamp, stamp
	}
	d.refreshChanged()
	if _, err := b.storeDraft(ctx, from.ID, ws, d); err != nil {
		b.logger.Warn("eventbot: draft not brought up to the editor's save", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	}
}

// sameOtherParts reports whether the event is, apart from its description, what
// the edit draft loaded: the name, the age, the promoter and the poster.
func sameOtherParts(ev openapi.EventItem, l DraftEvent) bool {
	promoter, poster := "", ""
	if ev.PromoterId != nil {
		promoter = ev.PromoterId.String()
	}
	if ev.PosterMediaId != nil {
		poster = ev.PosterMediaId.String()
	}
	return ev.Name == l.Name && deref(ev.AgeRating) == l.Age && promoter == l.PromoterID && poster == l.PosterMediaID
}
