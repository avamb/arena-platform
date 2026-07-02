// Package hcheckout implements HTTP handlers for the checkout domain:
// checkout sessions, reservations, payment intents, refunds, promo codes,
// price breakdown, and the reservation background processor.
package hcheckout

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hcheckout requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// PricingRules — pluggable fee / tax configuration
// ─────────────────────────────────────────────────────────────────────────────

// PricingRules holds the platform's fee and tax rates expressed in basis points
// (1 bp = 0.01 %; 10 000 bp = 100 %).
//
// All rates default to zero (no fee / no tax) when PricingRules is not
// explicitly configured, giving a safe zero-value default.
//
// Example:
//
//	PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 0}
//	→ 5 % platform fee, 2 % provider fee, no tax.
type PricingRules struct {
	// PlatformFeeRate is the platform service charge in basis points.
	// Applied to (subtotal - discount). 500 = 5 %.
	PlatformFeeRate int64

	// ProviderFeeRate is the payment-provider processing fee in basis points.
	// Applied to (subtotal - discount). 200 = 2 %.
	ProviderFeeRate int64

	// TaxRate is the applicable sales/VAT tax in basis points.
	// Applied to (subtotal - discount). 0 = tax-exempt.
	TaxRate int64
}

// ─────────────────────────────────────────────────────────────────────────────
// PricingBreakdown — itemized pipeline result
// ─────────────────────────────────────────────────────────────────────────────

// PricingBreakdown is the result of running ComputePricing. Every field is
// expressed in the smallest currency unit (integer cents).
//
// Invariant (enforced by ComputePricing):
//
//	Discount + PlatformFee + ProviderFee + Tax + (Total - Subtotal) == 0
//
// In other words: Subtotal - Discount + PlatformFee + ProviderFee + Tax == Total.
type PricingBreakdown struct {
	UnitPrice   int64  `json:"unit_price"`   // price per single ticket
	Quantity    int32  `json:"quantity"`     // number of tickets
	Subtotal    int64  `json:"subtotal"`     // UnitPrice × Quantity
	Discount    int64  `json:"discount"`     // promo discount (≥ 0, ≤ Subtotal)
	PlatformFee int64  `json:"platform_fee"` // platform service charge
	ProviderFee int64  `json:"provider_fee"` // payment-provider processing fee
	Tax         int64  `json:"tax"`          // sales / VAT tax
	Total       int64  `json:"total"`        // all-in amount the customer pays
	Currency    string `json:"currency"`     // ISO 4217 currency code
}

// ─────────────────────────────────────────────────────────────────────────────
// ComputePricing — pure pipeline function
// ─────────────────────────────────────────────────────────────────────────────

// ComputePricing runs the pricing pipeline and returns an itemized breakdown.
//
//   - unitPrice is the per-ticket price in smallest currency units (cents).
//   - quantity is the number of tickets (must be ≥ 1; caller's responsibility).
//   - discount is the total promo-code discount already computed by
//     computeDiscount (≥ 0; capped at subtotal if larger).
//   - currency is the ISO 4217 code (e.g. "ILS", "USD").
//   - rules carries the platform/provider/tax basis-point rates.
//
// All intermediate values use integer arithmetic (floor division) to avoid
// floating-point rounding drift.  The pipeline guarantees:
//
//	(Subtotal - Discount) + PlatformFee + ProviderFee + Tax == Total
func ComputePricing(unitPrice int64, quantity int32, discount int64, currency string, rules PricingRules) PricingBreakdown {
	subtotal := unitPrice * int64(quantity)

	// Cap discount so it never exceeds the subtotal.
	if discount > subtotal {
		discount = subtotal
	}
	if discount < 0 {
		discount = 0
	}

	discounted := subtotal - discount

	// All fees/taxes are applied to the discounted amount (post-promo base).
	platformFee := discounted * rules.PlatformFeeRate / 10_000
	providerFee := discounted * rules.ProviderFeeRate / 10_000
	tax := discounted * rules.TaxRate / 10_000

	total := discounted + platformFee + providerFee + tax

	return PricingBreakdown{
		UnitPrice:   unitPrice,
		Quantity:    quantity,
		Subtotal:    subtotal,
		Discount:    discount,
		PlatformFee: platformFee,
		ProviderFee: providerFee,
		Tax:         tax,
		Total:       total,
		Currency:    currency,
	}
}

// Handler holds the shared dependencies for all checkout HTTP handlers.
type Handler struct {
	checkoutQueries      *gen.Queries
	reservationQueries   *gen.Queries
	inventoryQueries     *gen.Queries
	paymentIntentQueries *gen.Queries
	refundQueries        *gen.Queries
	promoQueries         *gen.Queries
	tierQueries          *gen.Queries
	ticketQueries        *gen.Queries
	channelQueries       *gen.Queries
	orgQueries           *gen.Queries
	pool                 TxStarter
	logger               *slog.Logger
	pricingRules         PricingRules

	// Callback fields for cross-domain side effects.
	issueTickets      func(ctx context.Context, cs gen.CheckoutSessionRow) ([]gen.TicketRow, error)
	enqueueDelivery   func(ctx context.Context, tickets []gen.TicketRow)
	publishRefunded   func(ctx context.Context, checkoutSessionID, refundID, currency string, amount int64)
	publishRefundedV1 func(ctx context.Context, ticketIDs []string, checkoutSessionID, refundID, currency string, amount int64)
}

// New constructs a Handler from the caller's dependencies.
func New(
	checkoutQ *gen.Queries,
	reservationQ *gen.Queries,
	inventoryQ *gen.Queries,
	paymentIntentQ *gen.Queries,
	refundQ *gen.Queries,
	promoQ *gen.Queries,
	tierQ *gen.Queries,
	ticketQ *gen.Queries,
	channelQ *gen.Queries,
	orgQ *gen.Queries,
	pool TxStarter,
	logger *slog.Logger,
	pricingRules PricingRules,
	issueTickets func(ctx context.Context, cs gen.CheckoutSessionRow) ([]gen.TicketRow, error),
	enqueueDelivery func(ctx context.Context, tickets []gen.TicketRow),
	publishRefunded func(ctx context.Context, checkoutSessionID, refundID, currency string, amount int64),
	publishRefundedV1 func(ctx context.Context, ticketIDs []string, checkoutSessionID, refundID, currency string, amount int64),
) *Handler {
	return &Handler{
		checkoutQueries:      checkoutQ,
		reservationQueries:   reservationQ,
		inventoryQueries:     inventoryQ,
		paymentIntentQueries: paymentIntentQ,
		refundQueries:        refundQ,
		promoQueries:         promoQ,
		tierQueries:          tierQ,
		ticketQueries:        ticketQ,
		channelQueries:       channelQ,
		orgQueries:           orgQ,
		pool:                 pool,
		logger:               logger,
		pricingRules:         pricingRules,
		issueTickets:         issueTickets,
		enqueueDelivery:      enqueueDelivery,
		publishRefunded:      publishRefunded,
		publishRefundedV1:    publishRefundedV1,
	}
}
