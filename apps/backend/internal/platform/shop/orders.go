package shop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// OrderView is an order with its lines and, when it has one, its payment.
type OrderView struct {
	gen.ShopOrderRow
	Items   []gen.ShopOrderItemRow
	Payment *gen.PaymentIntentRow
}

func (s *Service) view(ctx context.Context, q *gen.Queries, o gen.ShopOrderRow) (OrderView, error) {
	items, err := q.ListShopOrderItems(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return OrderView{}, err
	}
	v := OrderView{ShopOrderRow: o, Items: items}
	if o.PaymentIntentID != nil {
		pi, err := q.GetPaymentIntentByID(ctx, *o.PaymentIntentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return OrderView{}, err
		}
		if err == nil {
			v.Payment = &pi
		}
	}
	return v, nil
}

// GetOrder reads one order of the organization. It never writes: refunds
// reach the order through the refund engine's projection (ProjectPaymentTx).
func (s *Service) GetOrder(ctx context.Context, orgID, id uuid.UUID) (OrderView, error) {
	if err := s.ready(); err != nil {
		return OrderView{}, err
	}
	o, err := s.q.GetShopOrder(ctx, id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderView{}, ErrOrderNotFound
	}
	if err != nil {
		return OrderView{}, err
	}
	return s.view(ctx, s.q, o)
}

// OrderFilter filters the order list (see gen.ListShopOrdersParams).
type OrderFilter struct {
	Status string
	// View is a tab of the list: ViewToShip, ViewUnpaid, ViewShipped or
	// ViewRefunds. "" is the whole list.
	View   string
	Query  string
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

var orderStatuses = map[string]bool{
	StatusPendingPayment: true, StatusPaid: true, StatusShipped: true, StatusCancelled: true,
	StatusExpired: true, StatusRefunded: true, StatusPartiallyRefunded: true, StatusManualReview: true,
}

// The tabs of the order list. to_ship is what a manager asks first thing in
// the morning: paid, something to hand over, not shipped yet.
const (
	ViewToShip  = "to_ship"
	ViewUnpaid  = "unpaid"
	ViewShipped = "shipped"
	ViewRefunds = "refunds"
)

var orderViews = map[string]bool{ViewToShip: true, ViewUnpaid: true, ViewShipped: true, ViewRefunds: true}

// OrderCounts is how many orders each tab of the list holds.
type OrderCounts = gen.ShopOrderCounts

// CountOrders counts the orders of every tab in one query.
func (s *Service) CountOrders(ctx context.Context, orgID uuid.UUID) (OrderCounts, error) {
	if err := s.ready(); err != nil {
		return OrderCounts{}, err
	}
	return s.q.CountShopOrders(ctx, orgID)
}

// ListOrders lists one page of orders, newest first, with their lines.
func (s *Service) ListOrders(ctx context.Context, orgID uuid.UUID, f OrderFilter) ([]OrderView, error) {
	out, _, err := s.ListOrdersPage(ctx, orgID, f)
	return out, err
}

// ListOrdersPage is ListOrders that also says whether another page follows:
// one row more than the page is read and left out, so an exact multiple of
// the page size has no empty last page (SA-37).
func (s *Service) ListOrdersPage(ctx context.Context, orgID uuid.UUID, f OrderFilter) (out []OrderView, more bool, err error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	if f.Status != "" && !orderStatuses[f.Status] {
		return nil, false, newErr(http.StatusBadRequest, "shop.invalid_filter", "status is not an order status")
	}
	if f.View != "" && !orderViews[f.View] {
		return nil, false, newErr(http.StatusBadRequest, "shop.invalid_filter", "view must be to_ship, unpaid, shipped or refunds")
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 || f.Offset < 0 || f.Offset > 1_000_000 {
		return nil, false, newErr(http.StatusBadRequest, "shop.invalid_filter", "limit must be 1-200 and offset zero or more")
	}
	rows, err := s.q.ListShopOrders(ctx, gen.ListShopOrdersParams{
		OrgID: orgID, Status: f.Status, View: f.View, Query: strings.TrimSpace(f.Query), From: f.From, To: f.To,
		Limit: int32(f.Limit + 1), Offset: int32(f.Offset), //nolint:gosec // bounded above
	})
	if err != nil {
		return nil, false, err
	}
	if len(rows) > f.Limit {
		rows, more = rows[:f.Limit], true
	}
	out = make([]OrderView, 0, len(rows))
	if len(rows) == 0 {
		return out, false, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, o := range rows {
		ids[i] = o.ID
	}
	items, err := s.q.ListShopOrderItems(ctx, ids)
	if err != nil {
		return nil, false, err
	}
	byOrder := map[uuid.UUID][]gen.ShopOrderItemRow{}
	for _, it := range items {
		byOrder[it.OrderID] = append(byOrder[it.OrderID], it)
	}
	for _, o := range rows {
		out = append(out, OrderView{ShopOrderRow: o, Items: byOrder[o.ID]})
	}
	return out, more, nil
}

// Ship marks a paid order shipped and writes v1.shop.order.shipped. Shipping
// an order that is shipped already only updates its tracking note. Shipping
// an order parked in manual_review means the operator found the goods: they
// are taken off the shelf now, but only when they are free (stock left after
// every live hold); otherwise 409 shop.out_of_stock lists the short lines.
func (s *Service) Ship(ctx context.Context, orgID, id uuid.UUID, trackingNote string) (OrderView, error) {
	trackingNote = strings.TrimSpace(trackingNote)
	if !textOK(trackingNote, MaxNoteLen) {
		return OrderView{}, invalid("shop.text_too_long", "tracking_note is limited to 2000 characters")
	}
	var out gen.ShopOrderRow
	err := s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if o.DeliveryKind == DeliveryNone {
			// Digital or service goods: there is no parcel, no address and no
			// letter, so "shipped" would only write a false event (SA-32).
			return newErr(http.StatusConflict, "shop.order_not_shippable",
				"this order has nothing to ship: it holds only digital or service goods")
		}
		switch o.Status {
		case StatusShipped:
			out, err = q.SetShopOrderStatus(ctx, o.ID, StatusShipped, &trackingNote, nil)
			return err
		case StatusPartiallyRefunded:
			if o.ShippedAt != nil {
				// Shipped before the refund (M-10): the parcel has gone, so it is
				// not shipped a second time and no second letter is sent. Only
				// the note can change, and the refund status stays.
				out, err = q.SetShopOrderStatus(ctx, o.ID, StatusPartiallyRefunded, &trackingNote, nil)
				return err
			}
		case StatusPaid:
		case StatusManualReview:
			if reviewReasonIs(o, ReviewPaymentUnconfirmed) {
				// Parked because its PAYMENT waits for a human (an amount
				// mismatch, a refund at the provider before arena confirmed
				// it): the goods go out only once that payment is settled.
				if err := requireSucceededPayment(ctx, q, o); err != nil {
					return err
				}
			}
			if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
				return err
			}
			lines, err := q.ShopOrderShortages(ctx, o.ID)
			if err != nil {
				return err
			}
			if short := shortLines(lines); len(short) > 0 {
				e := newErr(http.StatusConflict, "shop.out_of_stock",
					"the goods of this order are not free on the shelf; add stock or refund the order")
				e.Details = map[string]any{"items": short}
				return e
			}
			if err := s.applySaleTx(ctx, q, o); err != nil {
				return err
			}
		default:
			return newErr(http.StatusConflict, "shop.order_not_shippable", "only a paid order can be shipped; this one is "+o.Status)
		}
		out, err = q.SetShopOrderStatus(ctx, o.ID, StatusShipped, &trackingNote, nil)
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, EventOrderShipped, out, map[string]any{"tracking_note": trackingNote}); err != nil {
			return err
		}
		s.enqueueOrderEmailTx(ctx, tx, EmailShipped, out)
		if o.RefundedMinor > 0 {
			// Part of the order was refunded before it was handed over: the
			// shipment must not erase that. The status follows the money
			// again through the one rule every refund writer uses (SA-10).
			out, err = s.syncRefundsTx(ctx, tx, q, out)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return OrderView{}, err
	}
	return s.view(ctx, s.q, out)
}

// Cancel cancels an order that is still waiting for its payment; its goods go
// back on sale. A paid order is not cancelled: it is refunded. After the
// commit the provider's hosted page is closed where the provider can do it,
// so a buyer who still has it open cannot pay for goods that went back on
// sale; a payment that slips through anyway is a late payment
// (FulfillPaidOrderTx).
func (s *Service) Cancel(ctx context.Context, orgID, id uuid.UUID) (OrderView, error) {
	var (
		out  gen.ShopOrderRow
		page *gen.PaymentIntentRow
	)
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if o.Status != StatusPendingPayment {
			return newErr(http.StatusConflict, "shop.order_not_cancellable",
				"only an order waiting for its payment can be cancelled; refund a paid one instead (this one is "+o.Status+")")
		}
		if o.PaymentIntentID != nil {
			pi, err := q.GetPaymentIntentByID(ctx, *o.PaymentIntentID)
			if err != nil {
				return err
			}
			if pi.State == "succeeded" || pi.State == "manual_review" {
				return newErr(http.StatusConflict, "shop.order_not_cancellable", "the buyer has already paid this order; refund it instead")
			}
			page = &pi
		}
		out, err = q.SetShopOrderStatus(ctx, o.ID, StatusCancelled, nil, nil)
		if err != nil {
			return err
		}
		if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
			return err
		}
		return q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease)
	})
	if err != nil {
		return OrderView{}, err
	}
	s.expirePage(ctx, orgID, out.ID, page)
	return s.view(ctx, s.q, out)
}

// expirePage closes the hosted page of a cancelled order, best effort.
func (s *Service) expirePage(ctx context.Context, orgID, orderID uuid.UUID, pi *gen.PaymentIntentRow) {
	if s.pages == nil || pi == nil || pi.ProviderPaymentID == nil || *pi.ProviderPaymentID == "" {
		return
	}
	if err := s.pages.ExpireHostedPage(ctx, orgID, pi.Provider, *pi.ProviderPaymentID); err != nil {
		s.logger.Warn("shop: could not close the cancelled order's payment page; a late payment is parked or paid by the webhook",
			"order_id", orderID.String(), "provider", pi.Provider, "error", err.Error())
	}
}

// RefundInput is one refund of a shop order. Amount nil refunds what is left.
type RefundInput struct {
	OrgID       uuid.UUID
	OrderID     uuid.UUID
	Amount      *int64
	Reason      string
	RequestedBy string
	// IdempotencyKey is the Idempotency-Key of the call (1 to 255
	// characters, required): a repeat with the same key answers the first
	// refund and writes nothing, so a retry after a dropped answer can never
	// return the money twice.
	IdempotencyKey string
}

// RefundResult is the order after the refund and the refund itself.
type RefundResult struct {
	Order  OrderView
	Refund refunds.Refund
	// Replayed: the key had been used by an earlier call, whose refund this
	// is. Nothing new was written.
	Replayed bool
}

// MaxIdempotencyKeyLen is the longest Idempotency-Key a refund call accepts.
const MaxIdempotencyKeyLen = 255

// Refund returns money of a paid order through its payment provider. The
// refunds row is written on the order's payment intent and approved in one
// transaction under the payment's lock (the refund engine's rule: the sum
// of a payment's live refunds can never exceed it), then the engine drives it
// to the provider after the commit. The order's status and refunded amount
// follow what the provider accepted: refunded when all of it went back,
// partially_refunded when some did. Stock is never put back automatically
// (opened or shipped goods); the operator corrects it in the variant list.
func (s *Service) Refund(ctx context.Context, in RefundInput) (RefundResult, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > MaxIdempotencyKeyLen {
		return RefundResult{}, newErr(http.StatusBadRequest, "shop.idempotency_key_required",
			"an Idempotency-Key header of 1 to 255 characters is required")
	}
	reason := strings.TrimSpace(in.Reason)
	if !textOK(reason, MaxNoteLen) {
		return RefundResult{}, invalid("shop.text_too_long", "reason is limited to 2000 characters")
	}
	var (
		refundID uuid.UUID
		amount   int64
		currency string
		replayed bool
	)
	if err := s.ready(); err != nil {
		return RefundResult{}, err
	}
	// The payment's refund lock is taken before the order's row lock, the
	// order every refund writer follows (the engine's reversal projects the
	// order under the payment lock).
	peek, err := s.q.GetShopOrder(ctx, in.OrderID, in.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefundResult{}, ErrOrderNotFound
	}
	if err != nil {
		return RefundResult{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		if peek.PaymentIntentID != nil {
			if err := refunds.LockPayment(ctx, tx, *peek.PaymentIntentID); err != nil {
				return err
			}
		}
		o, err := q.GetShopOrderForUpdate(ctx, in.OrderID, in.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if !sameUUID(o.PaymentIntentID, peek.PaymentIntentID) {
			return newErr(http.StatusConflict, "shop.order_changed", "the order changed while the refund was being prepared; try again")
		}
		// A repeated call finds its refund before any other check: the
		// first call may have used up what was left of the order.
		prior, perr := q.GetShopRefundByKey(ctx, in.OrgID, key)
		if perr != nil && !errors.Is(perr, pgx.ErrNoRows) {
			return perr
		}
		if perr == nil {
			if prior.ShopOrderID == nil || *prior.ShopOrderID != in.OrderID || (in.Amount != nil && *in.Amount != prior.Amount) {
				return errKeyReused()
			}
			refundID, amount, replayed = prior.ID, prior.Amount, true
			return nil
		}
		switch o.Status {
		case StatusPaid, StatusShipped, StatusPartiallyRefunded, StatusManualReview:
		default:
			return newErr(http.StatusConflict, "shop.order_not_refundable", "only a paid order can be refunded; this one is "+o.Status)
		}
		if o.PaymentIntentID == nil {
			return newErr(http.StatusConflict, "shop.order_not_refundable", "this order was not paid through arena and cannot be refunded here")
		}
		pi, err := q.GetPaymentIntentByID(ctx, *o.PaymentIntentID)
		if err != nil {
			return err
		}
		if pi.State != "succeeded" {
			return newErr(http.StatusConflict, "shop.order_not_refundable", "the payment of this order is "+pi.State+", not succeeded")
		}
		_, live, err := q.SumShopPaymentRefunds(ctx, pi.ID)
		if err != nil {
			return err
		}
		remaining := min(o.Total, pi.Amount) - live
		amount, currency = remaining, o.Currency
		if in.Amount != nil {
			amount = *in.Amount
		}
		if remaining <= 0 {
			return newErr(http.StatusConflict, "shop.nothing_to_refund", "the whole order has been refunded already")
		}
		if amount <= 0 || amount > remaining {
			e := invalid("shop.invalid_refund_amount",
				fmt.Sprintf("amount_minor must be between 1 and %d (what is left of the order)", remaining))
			e.Details = map[string]any{"refundable_minor": remaining}
			return e
		}
		var reasonPtr, byPtr *string
		if reason != "" {
			reasonPtr = &reason
		}
		if in.RequestedBy != "" {
			by := in.RequestedBy
			byPtr = &by
		}
		row, err := q.InsertRefund(ctx, pi.ID, o.OrgID, amount, o.Currency, reasonPtr, byPtr)
		if err != nil {
			return err
		}
		if err := q.SetShopRefundKey(ctx, row.ID, o.ID, key); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// The key was taken by a refund of another payment between
				// the lookup and the write (that payment's lock is not ours).
				return errKeyReused()
			}
			return err
		}
		if _, err := refunds.MarkApprovedTx(ctx, tx, row.ID, pi.Provider); err != nil {
			return fmt.Errorf("shop: approve refund: %w", err)
		}
		refundID = row.ID
		return nil
	})
	if err != nil {
		return RefundResult{}, err
	}

	var r refunds.Refund
	if s.refunds != nil {
		// On a replay Drive is the repeat's job too: it returns an answered
		// refund unchanged and sends one that was recorded but never reached
		// the provider (the first call's connection was cut).
		r, err = s.refunds.Drive(ctx, refundID)
		if err != nil {
			// The refund is recorded and approved; refund.sweep retries it.
			s.logger.Error("shop: driving the refund failed; refund.sweep retries it",
				"refund_id", refundID.String(), "order_id", in.OrderID.String(), "error", err.Error())
		}
	}
	if r.ID == uuid.Nil {
		r = refunds.Refund{ID: refundID, State: refunds.StateProviderPending, Amount: amount, Currency: currency}
		if replayed {
			if row, gerr := s.q.GetRefundByID(ctx, refundID); gerr == nil {
				r = refunds.Refund{ID: row.ID, State: row.State, Amount: row.Amount, Currency: row.Currency,
					ProviderRefundID: row.ProviderRefundID, FailureReason: row.FailureReason}
			}
		}
	}
	if _, err := s.SyncRefunds(ctx, in.OrgID, in.OrderID); err != nil {
		return RefundResult{}, err
	}
	view, err := s.GetOrder(ctx, in.OrgID, in.OrderID)
	if err != nil {
		return RefundResult{}, err
	}
	return RefundResult{Order: view, Refund: r, Replayed: replayed}, nil
}

func errKeyReused() *Error {
	return newErr(http.StatusConflict, "shop.idempotency_key_reused",
		"this Idempotency-Key was already used for a different refund request")
}

// SyncRefunds folds the order's succeeded refunds into it (see
// syncRefundsTx), in a transaction of its own.
func (s *Service) SyncRefunds(ctx context.Context, orgID, id uuid.UUID) (gen.ShopOrderRow, error) {
	var out gen.ShopOrderRow
	err := s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		out, err = s.syncRefundsTx(ctx, tx, q, o)
		return err
	})
	return out, err
}

// ProjectPaymentTx is the refund engine's projection hook
// (refunds.Options.ProjectPayment): on the engine's transaction it folds the
// payment's succeeded refunds into the shop order the payment paid for. A
// payment of no shop order is left alone.
func (s *Service) ProjectPaymentTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID) error {
	q := gen.New(tx)
	o, err := q.GetShopOrderByPaymentIntentForUpdate(ctx, paymentIntentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.syncRefundsTx(ctx, tx, q, o)
	return err
}

// syncRefundsTx sets refunded_minor to what the order's payment got back
// through SUCCEEDED refunds (a pending refund may still fail) and the status
// that follows (refundStatus); o must be locked. When more went back than the
// order knew, v1.shop.order.refunded is written; when less (a succeeded
// refund the provider reversed), the order returns to the status it had.
func (s *Service) syncRefundsTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow) (gen.ShopOrderRow, error) {
	if o.PaymentIntentID == nil {
		return o, nil
	}
	accepted, _, err := q.SumShopPaymentRefunds(ctx, *o.PaymentIntentID)
	if err != nil {
		return o, err
	}
	accepted = min(accepted, o.Total)
	status := refundStatus(o, accepted)
	if accepted == o.RefundedMinor && status == o.Status {
		return o, nil
	}
	out, err := q.SetShopOrderRefunded(ctx, o.ID, status, accepted)
	if err != nil {
		return o, err
	}
	if accepted < o.RefundedMinor {
		s.logger.Warn("shop: a refund the order counted did not go through; its refunded amount went down",
			"order_id", o.ID.String(), "was", o.RefundedMinor, "now", accepted, "status", status)
		return out, nil
	}
	if accepted > o.RefundedMinor {
		if err := s.appendEvent(ctx, tx, EventOrderRefunded, out, map[string]any{
			"refunded_now": accepted - o.RefundedMinor,
		}); err != nil {
			return out, err
		}
		// The buyer is told in the same transaction that records the refund
		// as accepted, so a refund always has its letter queued and a rolled
		// back one never does. The letter can never fail the refund.
		s.enqueueRefundEmailTx(ctx, tx, out)
		return out, nil
	}
	return out, nil
}

// requireSucceededPayment refuses with 409 shop.payment_unconfirmed unless the
// order's payment intent succeeded.
func requireSucceededPayment(ctx context.Context, q *gen.Queries, o gen.ShopOrderRow) error {
	state := "missing"
	if o.PaymentIntentID != nil {
		pi, err := q.GetPaymentIntentByID(ctx, *o.PaymentIntentID)
		if err != nil {
			return err
		}
		state = pi.State
	}
	if state != "succeeded" {
		return newErr(http.StatusConflict, "shop.payment_unconfirmed",
			"the payment of this order is "+state+", not succeeded; settle the payment before shipping")
	}
	return nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CourierTextFor is the courier text of one order of the organization.
func (s *Service) CourierTextFor(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	o, err := s.q.GetShopOrder(ctx, id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrOrderNotFound
	}
	if err != nil {
		return "", err
	}
	items, err := s.q.ListShopOrderItems(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return "", err
	}
	return CourierText(o, items), nil
}
