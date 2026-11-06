// payment_reconcile.go — the second pass of checkout.payment_recover: the
// reconciliation of hosted payments nobody confirmed (spec 62 §5.4 п. 5,
// П-15).
//
// The first pass (payment_recover.go) settles attempts that never got their
// page. This one looks at attempts whose page WAS handed out but whose
// outcome never arrived — the provider's notification was lost, or it is
// still on its way. Each is asked about by its own reference (AllPay
// order_id) at most every ReconcileEvery, from ReconcileMinAge after it was
// created until ReconcileMargin past the end of its hold. A paid answer goes
// through the webhook's own state machine (the same Apply the first pass
// uses), which also covers the late cases: an order the buyer cancelled or
// that expired meanwhile is re-acquired or refunded there (hcheckout
// late_payment.go). Anything else only stamps provider_checked_at.
package hfeed

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// Defaults of the reconciliation pass.
const (
	// ReconcileEvery is the gap between two questions about one payment.
	ReconcileEvery = 5 * time.Minute
	// ReconcileMinAge leaves a fresh page to its own notification first.
	ReconcileMinAge = 3 * time.Minute
	// ReconcileMargin is how long after the hold ended a payment is still
	// asked about.
	ReconcileMargin = 15 * time.Minute
)

// UnsettledPaymentStore is the database side of the reconciliation
// (gen.Queries). A PaymentRecoverStore that does not implement it skips the
// pass.
type UnsettledPaymentStore interface {
	ListUnsettledHostedPayments(ctx context.Context, provider string, createdBefore, checkedBefore, holdEndedAfter time.Time, limit int32) ([]gen.UnsettledHostedPaymentRow, error)
	MarkPaymentIntentProviderChecked(ctx context.Context, id uuid.UUID) error
}

// ReconcileStats counts what one reconciliation pass did.
type ReconcileStats struct {
	Asked   int
	Settled int
	Failed  int
}

// ReconcileUnsettledPayments runs one reconciliation pass over at most limit
// payments per provider.
func (p *PaymentRecoverer) ReconcileUnsettledPayments(ctx context.Context, limit int32) (ReconcileStats, error) {
	var stats ReconcileStats
	if p == nil || p.Provider == nil {
		return stats, nil
	}
	store, ok := p.Store.(UnsettledPaymentStore)
	if !ok {
		return stats, nil
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	providers := p.Providers
	if len(providers) == 0 {
		providers = RecoverableProviders()
	}
	t := now()
	for _, provider := range providers {
		rows, err := store.ListUnsettledHostedPayments(ctx, provider,
			t.Add(-ReconcileMinAge), t.Add(-ReconcileEvery), t.Add(-ReconcileMargin), limit)
		if err != nil {
			return stats, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			stats.Asked++
			p.reconcileOne(ctx, logger, store, row, &stats)
		}
	}
	return stats, nil
}

func (p *PaymentRecoverer) reconcileOne(ctx context.Context, logger *slog.Logger, store UnsettledPaymentStore,
	row gen.UnsettledHostedPaymentRow, stats *ReconcileStats) {
	log := logger.With(
		slog.String("payment_intent_id", row.PaymentIntentID.String()),
		slog.String("checkout_session_id", row.CheckoutSessionID.String()),
		slog.String("provider", row.Provider),
	)
	// Stamped first: a provider that hangs or errors is asked again only
	// after ReconcileEvery, never on every run.
	if err := store.MarkPaymentIntentProviderChecked(ctx, row.PaymentIntentID); err != nil {
		stats.Failed++
		log.Warn("payment_reconcile: stamping the question failed", slog.String("error", err.Error()))
		return
	}
	lk, err := p.Provider.LookupHostedPayment(ctx, row.OrgID, row.Provider, row.ProviderPaymentID)
	if err != nil {
		stats.Failed++
		log.Warn("payment_reconcile: the provider could not be asked", slog.String("error", err.Error()))
		return
	}
	if lk.Result.State != payments.HostedPaymentSettled || p.Apply == nil {
		return
	}
	src := hcheckout.ConfirmedPaymentSource{OrgID: row.OrgID, ConfigID: lk.ConfigID, Provider: lk.Provider}
	if err := p.Apply(ctx, src, lk.Result.Event); err != nil {
		stats.Failed++
		log.Error("payment_reconcile: applying the provider's answer failed", slog.String("error", err.Error()))
		return
	}
	stats.Settled++
	log.Info("payment_reconcile: a payment without its notification was settled from the provider's answer",
		slog.String("event_kind", string(lk.Result.Event.Kind)))
}
