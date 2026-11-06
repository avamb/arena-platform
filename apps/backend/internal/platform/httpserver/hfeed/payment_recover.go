// payment_recover.go — the checkout.payment_recover job's work (П-19, spec 62
// §5.4 п. 2.4).
//
// checkout/start records the payment attempt BEFORE it calls a provider that
// files the payment under arena's own reference (AllPay: order_id = the
// checkout session id; startHostedPayment). If the provider's answer is lost
// or the process dies mid-call, that row stays without a payment page. This
// file finds such rows and asks the provider by the same reference:
//
//   - paid (or paid and already refunded): the event goes through the webhook's
//     own state machine (hcheckout.ApplyProviderConfirmedPayment), so the
//     purchase completes exactly as if the webhook had arrived; a webhook that
//     does arrive later is a no-op;
//   - unknown or unpaid: the payment page is asked for again WITH THE SAME
//     reference and stored, so GET /v1/public/checkout/{token} offers it as
//     payment_url — never a second purchase;
//   - anything else (pending, unreachable): left alone, asked again on the
//     next run while the hold lives.
//
// Only providers declaring Capabilities.ReferenceIsPaymentID are looked at;
// for the others the payment id is only known from the provider's answer,
// so there is nothing to ask about.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// Defaults of PaymentRecoverer.
const (
	// DefaultRecoverMinAge is how old an attempt without a page must be
	// before it is treated as lost: well past any provider call the request
	// itself could still be waiting on.
	DefaultRecoverMinAge = 2 * time.Minute
	// DefaultRecoverMinWindow is the least payment window a page asked for
	// again must still offer the buyer.
	DefaultRecoverMinWindow = 2 * time.Minute
)

// PaymentRecoverStore is the database side of the recovery (gen.Queries).
type PaymentRecoverStore interface {
	ListStalledHostedPaymentAttempts(ctx context.Context, provider string, createdBefore time.Time, limit int32) ([]gen.StalledHostedPaymentAttemptRow, error)
	SetPaymentIntentHostedURL(ctx context.Context, id uuid.UUID, hostedCheckoutURL string) (bool, error)
}

// HostedPaymentLookup is a provider's answer about one recorded attempt,
// with the configuration that read it.
type HostedPaymentLookup struct {
	Result   payments.HostedPaymentLookupResult
	Provider string
	ConfigID uuid.UUID
}

// PaymentRecoverProvider is the provider side (StripePaymentStarter).
type PaymentRecoverProvider interface {
	LookupHostedPayment(ctx context.Context, orgID uuid.UUID, provider, reference string) (HostedPaymentLookup, error)
	StartHostedCheckout(ctx context.Context, req HostedCheckoutRequest) (*HostedCheckoutResult, error)
}

// ConfirmedPaymentApplier runs a read-back payment event through the
// webhook's state machine (hcheckout.Handler.ApplyProviderConfirmedPayment).
type ConfirmedPaymentApplier func(ctx context.Context, src hcheckout.ConfirmedPaymentSource, ev payments.NormalizedEvent) error

// PaymentRecoverer finds recorded payment attempts that never got their
// payment page and settles them with the provider.
type PaymentRecoverer struct {
	Store    PaymentRecoverStore
	Provider PaymentRecoverProvider
	Apply    ConfirmedPaymentApplier
	// ReturnURLs re-validates the stored return page (checkout_sessions.
	// return_url) before it goes to the provider again.
	ReturnURLs ReturnURLPolicy
	// Grace is the widget payment grace: the page expires that long before
	// the hold (WIDGET_PAYMENT_GRACE_SECONDS).
	Grace     time.Duration
	MinAge    time.Duration
	MinWindow time.Duration
	// Providers limits the recovery to these registry names; empty means
	// every hosted provider declaring Capabilities.ReferenceIsPaymentID.
	Providers []string
	Logger    *slog.Logger
	Now       func() time.Time
}

// RecoverStats counts what one run did.
type RecoverStats struct {
	Examined int
	Applied  int
	Relinked int
	Pending  int
	TooLate  int
	Failed   int
}

// RecoverableProviders lists the hosted providers whose attempts can be read
// back by reference.
func RecoverableProviders() []string {
	return paymodules.Registry().NamesWhere(func(d payments.Descriptor) bool {
		return d.Capabilities.HostedCheckout && d.Capabilities.ReferenceIsPaymentID
	})
}

// RecoverStalledPayments runs one pass over at most limit attempts per
// provider. A failure on one attempt is logged and counted, never fatal: the
// next run asks again while the hold lives.
func (p *PaymentRecoverer) RecoverStalledPayments(ctx context.Context, limit int32) (RecoverStats, error) {
	var stats RecoverStats
	if p == nil || p.Store == nil || p.Provider == nil {
		return stats, errors.New("hfeed: payment recoverer is not wired")
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	minAge, minWindow := p.MinAge, p.MinWindow
	if minAge <= 0 {
		minAge = DefaultRecoverMinAge
	}
	if minWindow <= 0 {
		minWindow = DefaultRecoverMinWindow
	}
	providers := p.Providers
	if len(providers) == 0 {
		providers = RecoverableProviders()
	}

	for _, provider := range providers {
		rows, err := p.Store.ListStalledHostedPaymentAttempts(ctx, provider, now().Add(-minAge), limit)
		if err != nil {
			return stats, fmt.Errorf("hfeed: list stalled %s payment attempts: %w", provider, err)
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			stats.Examined++
			p.recoverOne(ctx, logger, now(), minWindow, row, &stats)
		}
	}
	return stats, nil
}

func (p *PaymentRecoverer) recoverOne(
	ctx context.Context,
	logger *slog.Logger,
	now time.Time,
	minWindow time.Duration,
	row gen.StalledHostedPaymentAttemptRow,
	stats *RecoverStats,
) {
	log := logger.With(
		slog.String("payment_intent_id", row.PaymentIntentID.String()),
		slog.String("checkout_session_id", row.CheckoutSessionID.String()),
		slog.String("provider", row.Provider),
	)

	lk, err := p.Provider.LookupHostedPayment(ctx, row.OrgID, row.Provider, row.ProviderPaymentID)
	if err != nil {
		stats.Failed++
		log.Warn("payment_recover: the provider could not be asked about the attempt", slog.String("error", err.Error()))
		return
	}

	switch lk.Result.State {
	case payments.HostedPaymentSettled:
		if p.Apply == nil {
			stats.Failed++
			log.Error("payment_recover: a paid attempt was found but nothing applies payments here")
			return
		}
		src := hcheckout.ConfirmedPaymentSource{OrgID: row.OrgID, ConfigID: lk.ConfigID, Provider: lk.Provider}
		if err := p.Apply(ctx, src, lk.Result.Event); err != nil {
			stats.Failed++
			log.Error("payment_recover: applying the provider's paid answer failed", slog.String("error", err.Error()))
			return
		}
		stats.Applied++
		log.Info("payment_recover: the provider reports the attempt paid; processed as its webhook",
			slog.String("event_kind", string(lk.Result.Event.Kind)))
		return
	case payments.HostedPaymentPending:
		stats.Pending++
		return
	case payments.HostedPaymentUnknown, payments.HostedPaymentUnpaid:
		// Ask for the page again below.
	default:
		stats.Pending++
		return
	}

	pageExpires := row.HoldExpiresAt.Add(-p.Grace)
	if pageExpires.Before(now.Add(minWindow)) {
		stats.TooLate++
		return
	}
	returnBase, err := p.ReturnURLs.Resolve(derefOr(row.ReturnURL))
	if err != nil {
		stats.Failed++
		log.Warn("payment_recover: no page to send the buyer back to; the payment page is not asked for again",
			slog.String("error", err.Error()))
		return
	}
	productName := strings.TrimSpace(row.EventName)
	if productName == "" {
		productName = "Tickets"
	}
	orderID := row.OrderID
	res, err := p.Provider.StartHostedCheckout(ctx, HostedCheckoutRequest{
		OrgID:             row.OrgID,
		ChannelID:         row.ChannelID,
		CheckoutSessionID: row.CheckoutSessionID,
		CheckoutToken:     row.CheckoutToken,
		OrderID:           &orderID,
		Amount:            row.Amount,
		Currency:          strings.TrimSpace(row.Currency),
		ProductName:       productName,
		BuyerEmail:        derefOr(row.BuyerEmail),
		BuyerName:         derefOr(row.BuyerName),
		BuyerPhone:        derefOr(row.BuyerPhone),
		ReturnURL:         returnBase,
		ExpiresAtUnix:     pageExpires.Unix(),
		BuyerLocale:       derefOr(row.BuyerLocale),
		ExpectProvider:    row.Provider,
	})
	if err != nil {
		stats.Failed++
		log.Warn("payment_recover: asking the provider for the payment page again failed", slog.String("error", err.Error()))
		return
	}
	if res.SessionID != row.ProviderPaymentID {
		stats.Failed++
		log.Error("payment_recover: the provider filed the page under another id; it is not stored",
			slog.String("provider_payment_id", row.ProviderPaymentID),
			slog.String("answered_id", res.SessionID))
		return
	}
	if _, err := p.Store.SetPaymentIntentHostedURL(ctx, row.PaymentIntentID, res.RedirectURL); err != nil {
		stats.Failed++
		log.Error("payment_recover: storing the payment page failed", slog.String("error", err.Error()))
		return
	}
	stats.Relinked++
	log.Info("payment_recover: the payment page was asked for again under the same reference and stored")
}

// LookupHostedPayment implements PaymentRecoverProvider: it reads one
// recorded attempt back from the provider it is filed under, with the
// organization's usable configuration of that provider.
func (s *StripePaymentStarter) LookupHostedPayment(ctx context.Context, orgID uuid.UUID, provider, reference string) (HostedPaymentLookup, error) {
	if s == nil || s.orgQueries == nil {
		return HostedPaymentLookup{}, errors.New("hfeed: payment lookup is not configured")
	}
	entry, ok := hostedEntry(provider)
	if !ok || !entry.Descriptor.Capabilities.ReferenceIsPaymentID {
		return HostedPaymentLookup{}, fmt.Errorf("hfeed: %s payments cannot be read back by reference", provider)
	}
	cfg, cfgErr := hcheckout.ResolveProviderConfig(ctx, s.orgQueries, orgID, entry.Descriptor.Name)
	if cfgErr != nil {
		return HostedPaymentLookup{}, fmt.Errorf("hfeed: %s config of org %s: %s", provider, orgID, cfgErr.Message)
	}
	opts := s.opts
	opts.Mode = cfg.Mode
	module, err := entry.Build(payments.SecretsFromJSON(cfg.Secrets), opts)
	if err != nil {
		return HostedPaymentLookup{}, fmt.Errorf("%s lookup (config %s, mode %s): %w", provider, cfg.ID, cfg.Mode, err)
	}
	lookup, ok := module.(payments.HostedPaymentLookup)
	if !ok {
		return HostedPaymentLookup{}, fmt.Errorf("%s lookup: module %T cannot read a payment back", provider, module)
	}
	res, err := lookup.LookupHostedPayment(ctx, reference)
	if err != nil {
		return HostedPaymentLookup{}, fmt.Errorf("%s lookup (config %s, mode %s): %w", provider, cfg.ID, cfg.Mode, err)
	}
	return HostedPaymentLookup{Result: res, Provider: entry.Descriptor.Name, ConfigID: cfg.ID}, nil
}
