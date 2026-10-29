// invitation_cancel_email.go — the invitation.cancel_email worker job: the
// letter Arena writes to a guest whose staff invitation the organizer
// withdrew (spec 46, section 3.4, decision R-9).
//
// POST .../invitations/{order_id}/cancel queues one job after it commits,
// naming the order and the tickets it cancelled, only when the order carries
// the guest's own address and the caller left notify_guest on. The handler
// writes the same "cancel" letter the session cancellation uses, worded for
// one invitation (templates.ChangeData.Invitation) and makes no promise about
// money. An order without a guest address (delivery my_email / pdf_only) is
// never mailed: the guest has no address Arena may write to, and the issuer
// already knows. Buyer addresses never reach the logs.
package delivery

import (
	"context"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobTypeInvitationCancelEmail is the worker_jobs.job_type of the letter that
// tells a guest their invitation was cancelled.
const JobTypeInvitationCancelEmail = "invitation.cancel_email"

// InvitationCancelEmailMaxAttempts is the retry budget of one such job.
const InvitationCancelEmailMaxAttempts = 6

// InvitationCancelPayload is the JSON payload of an invitation.cancel_email job.
type InvitationCancelPayload struct {
	// OrderID is the invitation order.
	OrderID string `json:"order_id"`
	// TicketIDs are the tickets the cancellation voided; the letter names them.
	TicketIDs []string `json:"ticket_ids"`
}

// InvitationCancelEmailOptions are the dependencies of the job.
type InvitationCancelEmailOptions struct {
	// HandlerOptions supplies the sender, media resolver, templates and the
	// query set shared with ticket.deliver.
	HandlerOptions
	// DB reads the order and the organizer contact.
	DB sessionchange.DB
}

// NewInvitationCancelEmailHandler builds the worker handler of
// JobTypeInvitationCancelEmail.
func NewInvitationCancelEmailHandler(opts InvitationCancelEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: invitation cancel e-mail templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	h := &invitationCancelEmail{opts: opts, logger: logger, renderer: renderer}
	// The ticket payload resolution (branding, presentation, contact route) is
	// the one the change letter uses; share it instead of copying it.
	h.payloads = &changeEmail{opts: ChangeEmailOptions(opts), logger: logger, renderer: renderer}
	return h.handle
}

type invitationCancelEmail struct {
	opts     InvitationCancelEmailOptions
	logger   *slog.Logger
	renderer *templates.Renderer
	payloads *changeEmail
}

// invitationCancelOrderSQL reads the guest's address (strictly the order's own
// buyer_email: the issuer's address is never a fallback here), name and
// language, and the ids the contact lookup needs.
const invitationCancelOrderSQL = `
SELECT o.org_id, o.event_id,
       COALESCE(btrim(o.buyer_email), ''),
       COALESCE(cs.buyer_locale, '')
FROM   orders o
LEFT   JOIN checkout_sessions cs ON cs.id = o.checkout_session_id
WHERE  o.id = $1 AND o.source = 'complimentary'`

func (h *invitationCancelEmail) handle(ctx context.Context, payload []byte) error {
	var p InvitationCancelPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		h.logger.Error("delivery: malformed invitation cancel payload", slog.String("error", err.Error()))
		return nil // permanent
	}
	orderID, err := uuid.Parse(p.OrderID)
	if err != nil {
		h.logger.Error("delivery: invalid order id in invitation cancel payload")
		return nil
	}
	var ticketIDs []uuid.UUID
	for _, raw := range p.TicketIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			h.logger.Error("delivery: invalid ticket id in invitation cancel payload")
			return nil
		}
		ticketIDs = append(ticketIDs, id)
	}
	if len(ticketIDs) == 0 {
		return nil
	}

	var orgID, eventID uuid.UUID
	var guestEmail, locale string
	if err := h.opts.DB.QueryRow(ctx, invitationCancelOrderSQL, orderID).Scan(&orgID, &eventID, &guestEmail, &locale); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not an invitation order (any more): nothing to say
		}
		return fmt.Errorf("delivery: read invitation order %s: %w", orderID, err)
	}
	if guestEmail == "" {
		h.logger.Info("delivery: invitation cancel e-mail not sent",
			slog.String("order_id", orderID.String()), slog.String("reason", "no address for the guest"))
		return nil
	}
	if h.opts.Sender == nil || email.IsDevOnly(h.opts.Sender) {
		h.logger.Info("delivery: invitation cancel e-mail not sent",
			slog.String("order_id", orderID.String()), slog.String("reason", "no production e-mail sender configured"))
		return nil
	}

	tickets, err := h.payloads.describeTickets(ctx, ticketIDs, locale)
	if err != nil {
		return err
	}
	msg, err := h.buildMessage(ctx, orgID, eventID, guestEmail, locale, tickets)
	if err != nil {
		return err
	}
	letter := emailstatus.NewLetter{OrgID: orgID, OrderID: &orderID, Kind: emailstatus.KindCancel, Recipient: guestEmail,
		TemplateVersion: templateVersion(h.renderer, templates.TemplateKindCancel, locale)}
	letter.TicketNumbers = letterTicketNumbers(ctx, h.opts.DB, ticketIDs)
	if sendErr := sendRecorded(ctx, h.opts.Sender, h.opts.DB, h.logger, letter, msg, nil); sendErr != nil {
		h.logger.Warn("delivery: invitation cancel e-mail send failed; worker will retry",
			slog.String("order_id", orderID.String()))
		return fmt.Errorf("delivery: send invitation cancel e-mail for order %s: %w", orderID, sendErr)
	}
	h.logger.Info("delivery: invitation cancel e-mail sent",
		slog.String("order_id", orderID.String()), slog.Int("tickets", len(ticketIDs)))
	return nil
}

// buildMessage renders the cancel letter, worded for an invitation.
func (h *invitationCancelEmail) buildMessage(
	ctx context.Context,
	orgID, eventID uuid.UUID,
	to, locale string,
	tickets []renderedTicket,
) (email.Message, error) {
	first := tickets[0].Payload
	numbers := make([]string, 0, len(tickets))
	for _, t := range tickets {
		numbers = append(numbers, pdf.DisplayNumber(t.Payload.TicketNumber, t.TicketID.String()))
	}
	branding := resolveBranding(ctx, h.opts.Media, first, h.logger).Branding

	// The contact the guest can write to: the event's promoter e-mail when it
	// has one, otherwise the ticket letter's rule (public e-mail, the hosted
	// page's form, or the organization's address).
	contactEmail, contactForm, contactName := first.OrgContactEmail, first.ContactFormURL, ""
	if c, err := sessionchange.ResolveContact(ctx, h.opts.DB, eventID, orgID); err == nil && c.Complete() {
		contactName = c.Name
		if c.Source == "promoter" {
			contactEmail, contactForm = strings.TrimSpace(c.Email), ""
		}
	}

	data := templates.Data{
		TicketID:       strings.Join(numbers, ", "),
		RecipientEmail: to,
		HolderName:     first.HolderName,
		EventName:      defaultStr(first.EventName, "Arena Event"),
		SessionStart:   formatSessionForEmail(first.SessionStart, first.SessionTZ),
		VenueName:      joinNonEmpty(", ", first.VenueName, first.VenueCity),
		Branding:       branding,
		Change: templates.ChangeData{
			Invitation:     true,
			ContactName:    contactName,
			ContactEmail:   contactEmail,
			ContactFormURL: contactForm,
		},
	}
	if len(tickets) == 1 {
		data.TierName = first.TierName
		data.SeatSector, data.SeatRow, data.SeatNumber = first.SeatSector, first.SeatRow, first.SeatNumber
	}
	out, err := h.renderer.Render(templates.TemplateKindCancel, locale, data)
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: render invitation cancel e-mail: %w", err)
	}
	first.OrgContactEmail, first.ContactFormURL = contactEmail, contactForm
	return ticketEmailMessage(first, to, out.Subject, out.HTMLBody, out.TextBody), nil
}
