// Package ordering owns the order aggregate lifecycle: turning a
// pricing-confirmed checkout session into an explicit orders /order_items/
// order_events triple, moving it through paid / cancelled / expired, and
// reconciling the cart lines a WooCommerce site reports against the hold we
// actually keep (W1-A6b, feature #487; spec 08_architecture/
// 18_bil24_compat_wave1_specification_ru.md §14.1, §7.7).
//
// Two deliberate non-goals keep this package honest:
//
//   - It never prices anything. Money always arrives already computed —
//     either from the confirmed checkout session (subtotal/discount/fees) or
//     from reservation_ga_items.unit_price. ComputePricingLines in hcheckout
//     stays the single pricing authority (spec §14.1); ordering only
//     *distributes* those totals across units.
//   - It never imports internal/platform/httpserver/... . The direction of
//     the dependency is the other way around: hcheckout, hfeed and hbil24
//     call into ordering once epic step 3 lands, so a back-import here would
//     be a cycle. Hold mutation (ExtendHold/ShrinkHold) therefore arrives as
//     injected function values — see HoldMutators in reconcile.go.
//
// Every entry point takes a *gen.Queries that the caller has already bound to
// its transaction, so an order and its items and its audit event are always
// written atomically with whatever the caller is doing (issuing tickets,
// confirming a checkout, answering CREATE_ORDER_EXT).
package ordering

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Vocabulary
// ─────────────────────────────────────────────────────────────────────────────

// Order status values, mirroring the CHECK constraint on orders.status in
// migration 0092. Kept as constants so a typo becomes a compile error instead
// of a 23514 at runtime.
const (
	StatusPendingPayment = "pending_payment"
	StatusPaid           = "paid"
	StatusCancelled      = "cancelled"
	StatusExpired        = "expired"
)

// Order source values, mirroring the CHECK constraint on orders.source.
const (
	SourceBil24Gateway  = "bil24_gateway"
	SourcePublicFeed    = "public_feed"
	SourceCheckoutAPI   = "checkout_api"
	SourceComplimentary = "complimentary"
)

// order_events.type values written by this package (spec §3.3 lists the full
// free-form vocabulary; these are the ones ordering itself emits).
const (
	EventCreated         = "created"
	EventLinesReconciled = "lines_reconciled"
	EventPaid            = "paid"
	EventCancelled       = "cancelled"
	EventHoldExpired     = "hold_expired"
)

// ActorSystem is the order_events.actor value for anything the platform does
// on its own initiative — the expire sweep above all. Human and gateway
// callers pass 'user:<uuid>' / 'gateway:<channel display_number>' instead.
const ActorSystem = "system"

// checkoutStatePricingConfirmed is the only checkout_sessions.state an order
// may be created from: before it, the money columns are still NULL.
const checkoutStatePricingConfirmed = "pricing_confirmed"

// ─────────────────────────────────────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrCheckoutNotPriced is returned when the checkout session has not
	// reached pricing_confirmed, so its money columns are still NULL and
	// there is nothing to snapshot onto the order.
	ErrCheckoutNotPriced = errors.New("ordering: checkout session is not pricing_confirmed")

	// ErrReservationNotHeld is returned when the reservation behind the
	// checkout session is no longer holding inventory (cancelled/expired/
	// already converted), which makes the order meaningless.
	ErrReservationNotHeld = errors.New("ordering: reservation no longer holds inventory")

	// ErrNoUnits is returned when the reservation carries neither GA lines
	// nor seats — an empty cart can never become an order.
	ErrNoUnits = errors.New("ordering: reservation has no units")

	// ErrMissingUnitPrice is returned when a seat unit has no price: neither
	// SeatUnitPrices nor TierUnitPrices covers it. Callers resolve prices via
	// priceresolve.ForTiers before calling in.
	ErrMissingUnitPrice = errors.New("ordering: no unit price for seat")

	// ErrNoOpenOrder is returned by FindOpenOrder when the customer has no
	// pending_payment order for that session. It is a normal outcome, not a
	// failure, and callers branch on it with errors.Is.
	ErrNoOpenOrder = errors.New("ordering: no open order")

	// ErrInvalidTransition is returned when a lifecycle call is made against
	// an order whose status forbids it (paying a cancelled order, expiring a
	// paid one).
	ErrInvalidTransition = errors.New("ordering: invalid status transition")
)

// ─────────────────────────────────────────────────────────────────────────────
// Store interfaces
// ─────────────────────────────────────────────────────────────────────────────
//
// Narrow, per-operation interfaces rather than one fat store: *gen.Queries
// satisfies all of them, and unit tests can implement just the handful of
// methods the function under test actually calls.

// CreateStore is the query surface CreateOrderFromCheckout needs.
type CreateStore interface {
	GetCheckoutSessionByID(ctx context.Context, id uuid.UUID) (gen.CheckoutSessionRow, error)
	GetReservationByID(ctx context.Context, id uuid.UUID) (gen.ReservationRow, error)
	ListReservationGAItems(ctx context.Context, reservationID uuid.UUID) ([]gen.ReservationGAItemRow, error)
	ListReservationSeats(ctx context.Context, reservationID uuid.UUID) ([]gen.SessionSeatRow, error)
	InsertOrder(
		ctx context.Context,
		orgID, channelID, eventID, sessionID uuid.UUID,
		customerID *uuid.UUID,
		checkoutSessionID, reservationID uuid.UUID,
		externalRef *string,
		source, status string,
		currency string,
		subtotal, discount, charge, total int64,
		chargePercentBP int32,
		promoCodeID *uuid.UUID,
		buyerName, buyerEmail, buyerPhone, paymentMethod *string,
		expiresAt *time.Time,
		metadata json.RawMessage,
	) (gen.OrderRow, error)
	InsertOrderItem(
		ctx context.Context,
		orderID uuid.UUID,
		ordinal int32,
		kind string,
		tierID uuid.UUID,
		sessionSeatID, ticketID *uuid.UUID,
		unitPrice, discount, charge, total int64,
	) (gen.OrderItemRow, error)
	EventStore
}

// EventStore appends to the order_events audit trail. Every mutation in this
// package writes exactly one event, so it is embedded everywhere.
type EventStore interface {
	InsertOrderEvent(ctx context.Context, orderID uuid.UUID, eventType, actor string, payload json.RawMessage) (gen.OrderEventRow, error)
}

// LifecycleStore is the query surface MarkPaid / Cancel / Expire need.
type LifecycleStore interface {
	GetOrderByID(ctx context.Context, id, orgID uuid.UUID) (gen.OrderRow, error)
	UpdateOrderStatus(ctx context.Context, id, orgID uuid.UUID, status string, paidAt, cancelledAt *time.Time) (gen.OrderRow, error)
	EventStore
}

// LookupStore is the query surface FindOpenOrder needs.
type LookupStore interface {
	FindOpenOrderByCustomerSession(ctx context.Context, customerID, sessionID uuid.UUID) (gen.OrderRow, error)
}

// SweepStore is the query surface the order.expire_sweep job needs.
type SweepStore interface {
	ListExpirableOrders(ctx context.Context, before time.Time, limit int32) ([]gen.OrderRow, error)
	ExpireOrderIfStillPending(ctx context.Context, id uuid.UUID) (gen.OrderRow, error)
	EventStore
}

// ─────────────────────────────────────────────────────────────────────────────
// Small helpers
// ─────────────────────────────────────────────────────────────────────────────

// ChargePercentBP converts sales_channels.fee_percent — a numeric(5,2) that
// pgx hands back as a string like "2.50" — into the basis points stored on
// orders.charge_percent_bp ("2.50" → 250).
//
// It is deliberately total: an unparseable or empty value yields 0 rather
// than an error, because the percentage is a SNAPSHOT FOR AUDIT and the exact
// charge amount is carried separately in orders.charge. Refusing to create an
// order over a malformed audit field would trade money for cosmetics.
func ChargePercentBP(feePercent string) int32 {
	s := strings.TrimSpace(feePercent)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	// math.Round before the int32 conversion so "2.505" does not truncate to
	// 250 through float noise.
	bp := math.Round(f * 100)
	if bp > math.MaxInt32 {
		return math.MaxInt32
	}
	if bp < math.MinInt32 {
		return math.MinInt32
	}
	return int32(bp)
}

// emptyJSON is the payload written when a caller supplies nothing; the column
// is NOT NULL DEFAULT '{}' but the driver still needs a concrete value.
func emptyJSON() json.RawMessage { return json.RawMessage(`{}`) }

// marshalPayload turns an arbitrary payload map into json.RawMessage,
// degrading to '{}' rather than failing a whole order for an audit row.
func marshalPayload(v any) json.RawMessage {
	if v == nil {
		return emptyJSON()
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return emptyJSON()
	}
	return b
}
