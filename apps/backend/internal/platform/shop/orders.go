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

// GetOrder reads one order of the organization. Refunds the provider settled
// since the last look (a pending refund that completed, a refund made in the
// provider's dashboard) are folded into the order first.
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
	if o.PaymentIntentID != nil && o.PaidAt != nil {
		if synced, err := s.SyncRefunds(ctx, orgID, id); err == nil {
			o = synced
		} else {
			s.logger.Warn("shop: refund sync failed; showing the stored order", "order_id", id.String(), "error", err.Error())
		}
	}
	return s.view(ctx, s.q, o)
}

// OrderFilter filters the order list (see gen.ListShopOrdersParams).
type OrderFilter struct {
	Status string
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

// ListOrders lists one page of orders, newest first, with their lines.
func (s *Service) ListOrders(ctx context.Context, orgID uuid.UUID, f OrderFilter) ([]OrderView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if f.Status != "" && !orderStatuses[f.Status] {
		return nil, newErr(http.StatusBadRequest, "shop.invalid_filter", "status is not an order status")
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 || f.Offset < 0 || f.Offset > 1_000_000 {
		return nil, newErr(http.StatusBadRequest, "shop.invalid_filter", "limit must be 1-200 and offset zero or more")
	}
	rows, err := s.q.ListShopOrders(ctx, gen.ListShopOrdersParams{
		OrgID: orgID, Status: f.Status, Query: strings.TrimSpace(f.Query), From: f.From, To: f.To,
		Limit: int32(f.Limit), Offset: int32(f.Offset), //nolint:gosec // bounded above
	})
	if err != nil {
		return nil, err
	}
	out := make([]OrderView, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, o := range rows {
		ids[i] = o.ID
	}
	items, err := s.q.ListShopOrderItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	byOrder := map[uuid.UUID][]gen.ShopOrderItemRow{}
	for _, it := range items {
		byOrder[it.OrderID] = append(byOrder[it.OrderID], it)
	}
	for _, o := range rows {
		out = append(out, OrderView{ShopOrderRow: o, Items: byOrder[o.ID]})
	}
	return out, nil
}

// Ship marks a paid order shipped and writes v1.shop.order.shipped. Shipping
// an order that is shipped already only updates its tracking note. Shipping
// an order parked in manual_review means the operator found the goods: they
// are taken off the shelf now.
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
		switch o.Status {
		case StatusShipped:
			out, err = q.SetShopOrderStatus(ctx, o.ID, StatusShipped, &trackingNote, nil)
			return err
		case StatusPaid, StatusPartiallyRefunded:
		case StatusManualReview:
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
}

// RefundResult is the order after the refund and the refund itself.
type RefundResult struct {
	Order  OrderView
	Refund refunds.Refund
}

// Refund returns money of a paid order through its payment provider. The
// refunds row is written on the order's payment intent and approved in one
// transaction under the payment's lock (the refund engine's rule: the sum
// of a payment's live refunds can never exceed it), then the engine drives it
// to the provider after the commit. The order's status and refunded amount
// follow what the provider accepted: refunded when all of it went back,
// partially_refunded when some did. Stock is never put back automatically
// (opened or shipped goods); the operator corrects it in the variant list.
func (s *Service) Refund(ctx context.Context, in RefundInput) (RefundResult, error) {
	reason := strings.TrimSpace(in.Reason)
	if !textOK(reason, MaxNoteLen) {
		return RefundResult{}, invalid("shop.text_too_long", "reason is limited to 2000 characters")
	}
	var (
		refundID uuid.UUID
		amount   int64
		currency string
	)
	err := s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, in.OrderID, in.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
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
		if err := refunds.LockPayment(ctx, tx, pi.ID); err != nil {
			return err
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
		r, err = s.refunds.Drive(ctx, refundID)
		if err != nil {
			// The refund is recorded and approved; refund.sweep retries it.
			s.logger.Error("shop: driving the refund failed; refund.sweep retries it",
				"refund_id", refundID.String(), "order_id", in.OrderID.String(), "error", err.Error())
		}
	}
	if r.ID == uuid.Nil {
		r = refunds.Refund{ID: refundID, State: refunds.StateProviderPending, Amount: amount, Currency: currency}
	}
	if _, err := s.SyncRefunds(ctx, in.OrgID, in.OrderID); err != nil {
		return RefundResult{}, err
	}
	view, err := s.GetOrder(ctx, in.OrgID, in.OrderID)
	if err != nil {
		return RefundResult{}, err
	}
	return RefundResult{Order: view, Refund: r}, nil
}

// SyncRefunds folds what the provider has accepted for the order's payment
// into the order: refunded_minor and the status that follows. When more went
// back than the order knew, v1.shop.order.refunded is written in the same
// transaction. A refund that is still on its way changes nothing.
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
		out = o
		if o.PaymentIntentID == nil {
			return nil
		}
		accepted, _, err := q.SumShopPaymentRefunds(ctx, *o.PaymentIntentID)
		if err != nil {
			return err
		}
		accepted = min(accepted, o.Total)
		if accepted <= o.RefundedMinor {
			return nil
		}
		status := o.Status
		switch o.Status {
		case StatusPaid, StatusShipped, StatusPartiallyRefunded, StatusRefunded:
			status = RefundedStatus(o.Status, o.Total, accepted)
		case StatusManualReview:
			// A parked order stays parked until all of it went back.
			if accepted >= o.Total {
				status = StatusRefunded
			}
		}
		out, err = q.SetShopOrderRefunded(ctx, o.ID, status, accepted)
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, EventOrderRefunded, out, map[string]any{
			"refunded_now": accepted - o.RefundedMinor,
		})
	})
	return out, err
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
