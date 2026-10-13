// refund_email.go — the refund.buyer_email worker job: the letter Arena
// writes to a buyer whose money went back (PAY-07, spec 36 section 7 step 5).
//
// The refund engine queues one job per refund operation (refund_batches) a
// minute after a refund settles. The handler claims the operation's refunds
// that SUCCEEDED (the provider confirmed the money, not merely accepted a
// pending refund that may still fail), were settled (their tickets are
// cancelled when due) and that no letter covers yet, mails them in ONE letter
// and marks them. If a letter is lost anyway (mail outage past the job's
// retries, a worker that died after claiming), refund.sweep queues it again.
// Rules it keeps:
//
//   - nothing is mailed unless the operation asked for it (notify_buyer);
//   - the letter states the amount and the usual bank delay and promises
//     nothing else; a money-only refund does not say the tickets are void;
//   - a refund is claimed before the send and the claim is given back when
//     the send fails, so a redelivered job never mails twice and a failed one
//     is retried; a claim older than ten minutes (a crashed worker) is taken
//     over;
//   - a buyer without an address, or a deployment without a production sender,
//     ends the refunds as notified (skipped) rather than retrying forever;
//   - buyer addresses never reach the logs.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// RefundEmailOptions are the dependencies of the refund.buyer_email job.
type RefundEmailOptions struct {
	// HandlerOptions supplies the sender, media resolver, templates and the
	// query set shared with ticket.deliver.
	HandlerOptions
	// DB reads the batch, its refunds, the order and the organizer contact.
	DB sessionchange.DB
}

// NewRefundEmailHandler builds the worker handler of refunds.JobTypeBuyerEmail.
func NewRefundEmailHandler(opts RefundEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: refund e-mail templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	h := &refundEmail{opts: opts, logger: logger, renderer: renderer}
	// The ticket payload resolution (branding, presentation) is the one the
	// change letter uses; share it instead of copying it.
	h.payloads = &changeEmail{opts: ChangeEmailOptions(opts), logger: logger, renderer: renderer}
	return h.handle
}

type refundEmail struct {
	opts     RefundEmailOptions
	logger   *slog.Logger
	renderer *templates.Renderer
	payloads *changeEmail
}

// claimedRefund is one refund this job owns until it is mailed or released.
type claimedRefund struct {
	ID           uuid.UUID
	TicketID     *uuid.UUID
	Amount       int64
	Currency     string
	CancelTicket bool
}

const claimBuyerNoticeSQL = `
UPDATE refunds SET buyer_notice_claimed_at = now()
WHERE  batch_id = $1
  AND  buyer_notified_at IS NULL
  AND  state = 'succeeded' AND settled_at IS NOT NULL
  AND  (buyer_notice_claimed_at IS NULL OR buyer_notice_claimed_at < now() - interval '10 minutes')
RETURNING id, ticket_id, amount, currency, cancel_ticket`

// refundLetterHoldSQL is true while the operation $1, younger than $2, still
// has a refund that was not sent to the provider yet.
const refundLetterHoldSQL = `SELECT EXISTS (
	SELECT 1 FROM refunds r JOIN refund_batches b ON b.id = r.batch_id
	WHERE r.batch_id = $1 AND r.provider_refund_id IS NULL AND r.provider_attempts = 0
	  AND r.state IN ('requested', 'approved', 'provider_pending')
	  AND b.created_at > now() - $2::interval)`

// The letter of an operation waits for its unsent refunds at most
// refundLetterHoldFor, looking again every refundLetterRecheck.
const (
	refundLetterHoldFor = "2 hours"
	refundLetterRecheck = "2 minutes"
)

func (h *refundEmail) handle(ctx context.Context, payload []byte) error {
	var p refunds.BuyerEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		h.logger.Error("delivery: malformed refund e-mail payload", slog.String("error", err.Error()))
		return nil // permanent
	}
	batchID, err := uuid.Parse(p.BatchID)
	if err != nil {
		h.logger.Error("delivery: invalid batch id in refund e-mail payload")
		return nil
	}

	var orderID, orgID uuid.UUID
	var notify bool
	if err := h.opts.DB.QueryRow(ctx,
		`SELECT order_id, org_id, notify_buyer FROM refund_batches WHERE id = $1`, batchID).
		Scan(&orderID, &orgID, &notify); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("delivery: read refund batch: %w", err)
	}
	if !notify {
		return nil
	}
	// A big operation is driven over minutes (the create call's budget, then
	// one refund per payment per sweep pass): while a refund of it is still
	// waiting to be sent, the letter waits too, so the buyer gets one letter
	// for the operation. It waits at most refundLetterHoldFor after the
	// operation was created.
	var waiting bool
	if err := h.opts.DB.QueryRow(ctx, refundLetterHoldSQL, batchID, refundLetterHoldFor).Scan(&waiting); err != nil {
		return fmt.Errorf("delivery: check unsent refunds: %w", err)
	}
	if waiting {
		if _, err := h.opts.DB.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			SELECT $1, $2::jsonb, 5, 'pending', now() + $3::interval
			WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending' AND payload->>'batch_id' = $4)`,
			refunds.JobTypeBuyerEmail, payload, refundLetterRecheck, batchID.String()); err != nil {
			return fmt.Errorf("delivery: postpone refund e-mail: %w", err)
		}
		return nil
	}

	rows, err := h.opts.DB.Query(ctx, claimBuyerNoticeSQL, batchID)
	if err != nil {
		return fmt.Errorf("delivery: claim refunds: %w", err)
	}
	var claimed []claimedRefund
	for rows.Next() {
		var c claimedRefund
		if err := rows.Scan(&c.ID, &c.TicketID, &c.Amount, &c.Currency, &c.CancelTicket); err != nil {
			rows.Close()
			return err
		}
		claimed = append(claimed, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(claimed) == 0 {
		return nil // already mailed, or nothing accepted yet
	}
	ids := make([]uuid.UUID, len(claimed))
	for i, c := range claimed {
		ids[i] = c.ID
	}

	recipient, err := sessionchange.LoadOrderRecipient(ctx, h.opts.DB, orderID)
	if err != nil {
		h.release(ctx, ids)
		return fmt.Errorf("delivery: read order %s: %w", orderID, err)
	}
	if recipient.Email == "" {
		return h.done(ctx, ids, "no address for the buyer")
	}
	if h.opts.Sender == nil || email.IsDevOnly(h.opts.Sender) {
		return h.done(ctx, ids, "no production e-mail sender configured")
	}

	msg, err := h.buildMessage(ctx, orderID, orgID, recipient, claimed)
	if err != nil {
		h.release(ctx, ids)
		return err
	}
	letter := emailstatus.NewLetter{OrgID: orgID, OrderID: &orderID, Kind: emailstatus.KindRefund, Recipient: recipient.Email,
		TemplateVersion: templateVersion(h.renderer, templates.TemplateKindRefund, recipient.LocaleHint)}
	var refundTickets []uuid.UUID
	for _, c := range claimed {
		if c.TicketID != nil {
			refundTickets = append(refundTickets, *c.TicketID)
		}
	}
	letter.TicketNumbers = letterTicketNumbers(ctx, h.opts.DB, refundTickets)
	if sendErr := sendRecorded(ctx, h.opts.Sender, h.opts.DB, h.logger, letter, msg, nil); sendErr != nil {
		h.release(ctx, ids)
		h.logger.Warn("delivery: refund e-mail send failed; worker will retry", slog.String("batch_id", batchID.String()))
		return fmt.Errorf("delivery: send refund e-mail for batch %s: %w", batchID, sendErr)
	}
	if err := h.markNotified(ctx, ids); err != nil {
		// The letter is out; a lost mark would mail it again after the claim
		// goes stale, which is the lesser evil than reporting a failure that
		// retries a sent letter right now.
		h.logger.Error("delivery: could not mark refunds as mailed", slog.String("batch_id", batchID.String()), slog.String("error", err.Error()))
	}
	h.logger.Info("delivery: refund e-mail sent",
		slog.String("batch_id", batchID.String()), slog.Int("refunds", len(claimed)))
	return nil
}

// done ends the claimed refunds without a letter.
func (h *refundEmail) done(ctx context.Context, ids []uuid.UUID, reason string) error {
	if err := h.markNotified(ctx, ids); err != nil {
		return err
	}
	h.logger.Info("delivery: refund e-mail not sent", slog.String("reason", reason), slog.Int("refunds", len(ids)))
	return nil
}

func (h *refundEmail) markNotified(ctx context.Context, ids []uuid.UUID) error {
	_, err := h.opts.DB.Exec(ctx, `UPDATE refunds SET buyer_notified_at = now() WHERE id = ANY($1)`, ids)
	return err
}

// release gives the claim back so the retry (or the next job) can take it.
func (h *refundEmail) release(ctx context.Context, ids []uuid.UUID) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := h.opts.DB.Exec(rctx, `UPDATE refunds SET buyer_notice_claimed_at = NULL WHERE id = ANY($1) AND buyer_notified_at IS NULL`, ids); err != nil {
		h.logger.Warn("delivery: could not release the refund e-mail claim", slog.String("error", err.Error()))
	}
}

// buildMessage renders the letter for the claimed refunds.
func (h *refundEmail) buildMessage(ctx context.Context, orderID, orgID uuid.UUID, recipient sessionchange.OrderRecipient, claimed []claimedRefund) (email.Message, error) {
	var (
		total       int64
		currency    string
		anyCancel   bool
		lines       []templates.TicketLine
		first       Payload
		haveFirst   bool
		orderEvents uuid.UUID
	)
	_ = h.opts.DB.QueryRow(ctx, `SELECT event_id FROM orders WHERE id = $1`, orderID).Scan(&orderEvents)
	for _, c := range claimed {
		total += c.Amount
		if currency == "" {
			currency = c.Currency
		}
		line := templates.TicketLine{Price: formatMoney(c.Amount, c.Currency)}
		if c.TicketID != nil {
			tp := h.payloads.ticketPayload(ctx, *c.TicketID, recipient.LocaleHint)
			line.Number = pdf.DisplayNumber(tp.TicketNumber, c.TicketID.String())
			line.Tier = tp.TierName
			line.Seat = templates.FormatSeat(recipient.LocaleHint, tp.SeatSector, tp.SeatRow, tp.SeatNumber)
			if !haveFirst {
				first, haveFirst = tp, true
			}
		}
		lines = append(lines, line)
	}
	if !haveFirst {
		first = Payload{Locale: recipient.LocaleHint}
	}
	// An operation whose refunds name no ticket still belongs to the order's
	// event: the letter names it rather than a placeholder.
	if first.EventName == "" && orderEvents != uuid.Nil {
		_ = h.opts.DB.QueryRow(ctx, `SELECT name FROM events WHERE id = $1`, orderEvents).Scan(&first.EventName)
	}
	// "No longer valid" is said per ticket from the ticket's real state,
	// never from the cancel_ticket intent of the refund: a refund can be
	// money-only, or its cancellation can still be pending, and one letter
	// can cover both kinds.
	var ticketIDs []uuid.UUID
	for _, c := range claimed {
		if c.TicketID != nil {
			ticketIDs = append(ticketIDs, *c.TicketID)
		}
	}
	void := map[uuid.UUID]bool{}
	if len(ticketIDs) > 0 {
		rows, err := h.opts.DB.Query(ctx, `SELECT id FROM tickets WHERE id = ANY($1) AND status = 'cancelled'`, ticketIDs)
		if err != nil {
			return email.Message{}, fmt.Errorf("delivery: read ticket states for the refund e-mail: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return email.Message{}, fmt.Errorf("delivery: read ticket states for the refund e-mail: %w", err)
			}
			void[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return email.Message{}, fmt.Errorf("delivery: read ticket states for the refund e-mail: %w", err)
		}
	}
	voidCount := 0
	for i, c := range claimed {
		if c.TicketID != nil && void[*c.TicketID] {
			lines[i].Void = true
			voidCount++
		}
	}
	allVoid := voidCount > 0 && voidCount == len(lines)
	anyCancel = voidCount > 0
	branding := resolveBranding(ctx, h.opts.Media, first, h.logger).Branding

	data := templates.Data{
		RecipientEmail: recipient.Email,
		HolderName:     first.HolderName,
		EventName:      defaultStr(first.EventName, "Arena Event"),
		SessionStart:   formatSessionForEmail(first.SessionStart, first.SessionTZ),
		VenueName:      joinNonEmpty(", ", first.VenueName, first.VenueCity),
		Branding:       branding,
		Tickets:        lines,
		Refund: &templates.RefundData{Total: formatMoney(total, currency),
			TicketsCancelled: allVoid, SomeTicketsCancelled: anyCancel && !allVoid},
	}
	out, err := h.renderer.Render(templates.TemplateKindRefund, recipient.LocaleHint, data)
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: render refund e-mail: %w", err)
	}
	// Replies go to the organizer when one is known and complete.
	if orderEvents != uuid.Nil {
		if contact, cerr := sessionchange.ResolveContact(ctx, h.opts.DB, orderEvents, orgID); cerr == nil && contact.Complete() {
			first.OrgContactEmail = strings.TrimSpace(contact.Email)
		}
	}
	return ticketEmailMessage(first, recipient.Email, out.Subject, out.HTMLBody, out.TextBody), nil
}
