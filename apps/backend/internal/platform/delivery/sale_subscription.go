// sale_subscription.go - the two letters of "notify me when sales open"
// (package salesubscribe, owner decisions 2026-10-31):
//
//   - sales_state.subscribe_confirm_email asks the buyer to confirm the
//     address. The token is minted HERE, at send time, and only its hash is
//     stored, so the link never sits in worker_jobs. A retry mints a new one.
//   - sales_state.sale_open_email tells a confirmed address that sales opened,
//     with a link to the page it subscribed on, else the event's page.
//
// The language is the one the buyer subscribed in, else the organization's,
// else English (templates.ResolveLocale falls back by itself).
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/publicurl"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// SaleLetterOptions are the dependencies of both letters.
type SaleLetterOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        ScannedAlertDB
	// APIBaseURL is arena-api's public base: the confirmation link points at
	// GET /v1/public/sale-subscriptions/confirm there.
	APIBaseURL string
	// TicketsBaseURL builds the event's page when the subscription named no page.
	TicketsBaseURL string
	Logger         *slog.Logger
}

type saleSubscription struct {
	id                    uuid.UUID
	email, locale, page   string
	status                string
	sessionID, orgID      uuid.UUID
	orgLocale             string
	orgSlug, promoterSlug string
	eventSlug             string
}

func loadSaleSubscription(ctx context.Context, db ScannedAlertDB, id uuid.UUID) (saleSubscription, error) {
	var s saleSubscription
	err := db.QueryRow(ctx, `
		SELECT sub.id, sub.email, sub.locale, sub.page_url, sub.status, sub.session_id, sub.org_id,
		       COALESCE(o.default_locale, ''), COALESCE(o.slug, ''), COALESCE(p.slug, ''), COALESCE(e.slug, '')
		FROM   session_sale_subscriptions sub
		JOIN   organizations o ON o.id = sub.org_id
		JOIN   events e        ON e.id = sub.event_id
		LEFT   JOIN event_promoters ep ON ep.event_id = e.id
		LEFT   JOIN org_promoters   p  ON p.id = ep.promoter_id
		WHERE  sub.id = $1`, id).Scan(
		&s.id, &s.email, &s.locale, &s.page, &s.status, &s.sessionID, &s.orgID,
		&s.orgLocale, &s.orgSlug, &s.promoterSlug, &s.eventSlug)
	return s, err
}

func (s saleSubscription) letterLocale() string {
	if s.locale != "" {
		return s.locale
	}
	if s.orgLocale != "" {
		return s.orgLocale
	}
	return "en"
}

func saleJobSubscription(payload []byte) (uuid.UUID, bool) {
	var p salesubscribe.JobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(p.SubscriptionID)
	return id, err == nil
}

func saleRenderer(opts SaleLetterOptions) (*templates.Renderer, error) {
	if opts.Templates != nil {
		return opts.Templates, nil
	}
	return templates.New()
}

// sendSaleLetter renders kind for the subscription and sends it.
func sendSaleLetter(ctx context.Context, opts SaleLetterOptions, r *templates.Renderer, kind string, s saleSubscription, link string) error {
	in, err := salesalert.LoadSessionInfo(ctx, opts.DB, s.orgID, s.sessionID)
	if errors.Is(err, salesalert.ErrSessionGone) {
		return nil
	}
	if err != nil {
		return err
	}
	whiteLabel, brand := whiteLabelForSession(ctx, opts.DB, opts.Logger, s.sessionID)
	data := templates.Data{
		EventName:      in.EventName,
		RecipientEmail: s.email,
		Branding:       templates.Branding{OrgName: in.OrgName},
		SaleNotice:     templates.SaleNoticeData{URL: link, When: in.When(), Venue: in.Venue},
		WhiteLabel:     whiteLabel,
	}
	out, err := r.Render(kind, s.letterLocale(), data)
	if err != nil {
		return err
	}
	msg := email.Message{To: s.email, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}
	applyWhiteLabelSender(&msg, whiteLabel, brand)
	return opts.Sender.Send(ctx, msg)
}

// NewSaleConfirmHandler builds the handler of salesubscribe.JobTypeConfirmEmail.
func NewSaleConfirmHandler(opts SaleLetterOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		id, ok := saleJobSubscription(payload)
		if !ok {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: sale subscription confirmation skipped, no production e-mail sender configured")
			return nil
		}
		base := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
		if base == "" {
			return fmt.Errorf("delivery: sale subscription confirmation needs the API public base URL")
		}
		r, err := saleRenderer(opts)
		if err != nil {
			return err
		}
		s, err := loadSaleSubscription(ctx, opts.DB, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.status != "pending" {
			return nil // confirmed in the meantime
		}
		token, err := salesubscribe.NewToken()
		if err != nil {
			return err
		}
		tag, err := opts.DB.Exec(ctx, `
			UPDATE session_sale_subscriptions SET confirm_token_hash = $2, confirm_sent_at = $3
			WHERE id = $1 AND status = 'pending'`, id, salesubscribe.HashToken(token), time.Now().UTC())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		link := base + "/v1/public/sale-subscriptions/confirm?token=" + url.QueryEscape(token)
		return sendSaleLetter(ctx, opts, r, templates.TemplateKindSaleSubscribeConfirm, s, link)
	}
}

// NewSaleOpenHandler builds the handler of salesubscribe.JobTypeOpenEmail.
func NewSaleOpenHandler(opts SaleLetterOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		id, ok := saleJobSubscription(payload)
		if !ok {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: sale-open letter skipped, no production e-mail sender configured")
			return nil
		}
		r, err := saleRenderer(opts)
		if err != nil {
			return err
		}
		s, err := loadSaleSubscription(ctx, opts.DB, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		link := SaleBuyLink(s.page, opts.TicketsBaseURL, s.orgSlug, s.promoterSlug, s.eventSlug)
		if link == "" {
			logger.Error("delivery: sale-open letter has no page to link to", slog.String("subscription_id", id.String()))
			return nil
		}
		return sendSaleLetter(ctx, opts, r, templates.TemplateKindSaleOpen, s, link)
	}
}

// SaleBuyLink is where a sale-open letter sends the buyer: the page the
// subscription was made on, else the event's page on the tickets site, else
// the tickets site itself.
func SaleBuyLink(page, ticketsBase, orgSlug, promoterSlug, eventSlug string) string {
	if p := salesubscribe.CleanPageURL(page); p != "" {
		return p
	}
	if p := publicurl.Page(ticketsBase, orgSlug, promoterSlug, eventSlug); p != "" {
		return p
	}
	return strings.TrimRight(strings.TrimSpace(ticketsBase), "/")
}
