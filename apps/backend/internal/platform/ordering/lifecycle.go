package ordering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Lifecycle transitions
// ─────────────────────────────────────────────────────────────────────────────
//
// All three transitions share one shape: load the order (org-scoped, so a
// cross-tenant id is indistinguishable from a missing one), refuse the move
// when the current status forbids it, flip the status, append the audit event.
// They are idempotent on the "already there" case — replaying a payment
// webhook or a sweep tick must not fail, and must not write a second event.

// PaidInput describes a settled payment. PaymentMethod/ExternalRef are not
// touched here (the order already carries what it was created with); only the
// audit payload records the provider's identifiers.
type PaidInput struct {
	OrderID uuid.UUID
	OrgID   uuid.UUID
	Actor   string
	// Payload is merged into order_events.paid — provider reference,
	// amount as the gateway saw it, and so on. Nil is fine.
	Payload any
	Now     time.Time
}

// MarkPaid moves a pending_payment order to paid and stamps paid_at
// (spec §14.1: called from the payment webhook and from PAY_ORDER). Replaying
// it on an already-paid order returns that order unchanged with no second
// audit row; calling it on a cancelled or expired order is ErrInvalidTransition
// — a late payment for a dead order is a human decision, not an automatic one.
func MarkPaid(ctx context.Context, q LifecycleStore, in PaidInput) (gen.OrderRow, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	current, err := q.GetOrderByID(ctx, in.OrderID, in.OrgID)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: load order: %w", err)
	}
	if current.Status == StatusPaid {
		return current, nil
	}
	if current.Status != StatusPendingPayment {
		return gen.OrderRow{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current.Status, StatusPaid)
	}

	paidAt := now
	updated, err := q.UpdateOrderStatus(ctx, in.OrderID, in.OrgID, StatusPaid, &paidAt, nil)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: mark paid: %w", err)
	}
	if _, err := q.InsertOrderEvent(ctx, updated.ID, EventPaid, actorOrSystem(in.Actor), marshalPayload(in.Payload)); err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: insert paid event: %w", err)
	}
	return updated, nil
}

// OrderCancelledEventType is the platform outbox event a cancellation emits
// (W1-B7c, #506). The bil24_wp dispatcher turns it into Bil24's
// `order.cancelled` so a migrated WordPress shop voids the order it was told
// about at order.paid.
const OrderCancelledEventType = "v1.order.cancelled"

// OrderAggregateType is the outbox aggregate the cancellation event belongs to.
const OrderAggregateType = "order"

// OrderCancelledPublisher is the optional outbox hook Cancel calls once the
// transition has actually been recorded.
//
// It is a callback rather than an outbox writer because this package owns no
// transaction: Cancel operates through LifecycleStore, and the caller — which
// does hold the tx — decides whether the event is appended in-band or
// best-effort afterwards. Publishing is deliberately BEST EFFORT: a webhook
// that failed to be enqueued must never roll back a cancellation the operator
// already saw succeed.
type OrderCancelledPublisher func(ctx context.Context, order gen.OrderRow)

// BuildOrderCancelledPayload is the canonical v1.order.cancelled payload. The
// channel travels with the event because that is the dispatcher's routing key:
// which WordPress site, if any, sold this order.
func BuildOrderCancelledPayload(order gen.OrderRow, reason string) map[string]any {
	return map[string]any{
		"order_id":   order.ID.String(),
		"org_id":     order.OrgID.String(),
		"channel_id": order.ChannelID.String(),
		"system_id":  order.SystemID,
		"reason":     reason,
	}
}

// CancelInput describes a deliberate cancellation (buyer abandonment, an
// operator cancelling from the admin, a gateway CANCEL_ORDER).
type CancelInput struct {
	OrderID uuid.UUID
	OrgID   uuid.UUID
	Actor   string
	Reason  string
	Now     time.Time
	// Publish, when set, receives the cancelled order so the caller can
	// append v1.order.cancelled to the outbox. Nil disables the notification.
	Publish OrderCancelledPublisher
}

// Cancel moves a pending_payment order to cancelled and stamps cancelled_at.
// Re-cancelling is a no-op; cancelling a paid order is ErrInvalidTransition
// because unwinding money is the refund path's job, not this one's.
func Cancel(ctx context.Context, q LifecycleStore, in CancelInput) (gen.OrderRow, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	current, err := q.GetOrderByID(ctx, in.OrderID, in.OrgID)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: load order: %w", err)
	}
	if current.Status == StatusCancelled {
		return current, nil
	}
	if current.Status != StatusPendingPayment {
		return gen.OrderRow{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current.Status, StatusCancelled)
	}

	cancelledAt := now
	updated, err := q.UpdateOrderStatus(ctx, in.OrderID, in.OrgID, StatusCancelled, nil, &cancelledAt)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: cancel order: %w", err)
	}
	payload := map[string]any{"reason": in.Reason}
	if _, err := q.InsertOrderEvent(ctx, updated.ID, EventCancelled, actorOrSystem(in.Actor), marshalPayload(payload)); err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: insert cancelled event: %w", err)
	}
	// Only a real transition notifies: the no-op re-cancel above returned
	// early, so a retried CANCEL_ORDER cannot void the order at the site twice.
	if in.Publish != nil {
		in.Publish(ctx, updated)
	}
	return updated, nil
}

// ExpireInput describes an order whose hold ran out.
type ExpireInput struct {
	OrderID uuid.UUID
	OrgID   uuid.UUID
	Actor   string
	Now     time.Time
}

// Expire moves a pending_payment order to expired and records a hold_expired
// event. Unlike Cancel it stamps cancelled_at as well: 'expired' is the reason
// and cancelled_at is the "stopped being live at" timestamp the admin list
// sorts on. Re-expiring is a no-op.
//
// Note that this is the *single-order* transition; the sweep job in
// expire_sweep.go uses the status-guarded ExpireOrderIfStillPending query
// instead so it can race a payment webhook safely without holding a row lock
// across a read-then-write.
func Expire(ctx context.Context, q LifecycleStore, in ExpireInput) (gen.OrderRow, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	current, err := q.GetOrderByID(ctx, in.OrderID, in.OrgID)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: load order: %w", err)
	}
	if current.Status == StatusExpired {
		return current, nil
	}
	if current.Status != StatusPendingPayment {
		return gen.OrderRow{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current.Status, StatusExpired)
	}

	expiredAt := now
	updated, err := q.UpdateOrderStatus(ctx, in.OrderID, in.OrgID, StatusExpired, nil, &expiredAt)
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: expire order: %w", err)
	}
	if _, err := q.InsertOrderEvent(ctx, updated.ID, EventHoldExpired, actorOrSystem(in.Actor), emptyJSON()); err != nil {
		return gen.OrderRow{}, fmt.Errorf("ordering: insert hold_expired event: %w", err)
	}
	return updated, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// FindOpenOrder
// ─────────────────────────────────────────────────────────────────────────────

// FindOpenOrder resolves the one pending_payment order a customer may hold for
// an event session — the read side of the partial unique index
// orders_one_pending_per_customer_session_uq (spec §3.3/§7.7 step 4:
// CREATE_ORDER_EXT reuses the open order instead of minting a second one).
//
// "No open order" is the common case, not an error condition, so it comes back
// as ErrNoOpenOrder for callers to branch on with errors.Is rather than as a
// leaked pgx.ErrNoRows.
func FindOpenOrder(ctx context.Context, q LookupStore, customerID, sessionID uuid.UUID) (gen.OrderRow, error) {
	row, err := q.FindOpenOrderByCustomerSession(ctx, customerID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.OrderRow{}, ErrNoOpenOrder
		}
		return gen.OrderRow{}, fmt.Errorf("ordering: find open order: %w", err)
	}
	return row, nil
}
