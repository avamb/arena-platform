// late_payment_email.go — checkout.late_payment_email: the buyer's letter
// about a payment that came after the order had closed (spec 62 П-15).
//
// The payment webhook queues it once per payment (late_payments row,
// hcheckout late_payment.go) when the money goes back — arena refunds it
// (refund_requested) or the organizer must (operator_review). A payment whose
// places were taken again needs no such letter: its ticket letter goes out as
// for any paid order.
//
// The letter is claimed (late_payments.buyer_notified_at) before the send and
// the claim is given back when the send fails, so a redelivered job never
// mails twice and a failed one is retried. It is recorded in email_letters
// (kind late_payment) like every buyer letter. Buyer addresses never reach the
// logs.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/latepayment"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// NewLatePaymentEmailHandler builds the handler of latepayment.JobTypeEmail.
func NewLatePaymentEmailHandler(opts GrowthLetterOptions) worker.HandlerFunc {
	logger := opts.logger()
	return func(ctx context.Context, payload []byte) error {
		var p latepayment.JobPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil
		}
		piID, err := uuid.Parse(p.PaymentIntentID)
		if err != nil || opts.DB == nil {
			return nil
		}
		q := gen.New(opts.DB)
		lp, err := q.GetLatePaymentLetter(ctx, piID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if lp.BuyerNotifiedAt != nil || lp.Outcome == latepayment.OutcomeReacquired {
			return nil
		}
		to := ""
		if lp.BuyerEmail != nil {
			to = *lp.BuyerEmail
		}
		skip := ""
		switch {
		case to == "":
			skip = "no address for the buyer"
		case opts.Sender == nil || email.IsDevOnly(opts.Sender):
			skip = "no production e-mail sender configured"
		}
		if skip != "" {
			logger.Info("delivery: late payment letter not sent", slog.String("payment_intent_id", piID.String()), slog.String("reason", skip))
			_, err := q.MarkLatePaymentNotified(ctx, piID)
			return err
		}

		r, err := opts.renderer()
		if err != nil {
			return err
		}
		locale := ""
		if lp.BuyerLocale != nil {
			locale = *lp.BuyerLocale
		}
		style, err := loadOrgLetterStyle(ctx, opts.DB, lp.OrgID)
		if err != nil {
			return err
		}
		data := templates.Data{
			RecipientEmail: to,
			Branding:       style.branding(ctx, opts.Media, logger),
			LatePayment: templates.LatePaymentData{
				BuyerCancelled: lp.LateCase == latepayment.CaseBuyerCancelled,
				Refunding:      lp.Outcome == latepayment.OutcomeRefundRequested,
				Total:          formatMoney(lp.Amount, lp.Currency),
			},
		}
		if lp.OrderNumber != nil {
			data.LatePayment.OrderNumber = strconv.FormatInt(*lp.OrderNumber, 10)
		}
		var whiteLabel bool
		var brand string
		if lp.SessionID != nil {
			if in, ierr := salesalert.LoadSessionInfo(ctx, opts.DB, lp.OrgID, *lp.SessionID); ierr == nil {
				data.EventName = in.EventName
				data.Branding.OrgName = in.OrgName
				data.LatePayment.When = in.When()
				data.LatePayment.Venue = in.Venue
				if locale == "" {
					locale = in.OrgLocale
				}
			} else if !errors.Is(ierr, salesalert.ErrSessionGone) {
				return ierr
			}
			whiteLabel, brand = whiteLabelForSession(ctx, opts.DB, logger, *lp.SessionID)
		}
		if locale == "" {
			locale = lp.OrgLocale
		}
		data.WhiteLabel = whiteLabel
		var lateSession uuid.UUID
		if lp.SessionID != nil {
			lateSession = *lp.SessionID
		}
		contact := sessionContactEmail(ctx, opts.DB, lp.OrgID, lateSession, logger)
		data.Contact = templates.BuyerContact{Email: contact, OrderNumber: data.LatePayment.OrderNumber}
		out, err := r.Render(templates.TemplateKindLatePayment, locale, data)
		if err != nil {
			return fmt.Errorf("delivery: render late payment letter: %w", err)
		}
		letter := emailstatus.NewLetter{
			OrgID:           lp.OrgID,
			OrderID:         lp.OrderID,
			Kind:            emailstatus.KindLatePayment,
			Recipient:       to,
			TemplateVersion: templateVersion(r, templates.TemplateKindLatePayment, locale),
		}
		msg := email.Message{To: to, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}
		style.applySender(&msg)
		applyBrandSender(&msg, brand)
		withReplyTo(&msg, contact)

		claimed, err := q.MarkLatePaymentNotified(ctx, piID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		if err := sendRecorded(ctx, opts.Sender, opts.DB, logger, letter, msg, nil); err != nil {
			if uerr := q.UnmarkLatePaymentNotified(context.WithoutCancel(ctx), piID); uerr != nil {
				logger.Warn("delivery: late payment letter claim not released",
					slog.String("payment_intent_id", piID.String()), slog.String("error", uerr.Error()))
			}
			return err
		}
		logger.Info("delivery: late payment letter sent",
			slog.String("payment_intent_id", piID.String()), slog.String("outcome", lp.Outcome))
		return nil
	}
}
