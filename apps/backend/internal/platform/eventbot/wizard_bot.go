package eventbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg" // poster dimensions
	_ "image/png"  // poster dimensions
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// wizard_bot.go — how the wizard lives inside the Telegram bot: drafts are
// loaded and stored per (account, organization) in bot_drafts, every answer
// is applied and rendered, the poster is downloaded, checked and uploaded
// before the wizard sees it, and "Publish" runs the save.

const (
	posterMaxBytes = 5 << 20
	// posterMinWidth admits a photo Telegram has compressed: a 1080×1350
	// poster sent as a photo arrives as 1024×1280 (the longest side is
	// capped at 1280). Owner decision 2026-09-29: an organizer who sends a
	// photo must not be bounced — the file path keeps full quality, the
	// photo path keeps the sale.
	posterMinWidth = 1000
	posterTolerant = 0.02
)

// wizardSession builds the wizard's view of the caller.
func (b *Bot) wizardSession(id *Identity, jwt string) WizSession {
	return WizSession{JWT: jwt, OrgID: id.Current.OrgID, OrgName: id.Current.OrgName, Locale: id.Locale(), Defaults: DecodeDefaults(id.Link.Defaults)}
}

func (b *Bot) loadDraft(ctx context.Context, tgID int64, ws WizSession) (*Draft, *gen.BotDraftRow, error) {
	row, err := b.queries.GetBotDraft(ctx, tgID, ws.OrgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var d Draft
	if err := json.Unmarshal(row.State, &d); err != nil || d.Version != draftSchemaVersion || (row.Mode != ModeCreate && row.Mode != ModeEdit) {
		return nil, &row, nil // unreadable or foreign: treated as absent
	}
	if d.Mode == "" {
		d.Mode = row.Mode
	}
	return &d, &row, nil
}

func (b *Bot) storeDraft(ctx context.Context, tgID int64, ws WizSession, d *Draft) (string, error) {
	state, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	saved, _ := json.Marshal(d.Saved)
	mode := d.Mode
	if mode == "" {
		mode = ModeCreate
	}
	var eventID *uuid.UUID
	if id, err := uuid.Parse(d.Event.EventID); err == nil && d.Mode == ModeEdit {
		eventID = &id
	}
	row, err := b.queries.UpsertBotDraft(ctx, tgID, ws.OrgID, mode, eventID, d.Step, int32(draftSchemaVersion), state, saved)
	if err != nil {
		return "", err
	}
	return row.ID.String(), nil
}

// wizardOpenEvent starts "Edit" (copy=false) or "Repeat as new" (copy=true)
// on an existing event: the event is read back from arena and becomes the
// person's draft for this organization, replacing any draft they had.
func (b *Bot) wizardOpenEvent(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID, copy bool) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	ws := b.wizardSession(id, jwt)
	loc := ws.Locale
	d, notes, err := b.wizard.LoadEventDraft(ctx, b.arena, ws, eventID)
	if err != nil {
		if errors.Is(err, ErrNotEditable) {
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.edit_seated", nil), b.backKeyboard(loc, "event:"+eventID.String()+":1"))
			return
		}
		b.logger.Warn("eventbot: load event for edit failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.edit_load_failed", nil), b.backKeyboard(loc, "event:"+eventID.String()+":1"))
		return
	}
	note := b.texts.T(loc, "bot.wz.edit_intro", map[string]any{"Name": Esc(d.Event.Name)})
	if copy {
		d = CopyDraft(d)
		d.Channels = ws.Defaults.Channels
		if len(d.Channels) == 0 {
			if channels, err := b.arena.Channels(ctx, jwt, ws.OrgID); err == nil && len(channels) == 1 {
				d.Channels = channels
			}
		}
		note = b.texts.T(loc, "bot.wz.copy_intro", map[string]any{"Name": Esc(d.Event.Name)})
	}
	if len(notes) > 0 {
		note += "\n\n⚠ " + strings.Join(notes, "\n⚠ ")
	}
	if _, err := b.storeDraft(ctx, from.ID, ws, d); err != nil {
		b.wizardFail(ctx, chatID, editMsgID, loc, err)
		return
	}
	b.wizardRender(ctx, chatID, editMsgID, ws, d, note)
}

// wizardStart handles the "+ Event" button: resume an unfinished draft or
// start a fresh one.
func (b *Bot) wizardStart(ctx context.Context, chatID int64, editMsgID *int, from *models.User, force bool) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	ws := b.wizardSession(id, jwt)
	loc := ws.Locale
	if !force {
		d, _, err := b.loadDraft(ctx, from.ID, ws)
		if err != nil {
			b.wizardFail(ctx, chatID, editMsgID, loc, err)
			return
		}
		if d != nil && d.Step != stDone {
			name := d.Event.Name
			if name == "" {
				name = "…"
			}
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.resume", map[string]any{"Name": Esc(name)}),
				&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
					{Text: b.texts.T(loc, "bot.wz.resume_btn", nil), CallbackData: "wz:resume"},
					{Text: b.texts.T(loc, "bot.wz.restart_btn", nil), CallbackData: "wz:restart"},
				}}})
			return
		}
	}
	d := NewDraft()
	if _, err := b.storeDraft(ctx, from.ID, ws, d); err != nil {
		b.wizardFail(ctx, chatID, editMsgID, loc, err)
		return
	}
	b.wizardRender(ctx, chatID, editMsgID, ws, d, "")
}

// wizardCallback handles every "wz:<data>" button.
func (b *Bot) wizardCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	switch {
	case data == "new":
		b.wizardStart(ctx, chatID, &msgID, from, false)
		return
	case data == "restart":
		b.wizardStart(ctx, chatID, &msgID, from, true)
		return
	case strings.HasPrefix(data, "edit:") || strings.HasPrefix(data, "copy:"):
		if eventID, err := uuid.Parse(data[len("edit:"):]); err == nil {
			b.wizardOpenEvent(ctx, chatID, &msgID, from, eventID, strings.HasPrefix(data, "copy:"))
			return
		}
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
	ws := b.wizardSession(id, jwt)
	d, row, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil {
		b.wizardFail(ctx, chatID, &msgID, ws.Locale, err)
		return
	}
	if d == nil {
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}
	switch data {
	case "resume":
		b.wizardRender(ctx, chatID, &msgID, ws, d, "")
	case "publish", "retry":
		b.wizardSave(ctx, chatID, &msgID, from, ws, d, row.ID.String(), false)
	case "publish:force":
		b.wizardSave(ctx, chatID, &msgID, from, ws, d, row.ID.String(), true)
	default:
		b.wizardApply(ctx, chatID, &msgID, from, ws, d, WizInput{Data: data})
	}
}

// wizardText routes a typed message to an open draft. It reports whether
// a draft consumed the text.
func (b *Bot) wizardText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil {
		return false
	}
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil || d == nil || d.Step == stDone || d.Step == stSummary {
		return false
	}
	b.wizardApply(ctx, chatID, nil, from, ws, d, WizInput{Text: text})
	return true
}

// wizardPoster routes a document or photo to an open draft waiting for the
// poster. It reports whether a draft consumed the message.
func (b *Bot) wizardPoster(ctx context.Context, chatID int64, from *models.User, m *models.Message) bool {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil {
		return false
	}
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, from.ID, ws)
	if err != nil || d == nil || d.Step != stEvPoster {
		return false
	}
	loc := ws.Locale
	// A document keeps the file as it was; a photo is what Telegram made of
	// it (JPEG, longest side 1280) — the largest size it offers is taken.
	var fileID, fileName string
	var fileSize int64
	switch {
	case m.Document != nil:
		fileID, fileName, fileSize = m.Document.FileID, m.Document.FileName, m.Document.FileSize
	case len(m.Photo) > 0:
		largest := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.Width > largest.Width {
				largest = p
			}
		}
		fileID, fileName, fileSize = largest.FileID, "poster.jpg", int64(largest.FileSize)
	default:
		return false
	}
	if fileSize > posterMaxBytes {
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.poster_too_big", nil), nil)
		return true
	}
	data, err := b.downloadTelegramFile(ctx, fileID)
	if err != nil {
		b.logger.Warn("eventbot: poster download failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.poster_failed", nil), nil)
		return true
	}
	if len(data) > posterMaxBytes {
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.poster_too_big", nil), nil)
		return true
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.poster_bad_type", nil), nil)
		return true
	}
	if key := PosterCheck(cfg.Width, cfg.Height); key != "" {
		b.send(ctx, chatID, b.texts.T(loc, key, map[string]any{"W": cfg.Width, "H": cfg.Height}), nil)
		return true
	}
	contentType := "image/png"
	if format == "jpeg" {
		contentType = "image/jpeg"
	}
	name := fileName
	if name == "" {
		name = "poster." + format
	}
	up, err := b.arena.UploadPoster(ctx, jwt, ws.OrgID, name, contentType, data)
	if err != nil {
		b.logger.Warn("eventbot: poster upload failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.poster_failed", nil), nil)
		return true
	}
	b.wizardApply(ctx, chatID, nil, from, ws, d, WizInput{Poster: &PosterAccepted{MediaID: up.ID, W: cfg.Width, H: cfg.Height}})
	return true
}

// PosterCheck applies the site's poster rules (4:5 or square within 2 %,
// width ≥ 1080) and returns the message key of the first broken rule.
func PosterCheck(w, h int) string {
	if w <= 0 || h <= 0 {
		return "bot.wz.poster_bad_type"
	}
	ratio := float64(w) / float64(h)
	ok := func(want float64) bool { return ratio > want*(1-posterTolerant) && ratio < want*(1+posterTolerant) }
	if !ok(0.8) && !ok(1.0) {
		return "bot.wz.poster_bad_ratio"
	}
	if w < posterMinWidth {
		return "bot.wz.poster_too_small"
	}
	return ""
}

func (b *Bot) downloadTelegramFile(ctx context.Context, fileID string) ([]byte, error) {
	f, err := b.tg.GetFile(ctx, &tgbot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.tg.FileDownloadLink(f), nil)
	if err != nil {
		return nil, err
	}
	client := b.fileClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("telegram file download: " + res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, posterMaxBytes+1))
}

// wizardApply feeds one answer, stores the draft and shows the next screen.
func (b *Bot) wizardApply(ctx context.Context, chatID int64, editMsgID *int, from *models.User, ws WizSession, d *Draft, in WizInput) {
	loc := ws.Locale
	note, err := b.wizard.Apply(ctx, ws, d, in)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			_ = b.queries.DeleteBotDraft(ctx, from.ID, ws.OrgID)
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.cancelled", nil), nil)
			b.showHome(ctx, chatID, nil, from, "")
			return
		}
		b.wizardFail(ctx, chatID, editMsgID, loc, err)
		return
	}
	if _, err := b.storeDraft(ctx, from.ID, ws, d); err != nil {
		b.wizardFail(ctx, chatID, editMsgID, loc, err)
		return
	}
	b.wizardRender(ctx, chatID, editMsgID, ws, d, note)
}

func (b *Bot) wizardRender(ctx context.Context, chatID int64, editMsgID *int, ws WizSession, d *Draft, note string) {
	screen, err := b.wizard.Render(ctx, ws, d)
	if err != nil {
		b.wizardFail(ctx, chatID, editMsgID, ws.Locale, err)
		return
	}
	text := screen.Text
	if note != "" {
		text = note + "\n\n" + text
	}
	b.reply(ctx, chatID, editMsgID, text, wizardKeyboard(screen))
}

func wizardKeyboard(s Screen) *models.InlineKeyboardMarkup {
	if len(s.Buttons) == 0 {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(s.Buttons))
	for _, r := range s.Buttons {
		row := make([]models.InlineKeyboardButton, 0, len(r))
		for _, btn := range r {
			row = append(row, models.InlineKeyboardButton{Text: btn.Label, CallbackData: "wz:" + btn.Data})
		}
		rows = append(rows, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// wizardSave runs the save and reports the outcome. For an edited event it
// first checks that nobody changed the event elsewhere since it was loaded;
// force skips that check ("save anyway").
func (b *Bot) wizardSave(ctx context.Context, chatID int64, editMsgID *int, from *models.User, ws WizSession, d *Draft, draftID string, force bool) {
	loc := ws.Locale
	if d.Mode == ModeEdit && !force && d.Event.UpdatedAt != "" {
		if eventID, err := uuid.Parse(d.Event.EventID); err == nil {
			ev, err := b.arena.GetEvent(ctx, ws.JWT, eventID)
			if err == nil && ev.UpdatedAt.UTC().Format(time.RFC3339Nano) != d.Event.UpdatedAt {
				b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.edit_changed_meanwhile", map[string]any{"Name": Esc(d.Event.Name)}),
					&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
						{Text: b.texts.T(loc, "bot.wz.force_btn", nil), CallbackData: "wz:publish:force"},
						{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "wz:cancel"},
					}}})
				return
			}
		}
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.wz.saving", nil), nil)
	out := b.wizard.Save(ctx, b.arena, ws, d, draftID)
	if !out.Done {
		_, _ = b.storeDraft(ctx, from.ID, ws, d)
		when := ""
		if out.FailedIdx < len(d.Sessions) {
			s := d.Sessions[out.FailedIdx]
			when = DisplayDate(s.Date) + " " + s.Time
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.save_failed", map[string]any{"When": when, "Reason": out.Reason}),
			&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
				{Text: b.texts.T(loc, "bot.wz.retry_btn", nil), CallbackData: "wz:retry"},
				{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "wz:cancel"},
			}}})
		return
	}
	_ = b.queries.DeleteBotDraft(ctx, from.ID, ws.OrgID)
	if raw, err := json.Marshal(RememberDefaults(ws.Defaults, d)); err == nil {
		_ = b.queries.UpdateBotTelegramLinkDefaults(ctx, from.ID, raw)
	}
	link := b.texts.T(loc, "bot.wz.link_none", nil)
	if url := b.salesLink(ctx, ws, d); url != "" {
		link = b.texts.T(loc, "bot.wz.link_line", map[string]any{"URL": url})
	}
	published := ""
	if d.Publish {
		published = b.texts.T(loc, "bot.wz.saved_published", nil)
	}
	warnings := ""
	if len(out.Warnings) > 0 {
		warnings = "\n\n⚠ " + strings.Join(out.Warnings, "\n⚠ ")
	}
	if d.Mode == ModeEdit {
		b.send(ctx, chatID, b.texts.T(loc, "bot.wz.edit_saved", map[string]any{"Name": Esc(d.Event.Name), "Warnings": warnings}), nil)
		if eventID, err := uuid.Parse(d.Event.EventID); err == nil {
			b.showEvent(ctx, chatID, nil, from, eventID, 1)
			return
		}
		b.showHome(ctx, chatID, nil, from, "")
		return
	}
	b.send(ctx, chatID, b.texts.T(loc, "bot.wz.saved", map[string]any{
		"Name": Esc(d.Event.Name), "Published": published, "Link": link, "Warnings": warnings,
	}), nil)
	// A published event comes with the buyer's e-ticket as a PDF — the
	// first date's sample, stamped SAMPLE, with a real code the gate
	// recognises (migration 0119).
	if d.Publish && len(d.Saved.Sessions) > 0 {
		if sessionID, err := uuid.Parse(d.Saved.Sessions[0].SessionID); err == nil {
			b.sendSampleTicket(ctx, chatID, ws.JWT, ws.OrgID, sessionID, loc, d.Event.Name)
		}
	}
	b.showHome(ctx, chatID, nil, from, "")
}

// salesLink is the storefront page of the event: <tickets base>/<promoter
// slug>/<event slug> when the event has a promoter with a page (owner
// decision 2026-09-29: the link is the promoter's, the organization's slug
// is a technical detail), otherwise <tickets base>/<org slug>/<event slug>;
// just the landing page when the event slug is not known yet.
func (b *Bot) salesLink(ctx context.Context, ws WizSession, d *Draft) string {
	if b.ticketsBaseURL == "" || !d.Publish {
		return ""
	}
	pageSlug := ""
	if d.Event.PromoterID != "" {
		if promoters, err := b.arena.Promoters(ctx, ws.JWT, ws.OrgID); err == nil {
			for _, p := range promoters {
				if p.ID == d.Event.PromoterID && p.Slug != "" {
					pageSlug = p.Slug
				}
			}
		}
	}
	if pageSlug == "" {
		orgSlug, err := b.arena.OrganizationSlug(ctx, ws.JWT, ws.OrgID)
		if err != nil || orgSlug == "" {
			return ""
		}
		pageSlug = orgSlug
	}
	base := strings.TrimRight(b.ticketsBaseURL, "/") + "/" + pageSlug
	if d.Saved.EventID == "" {
		return base
	}
	events, err := b.arena.ListEvents(ctx, ws.JWT, ws.OrgID)
	if err == nil {
		for _, e := range events {
			if e.Id.String() == d.Saved.EventID && e.Slug != nil && *e.Slug != "" {
				return base + "/" + *e.Slug
			}
		}
	}
	return base
}

func (b *Bot) wizardFail(ctx context.Context, chatID int64, editMsgID *int, loc string, err error) {
	b.logger.Error("eventbot: wizard failed", slog.String("error", err.Error()))
	key := "bot.error_generic"
	if IsAPIError(err, http.StatusForbidden) {
		key = "bot.wz.err_forbidden"
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, "home"))
}
