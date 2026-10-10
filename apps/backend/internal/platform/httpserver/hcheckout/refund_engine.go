// refund_engine.go — hcheckout's side of the refund engine
// (internal/platform/refunds, PAY-03): the ModuleSource that builds an
// organization's payment module from its own configuration, and the hook
// the flat refund routes use to refuse a payment arena cannot refund
// through and to drive an approved refund for real.
package hcheckout

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// RefundModuleSource builds payment modules for the refund engine from the
// module registry and each organization's payment_provider_configs row —
// the same resolution hosted checkout uses (ResolveProviderConfig), so a
// refund goes through the configuration the organization sells with.
type RefundModuleSource struct {
	queries *gen.Queries
	opts    payments.Options
}

// NewRefundModuleSource builds a RefundModuleSource. opts carries the test
// seams (stub provider endpoints); empty means the real providers.
func NewRefundModuleSource(q *gen.Queries, opts payments.Options) *RefundModuleSource {
	return &RefundModuleSource{queries: q, opts: opts}
}

// Descriptor implements refunds.ModuleSource.
func (s *RefundModuleSource) Descriptor(provider string) (payments.Descriptor, bool) {
	e, ok := paymodules.Registry().Get(provider)
	if !ok {
		return payments.Descriptor{}, false
	}
	return e.Descriptor, e.Implemented()
}

// Build implements refunds.ModuleSource.
func (s *RefundModuleSource) Build(ctx context.Context, orgID uuid.UUID, provider string) (payments.Module, error) {
	cfg, cfgErr := ResolveProviderConfig(ctx, s.queries, orgID, provider)
	if cfgErr != nil {
		if cfgErr.Transient {
			// The configuration could not be read: an unknown outcome the
			// engine retries, never a refusal.
			return nil, cfgErr
		}
		return nil, &refunds.ConfigError{Code: cfgErr.Code, Message: cfgErr.Message}
	}
	return paymodules.Registry().Build(provider, payments.SecretsFromJSON(cfg.Secrets), s.opts)
}

// WithRefundEngine wires the refund engine into the flat refund routes.
// Without it, approving a refund of a payment arena can refund through
// answers 503 and writes nothing — it never falls back to the old
// "pretend the provider was called" behaviour.
func (h *Handler) WithRefundEngine(e *refunds.Engine) *Handler {
	h.refundEngine = e
	return h
}

// refundRoute decides how the money of pi goes back, reading the order
// through q (the pool before a transaction, the transaction's own Queries
// inside one). The order source is "" when the order is unknown.
func refundRoute(ctx context.Context, q *gen.Queries, pi gen.PaymentIntentRow) refunds.Route {
	src := ""
	if pi.CheckoutSessionID != nil && q != nil {
		if o, err := q.GetOrderByCheckoutSession(ctx, *pi.CheckoutSessionID); err == nil {
			src = o.Source
		}
	}
	return refunds.RouteFor(registryModules{}, pi.Provider, src)
}

// registryModules answers descriptors only, for routing when no engine is
// wired (building a module is never needed to decide a route).
type registryModules struct{}

func (registryModules) Descriptor(provider string) (payments.Descriptor, bool) {
	return (&RefundModuleSource{}).Descriptor(provider)
}

func (registryModules) Build(context.Context, uuid.UUID, string) (payments.Module, error) {
	return nil, &refunds.ConfigError{Code: refunds.CodeEngineUnavailable, Message: "refund engine is not wired"}
}
