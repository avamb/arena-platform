// change_email.go — the session.change_email worker job: the letter Arena
// itself writes to a buyer whose session was moved or cancelled
// (08_architecture/30_session_change_notifications_ru.md §5).
//
// sessionchange.Apply queues one job per affected order in the same
// transaction as the move. The handler:
//
//  1. reads the journal row and the notice; a notice already sent (or
//     skipped) is done, so a redelivered job never mails twice;
//  2. for a MOVE re-renders every active ticket's PDF from the session's NEW
//     data and stores it over the old one — the barcode is the ticket's own
//     EAN-13 credential, which never changes, so the old PDF still scans, but
//     the buyer's download page, the admin "resend" and this letter all show
//     the new date;
//  3. sends the letter in the buyer's language with Reply-To set to the
//     organizer's contact; a MOVE carries the fresh PDFs, a CANCELLATION none.
//
// A failed send records `failed` (with a scrubbed, truncated error) and
// returns the error so the worker retries; the next attempt starts from
// `failed` again. Buyer addresses never reach the logs.
package delivery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ChangeEmailOptions are the dependencies of the session.change_email job.
type ChangeEmailOptions struct {
	// HandlerOptions supplies the sender, media resolver, templates and the
	// query set shared with ticket.deliver.
	HandlerOptions
	// DB reads the change journal, the order and the organizer contact.
	DB sessionchange.DB
}

// NewChangeEmailHandler builds the worker handler of sessionchange.JobTypeChangeEmail.
func NewChangeEmailHandler(opts ChangeEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			// Without templates there is nothing honest to send; every job
			// fails loudly and retries rather than mailing a bare stub.
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: change e-mail templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	h := &changeEmail{opts: opts, logger: logger, renderer: renderer}
	return h.handle
}

type changeEmail struct {
	opts     ChangeEmailOptions
	logger   *slog.Logger
	renderer *templates.Renderer
}

func (h *changeEmail) handle(ctx context.Context, payload []byte) error {
	var p sessionchange.ChangeEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		h.logger.Error("delivery: malformed change e-mail payload", slog.String("error", err.Error()))
		return nil // permanent: a retry cannot fix a bad payload
	}
	changeID, err1 := uuid.Parse(p.ChangeID)
	orderID, err2 := uuid.Parse(p.OrderID)
	if err1 != nil || err2 != nil {
		h.logger.Error("delivery: invalid ids in change e-mail payload")
		return nil
	}

	state, err := sessionchange.NoticeState(ctx, h.opts.DB, changeID, orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("delivery: change e-mail without a notice row; dropping",
				slog.String("change_id", changeID.String()))
			return nil
		}
		return fmt.Errorf("delivery: read notice: %w", err)
	}
	if state == sessionchange.StateSent || state == sessionchange.StateSkipped ||
		state == sessionchange.StateDeliveredToSite {
		return nil // idempotent: already told
	}

	rec, err := sessionchange.LoadChange(ctx, h.opts.DB, changeID)
	if err != nil {
		return fmt.Errorf("delivery: read change %s: %w", changeID, err)
	}
	recipient, err := sessionchange.LoadOrderRecipient(ctx, h.opts.DB, orderID)
	if err != nil {
		return fmt.Errorf("delivery: read order %s: %w", orderID, err)
	}
	moves := sessionchange.MovesBuyers(rec.Kinds)

	tickets, err := sessionchange.ActiveTickets(ctx, h.opts.DB, orderID, rec.SessionID)
	if err != nil {
		return err
	}
	if len(tickets) == 0 {
		// Every ticket was cancelled or refunded since the change was saved.
		return h.finish(ctx, changeID, orderID, sessionchange.StateSkipped, "no active ticket left")
	}

	// Fresh PDFs first: they must reach the credential store even when the
	// letter itself cannot be sent (no address), so the download page and the
	// admin resend show the new date.
	var rendered []renderedTicket
	if moves {
		rendered, err = h.renderTickets(ctx, tickets, recipient.LocaleHint)
		if err != nil {
			h.record(ctx, changeID, orderID, sessionchange.StateFailed, err.Error())
			return err
		}
	} else {
		rendered, err = h.describeTickets(ctx, tickets, recipient.LocaleHint)
		if err != nil {
			h.record(ctx, changeID, orderID, sessionchange.StateFailed, err.Error())
			return err
		}
	}

	if recipient.Email == "" {
		return h.finish(ctx, changeID, orderID, sessionchange.StateSkipped, "no address for the buyer")
	}
	if h.opts.Sender == nil || email.IsDevOnly(h.opts.Sender) {
		return h.finish(ctx, changeID, orderID, sessionchange.StateSkipped, "no production e-mail sender configured")
	}

	contact, err := sessionchange.ResolveContact(ctx, h.opts.DB, rec.EventID, rec.OrgID)
	if err != nil {
		h.record(ctx, changeID, orderID, sessionchange.StateFailed, err.Error())
		return err
	}
	if !contact.Complete() {
		// Apply refuses to queue such a change, so this means the contact was
		// removed in between. Do not send a letter nobody can answer.
		h.record(ctx, changeID, orderID, sessionchange.StateFailed, "organizer contact e-mail is missing")
		return errors.New("delivery: organizer contact e-mail is missing")
	}

	msg, err := h.buildMessage(ctx, rec, recipient, rendered, contact, moves)
	if err != nil {
		h.record(ctx, changeID, orderID, sessionchange.StateFailed, err.Error())
		return err
	}
	if sendErr := h.opts.Sender.Send(ctx, msg); sendErr != nil {
		h.record(ctx, changeID, orderID, sessionchange.StateFailed, sendErr.Error())
		h.logger.Warn("delivery: change e-mail send failed; worker will retry",
			slog.String("change_id", changeID.String()),
			slog.String("order_id", orderID.String()))
		return fmt.Errorf("delivery: send change e-mail for order %s: %w", orderID, sendErr)
	}
	h.record(ctx, changeID, orderID, sessionchange.StateSent, "")
	h.logger.Info("delivery: change e-mail sent",
		slog.String("change_id", changeID.String()),
		slog.String("order_id", orderID.String()),
		slog.Int("attachments", len(msg.Attachments)))
	return nil
}

// finish stores a terminal state without an error.
func (h *changeEmail) finish(ctx context.Context, changeID, orderID uuid.UUID, state, reason string) error {
	h.record(ctx, changeID, orderID, state, reason)
	h.logger.Info("delivery: change e-mail not sent",
		slog.String("change_id", changeID.String()),
		slog.String("order_id", orderID.String()),
		slog.String("state", state))
	return nil
}

// record stores a notice state; a failure to store is logged, never fatal.
func (h *changeEmail) record(ctx context.Context, changeID, orderID uuid.UUID, state, reason string) {
	if err := sessionchange.SetNoticeState(ctx, h.opts.DB, changeID, orderID, state, reason); err != nil {
		h.logger.Warn("delivery: could not record the change notice state",
			slog.String("change_id", changeID.String()),
			slog.String("state", state),
			slog.String("error", err.Error()))
	}
}

// renderedTicket is one ticket's resolved payload and (for a move) its fresh PDF.
type renderedTicket struct {
	TicketID uuid.UUID
	Payload  Payload
	PDF      []byte
}

// ticketPayload resolves the full render payload of one ticket from its rows:
// seat, branding, presentation (the session's CURRENT data, which is the new
// date) — exactly what ticket.deliver does, minus the delivery_jobs steps.
func (h *changeEmail) ticketPayload(ctx context.Context, ticketID uuid.UUID, locale string) Payload {
	p := Payload{TicketID: ticketID.String(), Locale: locale}
	q := h.opts.TicketQueries
	if q == nil {
		return p
	}
	if t, err := q.GetTicketByID(ctx, ticketID); err == nil {
		if t.SeatSector != nil {
			p.SeatSector = *t.SeatSector
		}
		if t.SeatRow != nil {
			p.SeatRow = *t.SeatRow
		}
		if t.SeatNumber != nil {
			p.SeatNumber = *t.SeatNumber
		}
	} else {
		h.logger.Warn("delivery: change e-mail ticket lookup failed", slog.String("ticket_id", ticketID.String()))
	}
	if b, err := q.GetOrgBrandingByTicketID(ctx, ticketID); err == nil {
		p.OrgName = b.Name
		setIf := func(dst *string, src *string) {
			if src != nil {
				*dst = *src
			}
		}
		setIf(&p.OrgWebsiteURL, b.WebsiteURL)
		setIf(&p.OrgLegalName, b.LegalName)
		setIf(&p.OrgLegalAddressLine1, b.LegalAddressLine1)
		setIf(&p.OrgLegalAddressLine2, b.LegalAddressLine2)
		setIf(&p.OrgLegalAddressPostal, b.LegalAddressPostalCode)
		setIf(&p.OrgLegalAddressCity, b.LegalAddressCity)
		setIf(&p.OrgLegalAddressCountry, b.LegalAddressCountry)
		setIf(&p.OrgContactEmail, b.ContactEmail)
		if b.LogoMediaID != nil {
			p.OrgLogoMediaID = b.LogoMediaID.String()
		}
		setIf(&p.SenderEmail, b.SenderEmail)
		p.SenderVerificationStatus = b.SenderVerificationStatus
	}
	if senderEmail, status, err := q.GetSenderIdentityByTicketID(ctx, ticketID); err == nil && senderEmail != nil {
		p.SenderEmail, p.SenderVerificationStatus = *senderEmail, status
	}
	if h.opts.CredentialQueries != nil {
		if cred, err := h.opts.CredentialQueries.GetCredentialByTicketID(ctx, ticketID, "ean13"); err == nil {
			p.EAN13 = cred.Payload
		}
	}
	resolvePresentation(ctx, q, ticketID, &p, h.logger)
	return p
}

// describeTickets resolves payloads only (a cancellation attaches nothing).
func (h *changeEmail) describeTickets(ctx context.Context, tickets []uuid.UUID, locale string) ([]renderedTicket, error) {
	out := make([]renderedTicket, 0, len(tickets))
	for _, id := range tickets {
		out = append(out, renderedTicket{TicketID: id, Payload: h.ticketPayload(ctx, id, locale)})
	}
	return out, nil
}

// renderTickets resolves each ticket and renders its PDF anew, storing it as
// the ticket's pdf credential (upsert) so every later read sees the new date.
func (h *changeEmail) renderTickets(ctx context.Context, tickets []uuid.UUID, locale string) ([]renderedTicket, error) {
	out := make([]renderedTicket, 0, len(tickets))
	for _, id := range tickets {
		p := h.ticketPayload(ctx, id, locale)
		brand := resolveBranding(ctx, h.opts.Media, p, h.logger)
		poster := resolvePoster(ctx, h.opts.Media, p.PosterMediaID, h.logger)
		data, err := renderTicketPDF(ctx, id, p, brand.Branding, brand.LogoBytes, poster)
		if err != nil {
			return nil, fmt.Errorf("delivery: render pdf for ticket %s: %w", id, err)
		}
		if h.opts.CredentialQueries != nil {
			if _, err := h.opts.CredentialQueries.InsertTicketCredential(ctx, id, "pdf",
				base64.StdEncoding.EncodeToString(data)); err != nil {
				return nil, fmt.Errorf("delivery: store pdf credential for ticket %s: %w", id, err)
			}
		}
		out = append(out, renderedTicket{TicketID: id, Payload: p, PDF: data})
	}
	return out, nil
}

// buildMessage renders the letter for the order's tickets.
func (h *changeEmail) buildMessage(
	ctx context.Context,
	rec sessionchange.Record,
	recipient sessionchange.OrderRecipient,
	tickets []renderedTicket,
	contact sessionchange.Contact,
	moves bool,
) (email.Message, error) {
	first := tickets[0].Payload
	kind := templates.TemplateKindChange
	if !moves {
		kind = templates.TemplateKindCancel
	}

	var numbers []string
	for _, t := range tickets {
		numbers = append(numbers, pdf.DisplayNumber(t.Payload.TicketNumber, t.TicketID.String()))
	}

	// The organization's own logo through the media store; best effort, the
	// platform logo is the fallback.
	branding := resolveBranding(ctx, h.opts.Media, first, h.logger).Branding

	newStart := formatSessionForEmail(rec.New.StartAt, rec.New.Timezone)
	oldStart := formatSessionForEmail(rec.Old.StartAt, rec.Old.Timezone)
	data := templates.Data{
		TicketID:       strings.Join(numbers, ", "),
		RecipientEmail: recipient.Email,
		HolderName:     first.HolderName,
		EventName:      defaultStr(first.EventName, "Arena Event"),
		SessionStart:   formatSessionForEmail(first.SessionStart, first.SessionTZ),
		VenueName:      joinNonEmpty(", ", first.VenueName, first.VenueCity),
		Branding:       branding,
		Change: templates.ChangeData{
			Date:         sessionchange.Has(rec.Kinds, sessionchange.KindDate) || sessionchange.Has(rec.Kinds, sessionchange.KindTime),
			Venue:        sessionchange.Has(rec.Kinds, sessionchange.KindVenue),
			OldStart:     oldStart,
			NewStart:     newStart,
			OldVenue:     rec.Old.VenueName,
			NewVenue:     rec.New.VenueName,
			Message:      rec.Message,
			ContactName:  contact.Name,
			ContactEmail: contact.Email,
			ContactPhone: contact.PublicPhone(),
		},
	}
	if !moves {
		// A cancelled session's date is the one it was scheduled for.
		data.SessionStart = oldStart
		data.Change.OldStart = oldStart
	}
	if len(tickets) == 1 {
		data.TierName = first.TierName
		data.SeatSector, data.SeatRow, data.SeatNumber = first.SeatSector, first.SeatRow, first.SeatNumber
	}

	out, err := h.renderer.Render(kind, recipient.LocaleHint, data)
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: render change e-mail: %w", err)
	}
	// Replies go to the organizer (the letter says so); the platform sender,
	// or the organization's verified one, still originates it.
	first.OrgContactEmail = contact.Email
	msg := ticketEmailMessage(first, recipient.Email, out.Subject, out.HTMLBody, out.TextBody)
	if moves {
		for i, t := range tickets {
			if len(t.PDF) == 0 {
				continue
			}
			msg.Attachments = append(msg.Attachments, email.Attachment{
				Filename:    fmt.Sprintf("ticket-%s.pdf", numbers[i]),
				ContentType: "application/pdf",
				Data:        t.PDF,
			})
		}
	}
	return msg, nil
}
