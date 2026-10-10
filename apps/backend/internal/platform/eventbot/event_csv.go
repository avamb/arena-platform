package eventbot

// event_csv.go — "Export CSV" (EC-07, spec 35 §5.8, §4.3): the bot downloads
// an export from arena-api (the server writes the file: BOM, `;`, barcodes as
// text) and hands the bytes to Telegram as a document with a caption saying
// what is in it. The file holds buyers' names, e-mails and phones, so it goes
// only to the private chat of the person who pressed the button, is never
// written to disk and never logged; a log line names the route kind and the
// size, nothing from inside the file.

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

// The export buttons' callback kinds ("ec:<kind>:<uuid>").
const (
	csvSessionSales   = "cs"
	csvSessionSummary = "cm"
	csvEventSales     = "ce"
)

// sendCSV downloads one export and sends it as a document. kind is one of the
// csv* constants and target the session or the event it is about. Every
// failure is told in one line; nothing here changes the screen the button
// is on.
func (b *Bot) sendCSV(ctx context.Context, chatID int64, from *models.User, kind string, target uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, nil, from)
	if !ok {
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	if !canViewSales(id) {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), nil)
		return
	}

	// What the caption names: the event and the date (or "all dates").
	var name, scope string
	if kind == csvEventSales {
		sum, err := b.arena.EventSummary(ctx, jwt, orgID, target)
		if err != nil {
			b.ecError(ctx, chatID, nil, id, err, "el:b")
			return
		}
		name, scope = sum.Event.Name, b.texts.T(loc, "bot.ec.scope_all", map[string]any{"N": len(sum.Sessions)})
	} else {
		sum, err := b.arena.SessionSummary(ctx, jwt, orgID, target)
		if err != nil {
			b.ecError(ctx, chatID, nil, id, err, "el:b")
			return
		}
		tz := ""
		if sum.Session.VenueTimezone != nil {
			tz = *sum.Session.VenueTimezone
		}
		name, scope = sum.Session.EventName, Esc(FormatWhen(sum.Session.StartAt, tz))
	}

	var file CSVFile
	var err error
	what := "bot.ec.csv_what_sales"
	switch kind {
	case csvSessionSales:
		file, err = b.arena.SessionSalesCSV(ctx, jwt, orgID, target, loc)
	case csvSessionSummary:
		file, err = b.arena.SessionSummaryCSV(ctx, jwt, orgID, target, loc)
		what = "bot.ec.csv_what_summary"
	case csvEventSales:
		file, err = b.arena.EventSalesCSV(ctx, jwt, orgID, target, loc)
	default:
		return
	}
	if err != nil {
		b.csvFailed(ctx, chatID, loc, kind, err)
		return
	}
	if file.Rows == 0 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_empty", nil), nil)
		return
	}
	// allow:timeformat: the export date in a chat caption, not a wire timestamp
	date := time.Now().UTC().Format("02.01.2006")
	_, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: file.Name, Data: bytes.NewReader(file.Body)},
		Caption: b.texts.T(loc, "bot.ec.csv_caption", map[string]any{
			"Name": Esc(truncate(name, 120)), "Scope": scope, "Rows": file.Rows,
			"What": b.texts.T(loc, what, nil), "Date": date,
		}),
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		b.logger.Warn("eventbot: send export failed", slog.String("kind", kind), slog.Int("bytes", len(file.Body)), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_failed", nil), nil)
	}
}

// csvFailed tells why an export did not arrive. A file over the row cap is
// the one failure the person can act on: narrow it to one date.
func (b *Bot) csvFailed(ctx context.Context, chatID int64, loc, kind string, err error) {
	switch {
	case IsAPIError(err, http.StatusRequestEntityTooLarge) || errors.Is(err, errCSVTooLarge):
		key := "bot.ec.csv_too_big_event"
		if kind != csvEventSales {
			key = "bot.ec.csv_too_big_session"
		}
		b.send(ctx, chatID, b.texts.T(loc, key, nil), nil)
	case IsAPIError(err, http.StatusNotFound):
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.not_found", nil), nil)
	case IsAPIError(err, http.StatusForbidden):
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), nil)
	default:
		b.logger.Warn("eventbot: export failed", slog.String("kind", kind), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_failed", nil), nil)
	}
}
