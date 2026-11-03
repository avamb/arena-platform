package eventbot

// guest_list.go — the guest list of a session (owner decision 2026-11-02,
// spec 51 section 4.4, migration 0187).
//
// Three ways in:
//   - "👥 Guest list" on a date's card (ses:gl): the bot downloads
//     GET .../sessions/{id}/guest-list.csv as the person and sends it as a
//     document;
//   - the toggle on the Notifications screen (gl:on / gl:off, the WANTED value
//     so an old button never flips it back): PUT .../guest-list-subscription;
//   - the loop that claims the automatic lists (24 h, 3 h before the start
//     and at doors open) from POST /v1/bot/guest-list-deliveries/claim,
//     fetches each as the person it is for and reports how it ended.
//
// The file holds buyers' names, e-mails and phones: it goes to the person's
// PRIVATE chat only, is never written to disk and never logged.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// guestListEvery is how often the bot asks for automatic lists to send.
const guestListEvery = 30 * time.Second

// GuestListDelivery is an automatic guest list the bot has to send.
type GuestListDelivery struct {
	ID              uuid.UUID `json:"id"`
	OrgID           uuid.UUID `json:"org_id"`
	SessionID       uuid.UUID `json:"session_id"`
	Slot            string    `json:"slot"`
	Attempt         int32     `json:"attempt"`
	TelegramUserID  int64     `json:"telegram_user_id"`
	EventName       string    `json:"event_name"`
	SessionStartAt  time.Time `json:"session_start_at"`
	SessionTimezone string    `json:"session_timezone"`
}

// GuestListSubscription is one member's automatic guest list setting.
type GuestListSubscription struct {
	Enabled  bool   `json:"enabled"`
	Channel  string `json:"channel"`
	Eligible bool   `json:"eligible"`
}

// Outcomes reported back for a delivery.
const (
	guestListSent    = "sent"
	guestListFailed  = "failed"
	guestListSkipped = "skipped"
	guestListToEmail = "email"
)

// guestListFinishRetries is how often a lost report is tried again before the
// bot gives up on it; a report never made lets the row be taken again after
// its 10-minute lease, so the person would get the same list twice.
var guestListFinishRetries = []time.Duration{time.Second, 3 * time.Second, 10 * time.Second}

// SessionGuestListCSV downloads the guest list of a session.
func (c *ArenaClient) SessionGuestListCSV(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, locale string) (CSVFile, error) {
	return c.DownloadCSV(ctx, jwt, "/v1/organizations/"+orgID.String()+"/sessions/"+sessionID.String()+"/guest-list.csv", locale)
}

// GuestListSubscription reads the person's automatic guest list setting.
func (c *ArenaClient) GuestListSubscription(ctx context.Context, jwt string, orgID uuid.UUID) (GuestListSubscription, error) {
	var out GuestListSubscription
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/guest-list-subscription", jwt, nil, &out)
	return out, err
}

// SetGuestListSubscription turns the automatic guest list on or off.
func (c *ArenaClient) SetGuestListSubscription(ctx context.Context, jwt string, orgID uuid.UUID, enabled bool) (GuestListSubscription, error) {
	var out GuestListSubscription
	err := c.do(ctx, http.MethodPut, "/v1/organizations/"+orgID.String()+"/guest-list-subscription", jwt,
		map[string]any{"enabled": enabled}, &out)
	return out, err
}

// ClaimGuestListDeliveries takes the automatic lists due now; each is taken
// for 10 minutes and must be reported back with FinishGuestListDelivery.
func (c *ArenaClient) ClaimGuestListDeliveries(ctx context.Context) ([]GuestListDelivery, error) {
	var out struct {
		Deliveries []GuestListDelivery `json:"deliveries"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/guest-list-deliveries/claim", c.serviceToken, struct{}{}, &out)
	return out.Deliveries, err
}

// FinishGuestListDelivery reports how a taken delivery ended. attempt is the
// claim's token (GuestListDelivery.Attempt). reason is a short code, never
// data from the file.
func (c *ArenaClient) FinishGuestListDelivery(ctx context.Context, id uuid.UUID, attempt int32, outcome, reason string) error {
	body := map[string]any{"attempt": attempt, "outcome": outcome}
	if reason != "" {
		body["reason"] = reason
	}
	return c.do(ctx, http.MethodPost, "/v1/bot/guest-list-deliveries/"+id.String()+"/finish", c.serviceToken, body, nil)
}

// sendGuestList answers "👥 Guest list" on a date's card.
func (b *Bot) sendGuestList(ctx context.Context, chatID int64, from *models.User, sessionID uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, nil, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), nil)
		return
	}
	// Buyers' contacts never go into a group.
	if chatID != from.ID {
		b.send(ctx, chatID, b.texts.T(loc, "bot.gl.private_only", nil), nil)
		return
	}
	sum, err := b.arena.SessionSummary(ctx, jwt, id.Current.OrgID, sessionID)
	if err != nil {
		b.ecError(ctx, chatID, nil, id, err, "el:b")
		return
	}
	tz := ""
	if sum.Session.VenueTimezone != nil {
		tz = *sum.Session.VenueTimezone
	}
	file, err := b.arena.SessionGuestListCSV(ctx, jwt, id.Current.OrgID, sessionID, loc)
	if err != nil {
		b.csvFailed(ctx, chatID, loc, csvSessionSales, err)
		return
	}
	if file.Rows == 0 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.gl.empty", nil), nil)
		return
	}
	caption := b.texts.T(loc, "bot.gl.caption", map[string]any{
		"Name": Esc(truncate(sum.Session.EventName, 120)), "When": Esc(FormatWhen(sum.Session.StartAt, tz)), "Rows": file.Rows,
	}) + "\n\n" + b.texts.T(loc, "bot.gl.auto_hint", nil)
	if err := b.sendGuestListDocument(ctx, chatID, file, caption); err != nil {
		b.logger.Warn("eventbot: send guest list failed", slog.Int("bytes", len(file.Body)), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_failed", nil), nil)
	}
}

func (b *Bot) sendGuestListDocument(ctx context.Context, chatID int64, file CSVFile, caption string) error {
	name := file.Name
	if name == "" {
		name = "guests.csv"
	}
	_, err := b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:    chatID,
		Document:  &models.InputFileUpload{Filename: name, Data: bytes.NewReader(file.Body)},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	})
	return err
}

// guestListRow is the Notifications screen's toggle, or nil when the person
// cannot read sales or the setting could not be read.
func (b *Bot) guestListRow(ctx context.Context, chatID int64, jwt string, id *Identity) (string, []models.InlineKeyboardButton) {
	if id == nil || id.Current == nil || !canViewSales(id) || id.Superadmin || chatID <= 0 {
		return "", nil
	}
	loc := id.Locale()
	sub, err := b.arena.GuestListSubscription(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.logger.Warn("eventbot: guest list subscription read failed", slog.String("error", err.Error()))
		return "", nil
	}
	return guestListScreen(b, loc, sub)
}

// guestListScreen is the text and the toggle for a known setting.
func guestListScreen(b *Bot, loc string, sub GuestListSubscription) (string, []models.InlineKeyboardButton) {
	text := b.texts.T(loc, "bot.gl.notif_text", nil)
	key, data := "bot.gl.toggle_off", "gl:on"
	if sub.Enabled {
		key, data = "bot.gl.toggle_on", "gl:off"
	}
	return text, []models.InlineKeyboardButton{{Text: b.texts.T(loc, key, nil), CallbackData: data}}
}

// guestListCallback handles gl:on and gl:off from the Notifications screen.
func (b *Bot) guestListCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) || id.Superadmin {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), nil)
		return
	}
	if chatID != from.ID {
		b.send(ctx, chatID, b.texts.T(loc, "bot.gl.private_only", nil), nil)
		return
	}
	want := data == "on"
	if data != "on" && data != "off" {
		return
	}
	if _, err := b.arena.SetGuestListSubscription(ctx, jwt, id.Current.OrgID, want); err != nil {
		b.logger.Warn("eventbot: guest list subscription change failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.gl.toggle_failed", nil), nil)
		return
	}
	b.showNotifications(ctx, chatID, &msgID, id, jwt)
}

// guestListLoop sends the automatic guest lists into private chats.
func (b *Bot) guestListLoop(ctx context.Context) {
	every := b.noticeEvery
	if every <= 0 {
		every = guestListEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.guestListAnnounce(ctx)
		}
	}
}

func (b *Bot) guestListAnnounce(ctx context.Context) {
	ds, err := b.arena.ClaimGuestListDeliveries(ctx)
	if err != nil {
		b.logger.Warn("eventbot: guest list deliveries failed", slog.String("error", err.Error()))
		return
	}
	for _, d := range ds {
		outcome, reason := b.deliverGuestList(ctx, d)
		b.finishGuestList(ctx, d, outcome, reason)
	}
}

// finishGuestList reports one delivery, trying again a few times when the API
// does not answer. A 404 means the row was taken again meanwhile or closed: a
// retry cannot help.
func (b *Bot) finishGuestList(ctx context.Context, d GuestListDelivery, outcome, reason string) {
	var err error
	for i := 0; ; i++ {
		err = b.arena.FinishGuestListDelivery(ctx, d.ID, d.Attempt, outcome, reason)
		if err == nil || IsAPIError(err, http.StatusNotFound) || IsAPIError(err, http.StatusBadRequest) ||
			i >= len(guestListFinishRetries) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(guestListFinishRetries[i]):
		}
	}
	if err != nil {
		b.logger.Warn("eventbot: guest list finish failed", slog.String("delivery_id", d.ID.String()), slog.String("error", err.Error()))
	}
}

// deliverGuestList fetches one automatic list as the person it is for and
// sends it to their private chat. The API decides whether they may still
// read it: a refusal closes the delivery without a send.
func (b *Bot) deliverGuestList(ctx context.Context, d GuestListDelivery) (outcome, reason string) {
	link, err := b.queries.GetBotTelegramLink(ctx, d.TelegramUserID)
	if err != nil || link.RevokedAt != nil {
		return guestListSkipped, "not_linked"
	}
	jwt, err := b.minter.Mint(link.UserID)
	if err != nil {
		return guestListFailed, "mint_failed"
	}
	loc := NormalizeLocale(link.Locale)
	file, err := b.arena.SessionGuestListCSV(ctx, jwt, d.OrgID, d.SessionID, loc)
	switch {
	case IsAPIError(err, http.StatusForbidden), IsAPIError(err, http.StatusNotFound):
		return guestListSkipped, "no_access"
	case IsAPIError(err, http.StatusRequestEntityTooLarge):
		return guestListSkipped, "too_many_rows"
	case err != nil:
		b.logger.Warn("eventbot: guest list fetch failed", slog.String("delivery_id", d.ID.String()), slog.String("error", err.Error()))
		return guestListFailed, "fetch_failed"
	case file.Rows == 0:
		return guestListSkipped, "no_tickets"
	}
	caption := b.texts.T(loc, "bot.gl.auto_caption_"+guestListSlot(d.Slot), map[string]any{
		"Name": Esc(truncate(d.EventName, 120)), "When": Esc(FormatWhen(d.SessionStartAt, d.SessionTimezone)), "Rows": file.Rows,
	}) + "\n\n" + b.texts.T(loc, "bot.gl.auto_stop", nil)
	if err := b.sendGuestListDocument(ctx, d.TelegramUserID, file, caption); err != nil {
		b.logger.Warn("eventbot: guest list send failed", slog.String("delivery_id", d.ID.String()), slog.String("error", err.Error()))
		if errors.Is(err, tgbot.ErrorForbidden) {
			// The person blocked the bot: the list goes by e-mail.
			return guestListToEmail, "bot_blocked"
		}
		return guestListFailed, "send_failed"
	}
	return guestListSent, ""
}

// guestListSlot keeps the caption key to the three known moments.
func guestListSlot(slot string) string {
	switch slot {
	case "h24", "h3", "doors":
		return slot
	}
	return "doors"
}
