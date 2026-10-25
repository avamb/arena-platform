// event_note_sample.go - the event.email_note_test worker job: mail the
// organizer a sample ticket letter that carries the event's note (EC-31).
//
// The letter is a preview, not a buyer letter. It goes straight through the
// sender: no email_letters row (that table is the evidence of what buyers
// received), no ticket, no PDF, no delivery job.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// EventNoteTestDB is what the handler reads.
type EventNoteTestDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EventNoteTestOptions are the dependencies of the job.
type EventNoteTestOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        EventNoteTestDB
	Logger    *slog.Logger
}

// testSubjectPrefix marks the preview in the inbox, in every language.
const testSubjectPrefix = "[Test] "

// NewEventNoteTestHandler builds the worker handler of eventnote.JobTypeTestEmail.
func NewEventNoteTestHandler(opts EventNoteTestOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: event-note test templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	return func(ctx context.Context, payload []byte) error {
		var p eventnote.TestPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil // a payload nobody can read is never going to work
		}
		orgID, err1 := uuid.Parse(p.OrgID)
		eventID, err2 := uuid.Parse(p.EventID)
		if err1 != nil || err2 != nil || p.To == "" {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: event-note test letter skipped, no production e-mail sender configured")
			return nil
		}
		var eventName, orgName, legalName string
		err := opts.DB.QueryRow(ctx, `SELECT e.name, o.name, COALESCE(NULLIF(btrim(o.legal_name), ''), o.name)
			FROM events e JOIN organizations o ON o.id = e.org_id
			WHERE e.id = $1 AND e.org_id = $2 AND e.deleted_at IS NULL`, eventID, orgID).Scan(&eventName, &orgName, &legalName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the event is gone or not this organization's
		}
		if err != nil {
			return err
		}
		out, err := renderer.Render(templates.TemplateKindTicket, p.Locale, templates.Data{
			TicketID:       "1000",
			RecipientEmail: p.To,
			EventName:      eventName,
			Branding:       templates.Branding{OrgName: orgName, LegalName: legalName},
			Tickets:        []templates.TicketLine{{Number: "1000"}},
			EventNote:      eventnote.Build(p.Note, p.NoteFormat),
		})
		if err != nil {
			return err
		}
		if err := opts.Sender.Send(ctx, email.Message{
			To:       p.To,
			Subject:  testSubjectPrefix + out.Subject,
			HTMLBody: out.HTMLBody,
			TextBody: out.TextBody,
		}); err != nil {
			logger.Warn("delivery: event-note test letter not sent", slog.String("event_id", eventID.String()))
			return err
		}
		return nil
	}
}
