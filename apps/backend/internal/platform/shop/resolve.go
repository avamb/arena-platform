package shop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// The two ways an operator settles an order parked as late_payment_no_stock
// (the money was taken, the goods were gone), SB-04.
const (
	// ResolveFulfil: the goods turned up. The stock is checked once more and
	// the order is paid like any other (taken off the shelf, v1.shop.order.paid,
	// the buyer's "received" letter), ready to ship.
	ResolveFulfil = "fulfil"
	// ResolveRefundAndCancel: the goods will not come. The whole payment goes
	// back through the refund engine; the order ends refunded once the
	// provider confirms it.
	ResolveRefundAndCancel = "refund_and_cancel"
)

// ResolveInput is one decision on a parked order.
type ResolveInput struct {
	OrgID   uuid.UUID
	OrderID uuid.UUID
	// Action is ResolveFulfil or ResolveRefundAndCancel.
	Action      string
	RequestedBy string
	// IdempotencyKey is the Idempotency-Key of the call (1 to 255 characters,
	// required): a repeat with the same key answers the first decision and
	// writes nothing.
	IdempotencyKey string
}

// ResolveResult is the order after the decision. Refund is set for
// refund_and_cancel.
type ResolveResult struct {
	Order    OrderView
	Refund   *refunds.Refund
	Replayed bool
}

// Resolve settles an order parked in manual_review because its goods were gone
// after the payment arrived (review_reason late_payment_no_stock). An order
// parked because its PAYMENT is unconfirmed has nothing to decide yet: the
// payment's own success or failure moves it (FulfillPaidOrderTx,
// MarkPaymentFailedTx), and Resolve answers 409 shop.order_not_resolvable.
func (s *Service) Resolve(ctx context.Context, in ResolveInput) (ResolveResult, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > MaxIdempotencyKeyLen {
		return ResolveResult{}, newErr(http.StatusBadRequest, "shop.idempotency_key_required",
			"an Idempotency-Key header of 1 to 255 characters is required")
	}
	if in.Action != ResolveFulfil && in.Action != ResolveRefundAndCancel {
		return ResolveResult{}, newErr(http.StatusBadRequest, "shop.invalid_action", "action must be fulfil or refund_and_cancel")
	}
	if err := s.ready(); err != nil {
		return ResolveResult{}, err
	}
	switch in.Action {
	case ResolveFulfil:
		return s.resolveFulfil(ctx, in, key)
	default:
		return s.resolveRefund(ctx, in, key)
	}
}

func notInReview(o gen.ShopOrderRow) *Error {
	return newErr(http.StatusConflict, "shop.order_not_in_review",
		"only an order parked in manual_review can be resolved; this one is "+o.Status)
}

func notResolvable() *Error {
	return newErr(http.StatusConflict, "shop.order_not_resolvable",
		"this order waits for its payment to be confirmed, not for a decision about its goods; "+
			"settle the payment (succeeded or failed) and the order follows")
}

// resolveFulfil pays a parked order whose goods are free again.
func (s *Service) resolveFulfil(ctx context.Context, in ResolveInput, key string) (ResolveResult, error) {
	var (
		out      gen.ShopOrderRow
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, in.OrderID, in.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if o.Status != StatusManualReview {
			// A repeat of the call that paid it answers that decision.
			var m struct {
				Key    string `json:"resolve_key"`
				Action string `json:"resolve_action"`
			}
			if json.Unmarshal(o.Metadata, &m) == nil && m.Key == key && m.Action == ResolveFulfil {
				out, replayed = o, true
				return nil
			}
			return notInReview(o)
		}
		if !reviewReasonIs(o, ReviewLatePaymentNoStock) {
			return notResolvable()
		}
		if err := requireSucceededPayment(ctx, q, o); err != nil {
			return err
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
		meta := map[string]any{"resolve_key": key, "resolve_action": ResolveFulfil, "resolved_by": in.RequestedBy}
		out, err = s.markPaidTx(ctx, tx, q, o, meta)
		return err
	})
	if err != nil {
		return ResolveResult{}, err
	}
	view, err := s.view(ctx, s.q, out)
	if err != nil {
		return ResolveResult{}, err
	}
	return ResolveResult{Order: view, Replayed: replayed}, nil
}

// resolveRefund returns the whole payment of a parked order through the
// refund engine (Refund: the key makes a repeat answer the first refund).
func (s *Service) resolveRefund(ctx context.Context, in ResolveInput, key string) (ResolveResult, error) {
	o, err := s.q.GetShopOrder(ctx, in.OrderID, in.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolveResult{}, ErrOrderNotFound
	}
	if err != nil {
		return ResolveResult{}, err
	}
	// An order that has left manual_review is passed on as it is: a repeat of
	// the call finds its refund by the key, and anything else gets Refund's
	// own refusal.
	if o.Status == StatusManualReview && !reviewReasonIs(o, ReviewLatePaymentNoStock) {
		return ResolveResult{}, notResolvable()
	}
	res, err := s.Refund(ctx, RefundInput{
		OrgID: in.OrgID, OrderID: in.OrderID, Reason: "Goods not available after the payment arrived",
		RequestedBy: in.RequestedBy, IdempotencyKey: key,
	})
	if err != nil {
		return ResolveResult{}, err
	}
	r := res.Refund
	return ResolveResult{Order: res.Order, Refund: &r, Replayed: res.Replayed}, nil
}
