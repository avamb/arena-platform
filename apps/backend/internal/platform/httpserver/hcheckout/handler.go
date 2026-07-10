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
//
// Multi-line breakdown (feature #310, SEAT-C2): when a checkout covers more
// than one ticket tier (e.g. a seated reservation whose seats resolve to
// different tiers), Lines carries one entry per tier group and the top-level
// UnitPrice / Quantity fields are populated with the weighted-average unit
// price and total quantity so single-tier callers keep the same shape.
type PricingBreakdown struct {
	UnitPrice   int64             `json:"unit_price"`   // price per single ticket (weighted avg on multi-line)
	Quantity    int32             `json:"quantity"`     // number of tickets (sum across lines)
	Subtotal    int64             `json:"subtotal"`     // Σ (line.UnitPrice × line.Quantity)
	Discount    int64             `json:"discount"`     // promo discount (≥ 0, ≤ Subtotal)
	PlatformFee int64             `json:"platform_fee"` // platform service charge
	ProviderFee int64             `json:"provider_fee"` // payment-provider processing fee
	Tax         int64             `json:"tax"`          // sales / VAT tax
	Total       int64             `json:"total"`        // all-in amount the customer pays
	Currency    string            `json:"currency"`     // ISO 4217 currency code
	Lines       []PricingLineItem `json:"lines,omitempty"`
}

// PricingLineItem is one row of a multi-line pricing breakdown. Every seated
// reservation produces one line per (tier_id, unit_price) group; the sum of
// line subtotals equals the top-level Subtotal.
//
// TierID is the string form of the tier UUID; on general-admission checkouts
// the single line uses the reservation's tier_id (or an empty string when
// tier_id is nil).
type PricingLineItem struct {
	TierID    string `json:"tier_id"`
	Quantity  int32  `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
	Subtotal  int64  `json:"subtotal"`
}

// PricingLineInput is a caller-provided (tier_id, quantity, unit_price) tuple
// consumed by ComputePricingLines. The caller is responsible for grouping
// seats by tier before invocation — ComputePricingLines does no grouping.
type PricingLineInput struct {
	TierID    string
	Quantity  int32
	UnitPrice int64
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

// ComputePricingLines runs the pricing pipeline over a set of tier-group lines
// (feature #310, SEAT-C2). Each PricingLineInput is one (tier_id, quantity,
// unit_price) group; the sum of line subtotals feeds the same discount / fee /
// tax pipeline used by single-tier ComputePricing.
//
// Contract:
//
//   - lines must be non-empty; every line.Quantity must be > 0 and
//     every line.UnitPrice must be ≥ 0. Invariants outside that range are the
//     caller's responsibility (this matches the ComputePricing contract).
//
//   - The returned PricingBreakdown.Lines echoes back the input lines with a
//     computed Subtotal per line (Quantity × UnitPrice) so the API surface can
//     serialise the tier group breakdown verbatim.
//
//   - Top-level Subtotal is Σ line.Subtotal; Quantity is Σ line.Quantity; and
//     UnitPrice is the integer floor of Subtotal / Quantity (weighted-average
//     unit price — informational only; per-line UnitPrice is the source of
//     truth). Discount, PlatformFee, ProviderFee, Tax and Total are computed
//     over the aggregate the exact same way as the single-tier path so the
//     accounting invariant still holds:
//
//     (Subtotal − Discount) + PlatformFee + ProviderFee + Tax == Total
//
// Guardrail #15 (financial totals computed platform-side): the caller supplies
// only unit prices and quantities; discount / fees / tax are always derived
// server-side from the pricing rules.
func ComputePricingLines(lines []PricingLineInput, discount int64, currency string, rules PricingRules) PricingBreakdown {
	out := make([]PricingLineItem, 0, len(lines))
	var subtotal int64
	var quantity int32
	for _, l := range lines {
		lineSubtotal := l.UnitPrice * int64(l.Quantity)
		subtotal += lineSubtotal
		quantity += l.Quantity
		out = append(out, PricingLineItem{
			TierID:    l.TierID,
			Quantity:  l.Quantity,
			UnitPrice: l.UnitPrice,
			Subtotal:  lineSubtotal,
		})
	}

	// Cap discount so it never exceeds the subtotal.
	if discount > subtotal {
		discount = subtotal
	}
	if discount < 0 {
		discount = 0
	}

	discounted := subtotal - discount
	platformFee := discounted * rules.PlatformFeeRate / 10_000
	providerFee := discounted * rules.ProviderFeeRate / 10_000
	tax := discounted * rules.TaxRate / 10_000
	total := discounted + platformFee + providerFee + tax

	// Weighted-average unit price (informational — per-line UnitPrice is
	// source of truth). Zero quantity is degenerate (no lines) so we leave
	// UnitPrice at zero rather than panicking.
	var avgUnit int64
	if quantity > 0 {
		avgUnit = subtotal / int64(quantity)
	}

	return PricingBreakdown{
		UnitPrice:   avgUnit,
		Quantity:    quantity,
		Subtotal:    subtotal,
		Discount:    discount,
		PlatformFee: platformFee,
		ProviderFee: providerFee,
		Tax:         tax,
		Total:       total,
		Currency:    currency,
		Lines:       out,
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
