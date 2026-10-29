package eventbot

// invitation_pdf.go — a staff invitation issued from Telegram reaches the
// issuer's chat as a PDF document (spec 46, INV-09; owner decision
// 2026-10-29: this is the way a Telegram user gets the file, Telegram's
// WebApp.downloadFile is not used and no download link is ever made).
//
// The loop asks arena-api for the invitations issued from the bot or the Mini
// App whose tickets are all issued (POST /v1/bot/invitations/pdf-notices/claim,
// each comes once, like the onboarding decisions), fetches tickets.pdf as the
// issuer and sends it with sendDocument, named invitation-<number>.pdf, with a
// caption in the issuer's bot language. A failed fetch or send is logged and
// not retried: the file stays one tap away in the event center.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// invPDFEvery is how often the bot asks for invitations to send.
const invPDFEvery = 20 * time.Second

// InvitationPDFNotice is an invitation whose PDF the bot has to send.
type InvitationPDFNotice struct {
	OrderID         uuid.UUID `json:"order_id"`
	OrgID           uuid.UUID `json:"org_id"`
	SystemID        int64     `json:"system_id"`
	TelegramUserID  int64     `json:"telegram_user_id"`
	Locale          string    `json:"locale"`
	GuestName       string    `json:"guest_name"`
	EventName       string    `json:"event_name"`
	SessionStartAt  time.Time `json:"session_start_at"`
	SessionTimezone string    `json:"session_timezone"`
}

// ClaimInvitationPDFNotices takes the invitations whose PDF is not sent yet;
// each is handed out once.
func (c *ArenaClient) ClaimInvitationPDFNotices(ctx context.Context) ([]InvitationPDFNotice, error) {
	var out struct {
		Notices []InvitationPDFNotice `json:"notices"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/invitations/pdf-notices/claim", c.serviceToken, struct{}{}, &out)
	return out.Notices, err
}

// InvitationPDF fetches the one PDF with a page per active ticket of the
// invitation, as the user who issued it.
func (c *ArenaClient) InvitationPDF(ctx context.Context, jwt string, orgID, orderID uuid.UUID) ([]byte, error) {
	path := "/v1/organizations/" + orgID.String() + "/invitations/" + orderID.String() + "/tickets.pdf"
	return c.getPDF(ctx, jwt, path, "invitation pdf")
}

// invPDFLoop sends the PDF of every invitation issued from Telegram into the
// issuer's chat, once.
func (b *Bot) invPDFLoop(ctx context.Context) {
	every := b.noticeEvery
	if every <= 0 {
		every = invPDFEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.invPDFAnnounce(ctx)
		}
	}
}

func (b *Bot) invPDFAnnounce(ctx context.Context) {
	notices, err := b.arena.ClaimInvitationPDFNotices(ctx)
	if err != nil {
		b.logger.Warn("eventbot: invitation pdf notices failed", slog.String("error", err.Error()))
		return
	}
	for _, n := range notices {
		b.sendInvitationPDF(ctx, n)
	}
}

// sendInvitationPDF fetches and sends one invitation. It is called once per
// invitation (the claim already marked it), so every failure is only logged.
func (b *Bot) sendInvitationPDF(ctx context.Context, n InvitationPDFNotice) {
	link, err := b.queries.GetBotTelegramLink(ctx, n.TelegramUserID)
	if err != nil || link.RevokedAt != nil {
		return
	}
	jwt, err := b.minter.Mint(link.UserID)
	if err != nil {
		b.logger.Warn("eventbot: invitation pdf: mint failed", slog.String("order_id", n.OrderID.String()), slog.String("error", err.Error()))
		return
	}
	raw, err := b.arena.InvitationPDF(ctx, jwt, n.OrgID, n.OrderID)
	if err != nil {
		b.logger.Warn("eventbot: invitation pdf fetch failed", slog.String("order_id", n.OrderID.String()), slog.String("error", err.Error()))
		return
	}
	loc := NormalizeLocale(link.Locale)
	_, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:    n.TelegramUserID,
		Document:  &models.InputFileUpload{Filename: fmt.Sprintf("invitation-%d.pdf", n.SystemID), Data: bytes.NewReader(raw)},
		Caption:   b.texts.T(loc, "bot.inv.pdf_caption", invPDFCaptionData(n)),
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		// A person who blocked the bot (403) is not an error worth more than
		// a log line: the file is in the event center.
		b.logger.Warn("eventbot: invitation pdf send failed", slog.String("order_id", n.OrderID.String()), slog.String("error", err.Error()))
	}
}

// invPDFCaptionData is the template data of the caption. The guest's name is
// optional on the wire; without it the caption names the number only.
func invPDFCaptionData(n InvitationPDFNotice) map[string]any {
	guest := ""
	if n.GuestName != "" {
		guest = Esc(n.GuestName)
	}
	return map[string]any{
		"Num":   n.SystemID,
		"Name":  guest,
		"Title": Esc(n.EventName),
		"When":  Esc(FormatWhen(n.SessionStartAt, n.SessionTimezone)),
	}
}
