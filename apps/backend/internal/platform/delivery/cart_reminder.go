// cart_reminder.go - the two letters of spec 51-1:
//
//   - checkout.reminder_email is the ONE reminder about an unpaid widget
//     order (package cartreminder). It checks the order again before it goes,
//     records itself in email_letters like every buyer letter, and marks
//     checkout_reminders sent or skipped.
//   - marketing.confirm_email asks a buyer who ticked the marketing consent to
//     confirm the address (package marketing). The token is minted here, at
//     send time, and only its hash is stored. It is not a buyer-order letter
//     and is not recorded in email_letters.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/cartreminder"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// GrowthLetterOptions are the dependencies of both letters.
type GrowthLetterOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        emailstatus.DB
	// APIBaseURL is arena-api's public base: the reminder's resume link and
	// the confirmation and withdraw links point there.
	APIBaseURL string
	Logger     *slog.Logger
	// Now is time.Now when nil (tests pin it).
	Now func() time.Time
}

func (o GrowthLetterOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o GrowthLetterOptions) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

func (o GrowthLetterOptions) renderer() (*templates.Renderer, error) {
	if o.Templates != nil {
		return o.Templates, nil
	}
	return templates.New()
}

// ResumePath is the API path the reminder links to: it sends the buyer to
// the payment page while it lives, else back to their own page.
func ResumePath(checkoutToken string) string {
	return "/v1/public/checkout/" + url.PathEscape(checkoutToken) + "/resume?src=reminder"
}

// venueClock prints t on the venue's clock (Madrid when the venue has none).
func venueClock(t time.Time, zone string) string {
	loc, err := time.LoadLocation(zone)
	if zone == "" || err != nil {
		if loc, err = time.LoadLocation("Europe/Madrid"); err != nil {
			return t.UTC().Format("02.01.2006 15:04") + " UTC" // allow:timeformat
		}
	}
	return t.In(loc).Format("02.01.2006 15:04") // allow:timeformat
}

// NewCartReminderHandler builds the handler of cartreminder.JobTypeEmail.
func NewCartReminderHandler(opts GrowthLetterOptions) worker.HandlerFunc {
	logger := opts.logger()
	return func(ctx context.Context, payload []byte) error {
		var p cartreminder.JobPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil
		}
		orderID, err := uuid.Parse(p.OrderID)
		if err != nil {
			return nil
		}
		now := opts.now()
		o, err := cartreminder.LoadOrder(ctx, opts.DB, orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		skip := cartreminder.Decide(o, now)
		if skip == "" && (opts.Sender == nil || email.IsDevOnly(opts.Sender)) {
			skip = cartreminder.SkipNoSender
		}
		base := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
		if skip == "" && base == "" {
			return fmt.Errorf("delivery: the cart reminder needs the API public base URL")
		}
		var in salesalert.SessionInfo
		if skip == "" {
			in, err = salesalert.LoadSessionInfo(ctx, opts.DB, o.OrgID, o.SessionID)
			if errors.Is(err, salesalert.ErrSessionGone) {
				skip = cartreminder.SkipSessionGone
			} else if err != nil {
				return err
			}
		}
		if skip != "" {
			logger.Info("delivery: cart reminder skipped", slog.String("order_id", orderID.String()), slog.String("reason", skip))
			return cartreminder.Mark(ctx, opts.DB, orderID, skip, now)
		}
		r, err := opts.renderer()
		if err != nil {
			return err
		}
		locale := o.Locale
		if locale == "" {
			locale = in.OrgLocale
		}
		data := templates.Data{
			EventName:      in.EventName,
			RecipientEmail: o.BuyerEmail,
			Branding:       templates.Branding{OrgName: in.OrgName},
			CartReminder: templates.CartReminderData{
				URL:         base + ResumePath(o.CheckoutToken),
				When:        in.When(),
				HoldUntil:   venueClock(*o.ExpiresAt, in.Timezone),
				Venue:       in.Venue,
				Tickets:     o.Tickets,
				Total:       formatMoney(o.Total, o.Currency),
				OrderNumber: strconv.FormatInt(o.SystemID, 10),
			},
		}
		out, err := r.Render(templates.TemplateKindCheckoutReminder, locale, data)
		if err != nil {
			return err
		}
		orderRef := o.ID
		letter := emailstatus.NewLetter{
			OrgID:           o.OrgID,
			OrderID:         &orderRef,
			Kind:            templates.TemplateKindCheckoutReminder,
			Recipient:       o.BuyerEmail,
			TemplateVersion: templateVersion(r, templates.TemplateKindCheckoutReminder, locale),
		}
		msg := email.Message{To: o.BuyerEmail, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}
		if err := sendRecorded(ctx, opts.Sender, opts.DB, logger, letter, msg, nil); err != nil {
			return err
		}
		return cartreminder.Mark(ctx, opts.DB, orderID, "", now)
	}
}

// NewMarketingConfirmHandler builds the handler of marketing.JobTypeConfirmEmail.
func NewMarketingConfirmHandler(opts GrowthLetterOptions) worker.HandlerFunc {
	logger := opts.logger()
	return func(ctx context.Context, payload []byte) error {
		var p marketing.JobPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil
		}
		id, err := uuid.Parse(p.SubscriptionID)
		if err != nil {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: marketing confirmation skipped, no production e-mail sender configured")
			return nil
		}
		base := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
		if base == "" {
			return fmt.Errorf("delivery: the marketing confirmation needs the API public base URL")
		}
		var (
			addr, locale, status, withdraw, orgName, orgLocale string
		)
		err = opts.DB.QueryRow(ctx, `
			SELECT s.email, s.locale, s.status, s.unsubscribe_token, o.name, COALESCE(o.default_locale, '')
			FROM   marketing_subscriptions s JOIN organizations o ON o.id = s.org_id
			WHERE  s.id = $1`, id).Scan(&addr, &locale, &status, &withdraw, &orgName, &orgLocale)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != marketing.StatusPending {
			return nil // confirmed or withdrawn meanwhile
		}
		token, err := marketing.NewToken()
		if err != nil {
			return err
		}
		tag, err := opts.DB.Exec(ctx, `
			UPDATE marketing_subscriptions SET confirm_token_hash = $2, confirm_sent_at = $3
			WHERE id = $1 AND status = 'pending'`, id, marketing.HashToken(token), opts.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if locale == "" {
			locale = orgLocale
		}
		r, err := opts.renderer()
		if err != nil {
			return err
		}
		out, err := r.Render(templates.TemplateKindMarketingConfirm, locale, templates.Data{
			RecipientEmail: addr,
			Branding:       templates.Branding{OrgName: orgName},
			Marketing: templates.MarketingConfirmData{
				ConfirmURL:  base + marketing.ConfirmPath(url.QueryEscape(token)),
				WithdrawURL: base + marketing.WithdrawPath(url.QueryEscape(withdraw)),
			},
		})
		if err != nil {
			return err
		}
		return opts.Sender.Send(ctx, email.Message{To: addr, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody})
	}
}
