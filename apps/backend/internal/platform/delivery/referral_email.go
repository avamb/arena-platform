// referral_email.go — the letters of "Refer a friend" (spec 52 sections 5.2,
// 6.2 and 7.3).
//
//   - referral.cashback_email {reward_id, kind} writes to the REFERRER, who is
//     a buyer. "sent": a friend bought with their code and the cashback went
//     back to the card they paid with (it promises only the bank's delay).
//     "unavailable": the cashback was owed and could not be returned to the
//     card; the letter sends them to the organizer by the ticket letters'
//     contact rule (the sales page's public e-mail, else its contact form,
//     never a private address).
//   - referral.reversal_email {reward_id} writes to the organization's OWNERS:
//     a friend took the money back or opened a dispute after the cashback was
//     paid, so it cannot be taken back from the referrer.
//
// Rules it keeps:
//
//   - one cashback letter per reward, plus at most one correction:
//     referral_rewards.letter_sent_at (correction_sent_at for a correction) is
//     claimed before the send and given back when the send fails, so a
//     redelivered job never mails twice and a failed one is retried;
//   - the letter is sent only while the reward still is in the state the job
//     was queued for (a reward that moved on has nothing true to say);
//   - the language is the buyer's own (checkout_sessions.buyer_locale of the
//     referrer's order), else the organization's, else English;
//   - buyer addresses never reach the logs.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ReferralEmailOptions are the dependencies of referral.cashback_email.
type ReferralEmailOptions struct {
	// HandlerOptions supplies the sender, media resolver, templates and the
	// query set shared with ticket.deliver.
	HandlerOptions
	// DB reads the reward, the referrer's order and the organizer contact.
	DB sessionchange.DB
}

type referralCashbackEmail struct {
	opts     ReferralEmailOptions
	logger   *slog.Logger
	renderer *templates.Renderer
	payloads *changeEmail
}

// NewReferralCashbackEmailHandler builds the worker handler of
// referral.JobTypeCashbackEmail.
func NewReferralCashbackEmailHandler(opts ReferralEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: referral e-mail templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	h := &referralCashbackEmail{opts: opts, logger: logger, renderer: renderer}
	h.payloads = &changeEmail{opts: ChangeEmailOptions(opts), logger: logger, renderer: renderer}
	return h.handle
}

func (h *referralCashbackEmail) handle(ctx context.Context, payload []byte) error {
	var p referral.CashbackEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		h.logger.Error("delivery: malformed referral cashback e-mail payload", slog.String("error", err.Error()))
		return nil // permanent
	}
	rewardID, err := uuid.Parse(p.RewardID)
	if err != nil || (p.Kind != referral.LetterSent && p.Kind != referral.LetterUnavailable) {
		h.logger.Error("delivery: invalid referral cashback e-mail payload")
		return nil
	}
	rw, err := gen.ScanReferralRewardRow(h.opts.DB.QueryRow(ctx,
		`SELECT `+gen.ReferralRewardColumns+` FROM referral_rewards WHERE id = $1`, rewardID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delivery: read referral reward: %w", err)
	}
	reason := ""
	if rw.Reason != nil {
		reason = *rw.Reason
	}
	providerCode := ""
	if rw.ProviderCode != nil {
		providerCode = *rw.ProviderCode
	}
	if want, ok := referral.LetterKindFor(rw.State, reason, providerCode); !ok || want != p.Kind {
		return nil // the reward moved on: nothing true to say
	}
	if rw.ReferrerOrderID == nil && rw.ReferrerShopOrderID == nil {
		return nil
	}
	// The one letter of a reward, or its one correction (the referrer was told
	// the money went back and it did not stay back).
	column := "letter_sent_at"
	if p.Correction {
		column = "correction_sent_at"
		if rw.LetterSentAt == nil || rw.CorrectionSentAt != nil || p.Kind != referral.LetterUnavailable {
			return nil
		}
	} else if rw.LetterSentAt != nil {
		return nil
	}
	if h.opts.Sender == nil || email.IsDevOnly(h.opts.Sender) {
		h.logger.Info("delivery: referral cashback e-mail skipped, no production e-mail sender configured")
		return nil
	}

	// Claim the letter. column is one of two fixed names, never input.
	tag, err := h.opts.DB.Exec(ctx, `UPDATE referral_rewards SET `+column+` = now()
		WHERE id = $1 AND `+column+` IS NULL`, rw.ID)
	if err != nil {
		return fmt.Errorf("delivery: claim referral letter: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil // another run owns it, or it is mailed
	}

	recipient, err := h.loadRecipient(ctx, rw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the order is gone: nobody to write to
	}
	if err != nil {
		h.release(ctx, rw.ID, column)
		return fmt.Errorf("delivery: read the referrer's order of reward %s: %w", rw.ID, err)
	}
	if recipient.Email == "" {
		h.logger.Info("delivery: referral cashback e-mail not sent", slog.String("reason", "no address for the referrer"))
		return nil
	}

	msg, letter, err := h.buildMessage(ctx, rw, recipient, p.Kind)
	if err != nil {
		h.release(ctx, rw.ID, column)
		return err
	}
	if sendErr := sendRecorded(ctx, h.opts.Sender, h.opts.DB, h.logger, letter, msg, nil); sendErr != nil {
		h.release(ctx, rw.ID, column)
		h.logger.Warn("delivery: referral cashback e-mail send failed; worker will retry",
			slog.String("reward_id", rw.ID.String()), slog.String("kind", p.Kind))
		return fmt.Errorf("delivery: send referral cashback e-mail for reward %s: %w", rw.ID, sendErr)
	}
	h.logger.Info("delivery: referral cashback e-mail sent",
		slog.String("reward_id", rw.ID.String()), slog.String("kind", p.Kind))
	return nil
}

// loadRecipient reads who the letter goes to: the buyer of the referrer's ticket
// order or of the referrer's shop order.
func (h *referralCashbackEmail) loadRecipient(ctx context.Context, rw gen.ReferralRewardRow) (sessionchange.OrderRecipient, error) {
	if rw.ReferrerShopOrderID == nil {
		return sessionchange.LoadOrderRecipient(ctx, h.opts.DB, *rw.ReferrerOrderID)
	}
	o, err := gen.New(h.opts.DB).GetShopOrder(ctx, *rw.ReferrerShopOrderID, rw.OrgID)
	if err != nil {
		return sessionchange.OrderRecipient{}, err
	}
	r := sessionchange.OrderRecipient{Email: o.BuyerEmail, Name: o.BuyerName, OrderID: o.ID, SystemID: o.Number, Status: o.Status}
	if o.BuyerLocale != nil {
		r.LocaleHint = *o.BuyerLocale
	}
	return r, nil
}

// release gives the claim back so the retry can take it.
func (h *referralCashbackEmail) release(ctx context.Context, id uuid.UUID, column string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := h.opts.DB.Exec(rctx, `UPDATE referral_rewards SET `+column+` = NULL WHERE id = $1`, id); err != nil {
		h.logger.Warn("delivery: could not release the referral letter claim", slog.String("error", err.Error()))
	}
}

// buildMessage renders the letter for the reward and prepares its record.
func (h *referralCashbackEmail) buildMessage(ctx context.Context, rw gen.ReferralRewardRow, to sessionchange.OrderRecipient, kind string) (email.Message, emailstatus.NewLetter, error) {
	var code, orgLocale string
	if err := h.opts.DB.QueryRow(ctx, `SELECT pc.code, COALESCE(o.default_locale, '')
		FROM promo_codes pc JOIN organizations o ON o.id = pc.org_id WHERE pc.id = $1`, rw.PromoCodeID).
		Scan(&code, &orgLocale); err != nil {
		return email.Message{}, emailstatus.NewLetter{}, fmt.Errorf("delivery: read referral code: %w", err)
	}
	locale := to.LocaleHint
	if locale == "" {
		locale = orgLocale
	}

	// The seller's branding and the organizer contact come from a ticket of
	// the referrer's order: the same resolution, the same contact rule, as
	// the ticket letters.
	var first Payload
	whiteLabel := false
	if rw.ReferrerShopOrderID != nil {
		// A shop order: the organization's own branding and the SHOP's contact
		// address, the same the shop letters print.
		p, err := shopLetterPayload(ctx, h.opts.DB, rw.OrgID)
		if err != nil {
			return email.Message{}, emailstatus.NewLetter{}, err
		}
		p.Locale = locale
		first = p
		whiteLabel = whiteLabelForOrg(ctx, whiteLabelResolver(h.opts.DB), h.logger, rw.OrgID)
	} else {
		var ticketID uuid.UUID
		if err := h.opts.DB.QueryRow(ctx, `SELECT ticket_id FROM order_items
			WHERE order_id = $1 AND ticket_id IS NOT NULL ORDER BY ordinal LIMIT 1`, *rw.ReferrerOrderID).
			Scan(&ticketID); err == nil {
			first = h.payloads.ticketPayload(ctx, ticketID, locale)
			whiteLabel = whiteLabelForTicket(ctx, whiteLabelResolver(h.opts.DB), h.logger, ticketID.String())
		} else {
			first = Payload{Locale: locale}
		}
	}
	branding := resolveBranding(ctx, h.opts.Media, first, h.logger).Branding

	tkind := templates.TemplateKindReferralCashbackSent
	if kind == referral.LetterUnavailable {
		tkind = templates.TemplateKindReferralCashbackUnavailable
	}
	data := templates.Data{
		RecipientEmail: to.Email,
		HolderName:     to.Name,
		EventName:      first.EventName,
		Branding:       branding,
		WhiteLabel:     whiteLabel,
		Referral: &templates.ReferralData{
			Code:   code,
			Amount: formatMoney(rw.AmountMinor, rw.Currency),
			Order:  strconv.FormatInt(to.SystemID, 10),
		},
	}
	out, err := h.renderer.Render(tkind, locale, data)
	if err != nil {
		return email.Message{}, emailstatus.NewLetter{}, fmt.Errorf("delivery: render referral cashback e-mail: %w", err)
	}
	msg := ticketEmailMessage(first, to.Email, out.Subject, out.HTMLBody, out.TextBody)
	applyWhiteLabelSender(&msg, whiteLabel, brandSenderName(first))

	lkind := emailstatus.KindReferralCashback
	if kind == referral.LetterUnavailable {
		lkind = emailstatus.KindReferralUnavailable
	}
	// Recorded under its own kind (migration 0191), tied to the referrer's order and
	// to no refund: it is no refund letter.
	letter := emailstatus.NewLetter{
		OrgID: rw.OrgID, OrderID: rw.ReferrerOrderID, ShopOrderID: rw.ReferrerShopOrderID, Kind: lkind, Recipient: to.Email,
		TemplateVersion: templateVersion(h.renderer, tkind, locale),
	}
	return msg, letter, nil
}

// ReferralReversalEmailOptions are the dependencies of referral.reversal_email.
type ReferralReversalEmailOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        ScannedAlertDB
	Logger    *slog.Logger
}

// NewReferralReversalEmailHandler builds the worker handler of
// referral.JobTypeReversalEmail: the letter to an organization's owners about
// a friend who took the money back or opened a dispute.
func NewReferralReversalEmailHandler(opts ReferralReversalEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: referral reversal templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	return func(ctx context.Context, payload []byte) error {
		var p referral.ReversalEmailPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil // a payload nobody can read is never going to work
		}
		rewardID, err := uuid.Parse(p.RewardID)
		if err != nil {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: referral reversal e-mail skipped, no production e-mail sender configured")
			return nil
		}
		rw, err := gen.ScanReferralRewardRow(opts.DB.QueryRow(ctx,
			`SELECT `+gen.ReferralRewardColumns+` FROM referral_rewards WHERE id = $1`, rewardID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if rw.FriendReversal == nil {
			return nil
		}
		facts, ok, err := referral.LoadReversalFacts(ctx, opts.DB, rw)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		recipients, err := salesAlertRecipients(ctx, opts.DB, rw.OrgID, false)
		if err != nil {
			return err
		}
		if len(recipients) == 0 {
			logger.Error("delivery: referral reversal has no recipient in the organization", slog.String("org_id", rw.OrgID.String()))
			return nil
		}
		reversal := templates.ReferralReversalRefunded
		if *rw.FriendReversal == "disputed" {
			reversal = templates.ReferralReversalDisputed
		}
		data := templates.Data{
			Branding: templates.Branding{OrgName: facts.OrgName},
			Referral: &templates.ReferralData{
				Code: facts.Code, Amount: formatMoney(rw.AmountMinor, rw.Currency),
				Order: strconv.FormatInt(facts.FriendOrderNumber, 10), Reversal: reversal, CodePaused: facts.CodePaused(),
			},
		}
		sent := 0
		var firstErr error
		for _, rc := range recipients {
			data.RecipientEmail = rc.email
			out, rerr := renderer.Render(templates.TemplateKindReferralReversalOrganizer, rc.locale, data)
			if rerr != nil {
				return rerr
			}
			if serr := opts.Sender.Send(ctx, email.Message{To: rc.email, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}); serr != nil {
				logger.Warn("delivery: referral reversal e-mail not sent", slog.String("org_id", rw.OrgID.String()))
				if firstErr == nil {
					firstErr = serr
				}
				continue
			}
			sent++
		}
		// A retry would mail again everybody who already has it: retry only
		// when nobody got the letter.
		if sent == 0 {
			return firstErr
		}
		return nil
	}
}

// ReferralPayoutEmailOptions are the dependencies of referral.organizer_email.
type ReferralPayoutEmailOptions = ReferralReversalEmailOptions

// NewReferralPayoutEmailHandler builds the worker handler of
// referral.JobTypeOrganizerEmail: the letter to an organization's owners about
// cashback payouts (owner decision 03.11.2026) — they wait for the Stripe
// balance, Stripe holds them pending, or one was not paid and the organizer
// settles with the referrer directly. The payload carries the figures the
// sweep counted when it marked the rewards as told, so the letter says what
// the chat said.
func NewReferralPayoutEmailHandler(opts ReferralPayoutEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: referral payout templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	return func(ctx context.Context, payload []byte) error {
		var p referral.PayoutNotice
		if err := json.Unmarshal(payload, &p); err != nil || p.OrgID == uuid.Nil {
			return nil // a payload nobody can read is never going to work
		}
		switch p.Kind {
		case referral.NoticeBalance, referral.NoticePending, referral.NoticeUnpaid:
		default:
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: referral payout e-mail skipped, no production e-mail sender configured")
			return nil
		}
		var orgName string
		if err := opts.DB.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, p.OrgID).Scan(&orgName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		recipients, err := salesAlertRecipients(ctx, opts.DB, p.OrgID, false)
		if err != nil {
			return err
		}
		if len(recipients) == 0 {
			logger.Error("delivery: referral payout notice has no recipient in the organization", slog.String("org_id", p.OrgID.String()))
			return nil
		}
		data := templates.Data{
			Branding: templates.Branding{OrgName: orgName},
			Referral: &templates.ReferralData{
				Notice: p.Kind, Count: p.Count, Amount: formatMoney(p.AmountMinor, p.Currency),
				Code: p.Code, Order: strconv.FormatInt(p.FriendOrderNumber, 10),
				BalanceTimeout: p.Reason == referral.ReasonBalanceTimeout,
			},
		}
		sent := 0
		var firstErr error
		for _, rc := range recipients {
			data.RecipientEmail = rc.email
			out, rerr := renderer.Render(templates.TemplateKindReferralPayoutOrganizer, rc.locale, data)
			if rerr != nil {
				return rerr
			}
			if serr := opts.Sender.Send(ctx, email.Message{To: rc.email, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}); serr != nil {
				logger.Warn("delivery: referral payout e-mail not sent", slog.String("org_id", p.OrgID.String()))
				if firstErr == nil {
					firstErr = serr
				}
				continue
			}
			sent++
		}
		// A retry would mail again everybody who already has it.
		if sent == 0 {
			return firstErr
		}
		return nil
	}
}
