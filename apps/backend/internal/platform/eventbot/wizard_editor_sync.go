package eventbot

// wizard_editor_sync.go — the edit wizard after the mini app editor saved the
// description (the «Править в редакторе» button on the description question,
// textfmt_miniapp.go).
//
// The wizard keeps a copy of the event it loaded (Draft.Loaded) and a working
// copy (Draft.Event). The editor writes straight to arena-api, so after a save
// both copies hold the OLD description and the event's updated_at has moved.
// Two things could go wrong, and neither is allowed to:
//
//  1. «Publish changes» of some other part writing the old description back.
//     It cannot: BuildBundle sends the description only when the working copy
//     differs from the loaded one (Draft.bundleDescription), and a press on the
//     button leaves both untouched. The sync below keeps that true after a save
//     and also brings the screens up to the saved text.
//  2. The «changed elsewhere» check (wizardSave compares updated_at) blocking
//     the organizer because of their OWN save. The sync takes the new
//     updated_at, but only when the saved description is the one thing that
//     differs from what was loaded (name, age, promoter, poster as loaded);
//     if somebody else changed more, the warning stays.
//
// The bot cannot learn about a save (the mini app talks to arena-api only), so
// the draft is reconciled with the event at the moments it matters: opening
// the description question, leaving it for the card, and anything that
// publishes. A text the organizer typed into the draft after loading is theirs
// and is never replaced here: it goes through the usual «changed elsewhere»
// check instead.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// adoptEditorDescription brings an edit draft up to the description the event
// holds now. It reports whether the draft changed. Nothing happens when the
// draft is not an edit of this event, when the event's description is the one
// the draft loaded (as ART, so a plain-text row rewritten with the same words
// is "no change"), or when the organizer changed the description in the draft.
func (d *Draft) adoptEditorDescription(ev openapi.EventItem) bool {
	if d.Mode != ModeEdit || d.Loaded == nil || d.Event.EventID == "" || d.Event.EventID != ev.Id.String() {
		return false
	}
	if d.descriptionChanged() {
		return false // the organizer's own new text: not ours to replace
	}
	format := richtext.FormatText
	if d.Loaded.Event.DescriptionFormat == richtext.FormatART {
		format = richtext.FormatART
	}
	art := descriptionART(ev)
	if art == normalizedART(d.Loaded.Event.Description, format, richtext.ProfileDescription) {
		return false
	}
	text, fm := art, richtext.FormatART
	if strings.TrimSpace(art) == "" {
		text, fm = "", ""
	}
	d.Event.Description, d.Event.DescriptionFormat = text, fm
	d.Loaded.Event.Description, d.Loaded.Event.DescriptionFormat = text, fm
	if sameOtherParts(ev, d.Loaded.Event) {
		stamp := ev.UpdatedAt.UTC().Format(time.RFC3339Nano)
		d.Event.UpdatedAt, d.Loaded.Event.UpdatedAt = stamp, stamp
	}
	d.refreshChanged()
	return true
}

// editorSyncData lists the wizard buttons at which an edit draft is reconciled
// with the event: opening the description question, leaving the question for
// the card, and every press that previews or saves.
func editorSyncData(d *Draft, data string) bool {
	if d.Mode != ModeEdit || d.Loaded == nil {
		return false
	}
	switch {
	case data == "e:desc", data == "retry", strings.HasPrefix(data, "publish"):
		return true
	case data == "e:home":
		return d.Step == stXDescription
	}
	return false
}

// syncEditorDescription reads the event and adopts a description saved in the
// mini app into the draft, storing the draft when it changed. A failed read or
// store leaves everything as it was: the save-time check then decides.
func (b *Bot) syncEditorDescription(ctx context.Context, tgID int64, ws WizSession, d *Draft) {
	eid, err := uuid.Parse(d.Event.EventID)
	if err != nil {
		return
	}
	ev, err := b.arena.GetEvent(ctx, ws.JWT, eid)
	if err != nil {
		b.logger.Debug("eventbot: editor sync could not read the event", slog.String("error", routeOnly(err)))
		return
	}
	if !d.adoptEditorDescription(ev) {
		return
	}
	if _, err := b.storeDraft(ctx, tgID, ws, d); err != nil {
		b.logger.Warn("eventbot: draft not brought up to the editor's save", slog.Int64("telegram_user_id", tgID), slog.String("error", err.Error()))
	}
}
