package eventbot

// sample.go — the organizer's preview of a buyer's e-ticket (migration
// 0119, GET .../sessions/{id}/sample-ticket). The bot sends it as a PDF
// document right after a publish, and on the "Sample ticket" button of an
// event's card. The barcode on it is real: a scanner at the gate answers
// "sample ticket" and admits nobody, so the organizer can try the whole
// chain before the first sale.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// sampleMaxBytes bounds the PDF the bot forwards; a real sample is ~50 KB
// plus the artwork.
const sampleMaxBytes = 20 << 20

// SampleTicketPDF fetches the session's sample e-ticket in the given
// language; the body is the PDF bytes.
func (c *ArenaClient) SampleTicketPDF(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, locale string) ([]byte, error) {
	path := "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/sample-ticket"
	if locale != "" {
		path += "?locale=" + locale
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/pdf")
	req.Header.Set("X-Admin-Reason", AdminReason)
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET sample ticket: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, sampleMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read sample ticket: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		ae := &APIError{Status: res.StatusCode, Code: "http." + http.StatusText(res.StatusCode)}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if decodeJSON(raw, &env) == nil && env.Error.Code != "" {
			ae.Code, ae.Message = env.Error.Code, env.Error.Message
		}
		return nil, ae
	}
	if len(raw) > sampleMaxBytes || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return nil, fmt.Errorf("sample ticket: not a PDF (%d bytes)", len(raw))
	}
	return raw, nil
}

// sendSampleTicket fetches the sample of one session and posts it to the
// chat as a document with a caption naming the event. A failure is told in
// one line and logged; it never blocks whatever the caller does next.
func (b *Bot) sendSampleTicket(ctx context.Context, chatID int64, jwt string, orgID, sessionID uuid.UUID, loc, eventName string) {
	pdfBytes, err := b.arena.SampleTicketPDF(ctx, jwt, orgID, sessionID, loc)
	if err != nil {
		b.logger.Warn("eventbot: sample ticket failed",
			slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.sample_failed", nil), nil)
		return
	}
	_, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:    chatID,
		Document:  &models.InputFileUpload{Filename: "sample-ticket.pdf", Data: bytes.NewReader(pdfBytes)},
		Caption:   b.texts.T(loc, "bot.sample_caption", map[string]any{"Name": Esc(eventName)}),
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		b.logger.Warn("eventbot: send sample document failed",
			slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.sample_failed", nil), nil)
	}
}

// sampleCallback answers the event card's "Sample ticket" button: the
// sample of the event's first date.
func (b *Bot) sampleCallback(ctx context.Context, chatID int64, from *models.User, eventIDRaw string) {
	eventID, err := uuid.Parse(eventIDRaw)
	if err != nil {
		return
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, nil, id)
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	name := ""
	if events, err := b.arena.ListEvents(ctx, jwt, orgID); err == nil {
		for _, e := range events {
			if e.Id == eventID {
				name = e.Name
			}
		}
	}
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.replyAPIError(ctx, chatID, nil, id, err)
		return
	}
	if len(sessions) == 0 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.event_no_sessions", nil), nil)
		return
	}
	b.sendSampleTicket(ctx, chatID, jwt, orgID, sessions[0].Id, loc, name)
}
