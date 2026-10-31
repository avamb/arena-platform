package shop

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// The shop's e-mails to the buyer are written by arena-worker (delivery
// package, which owns the templates and the sender): this package only queues
// a shop.order_email job in the SAME transaction as the status change, so a
// paid or shipped order always has its letter queued and an order that rolled
// back never does. The shop package renders nothing.
const (
	// JobTypeOrderEmail is the worker job of one buyer e-mail of a shop order.
	JobTypeOrderEmail = "shop.order_email"

	// EmailReceived is the "your order is confirmed" letter (order paid).
	EmailReceived = "received"
	// EmailShipped is the "your order is on its way" letter (order shipped).
	EmailShipped = "shipped"
	// EmailRefunded is the "money has been returned" letter, one per
	// succeeded refund (partial or full).
	EmailRefunded = "refunded"
)

// DeliveryNone is shop_orders.delivery_kind of a cart with nothing to ship
// (only digital or service lines): no address, no shipping fee, and no
// "shipped" letter.
const DeliveryNone = "none"

// OrderEmailPayload is the payload of JobTypeOrderEmail.
type OrderEmailPayload struct {
	OrderID string `json:"order_id"`
	OrgID   string `json:"org_id"`
	// Kind is EmailReceived, EmailShipped or EmailRefunded.
	Kind string `json:"kind"`
	// RefundID names the refund an EmailRefunded letter is about: the letter
	// is sent, and remembered as sent, once per refund.
	RefundID string `json:"refund_id,omitempty"`
	// Resend is set when a person asked for the letter again: the worker
	// then ignores its "already sent" marker and mails it once more.
	Resend bool `json:"resend,omitempty"`
}

// orderEmailAttempts is the job's retry budget (a mail outage).
const orderEmailAttempts = 8

// enqueueOrderEmailTx queues the buyer's e-mail inside tx. It runs behind a
// SAVEPOINT: this is called inside the payment webhook's money transaction,
// and a failed statement would otherwise abort the whole of it (AGENTS.md:
// best-effort writes inside a money transaction), losing a paid order over a
// letter. A failure is logged and the order goes on without its e-mail.
func (s *Service) enqueueOrderEmailTx(ctx context.Context, tx pgx.Tx, kind string, o gen.ShopOrderRow) {
	if o.BuyerEmail == "" {
		return // nobody to write to
	}
	if kind == EmailShipped && o.DeliveryKind == DeliveryNone {
		return // nothing ships: a digital or service order has no "shipped" letter
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		s.logger.Error("shop: could not open a savepoint for the order e-mail",
			"order_id", o.ID.String(), "kind", kind, "error", err.Error())
		return
	}
	_, err = worker.EnqueueInTx(ctx, sp, JobTypeOrderEmail, OrderEmailPayload{
		OrderID: o.ID.String(), OrgID: o.OrgID.String(), Kind: kind,
	}, orderEmailAttempts)
	if err != nil {
		_ = sp.Rollback(ctx)
		s.logger.Error("shop: could not queue the order e-mail; the order is unaffected",
			"order_id", o.ID.String(), "kind", kind, "error", fmt.Sprint(err))
		return
	}
	if err := sp.Commit(ctx); err != nil {
		s.logger.Error("shop: could not release the order e-mail savepoint",
			"order_id", o.ID.String(), "kind", kind, "error", err.Error())
	}
}

// LatestSucceededRefundID is the id of the newest succeeded refund of the
// payment (by the time it succeeded), uuid.Nil when there is none.
func LatestSucceededRefundID(ctx context.Context, db interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, paymentIntentID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx, `SELECT id FROM refunds
		WHERE payment_intent_id = $1 AND state = 'succeeded'
		ORDER BY updated_at DESC, id DESC LIMIT 1`, paymentIntentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// enqueueRefundEmailTx queues the "money returned" letter for the refund that
// was just folded into the order, in the same transaction and behind a
// SAVEPOINT for the reason enqueueOrderEmailTx gives: it runs inside the
// refund engine's transaction, and a failed statement there would roll back
// a refund that already went through the provider. One job per refund: a
// replay (the same refund seen again) finds the job and queues nothing.
func (s *Service) enqueueRefundEmailTx(ctx context.Context, tx pgx.Tx, o gen.ShopOrderRow) {
	if o.BuyerEmail == "" || o.PaymentIntentID == nil {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		s.logger.Error("shop: could not open a savepoint for the refund e-mail",
			"order_id", o.ID.String(), "error", err.Error())
		return
	}
	err = s.queueRefundEmail(ctx, sp, o)
	if err != nil {
		_ = sp.Rollback(ctx)
		s.logger.Error("shop: could not queue the refund e-mail; the refund is unaffected",
			"order_id", o.ID.String(), "error", fmt.Sprint(err))
		return
	}
	if err := sp.Commit(ctx); err != nil {
		s.logger.Error("shop: could not release the refund e-mail savepoint",
			"order_id", o.ID.String(), "error", err.Error())
	}
}

func (s *Service) queueRefundEmail(ctx context.Context, sp pgx.Tx, o gen.ShopOrderRow) error {
	refundID, err := LatestSucceededRefundID(ctx, sp, *o.PaymentIntentID)
	if err != nil || refundID == uuid.Nil {
		return err
	}
	var queued bool
	if err := sp.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM worker_jobs
		WHERE job_type = $1 AND payload->>'kind' = $2 AND payload->>'refund_id' = $3
		  AND payload->>'resend' IS NULL)`,
		JobTypeOrderEmail, EmailRefunded, refundID.String()).Scan(&queued); err != nil {
		return err
	}
	if queued {
		return nil
	}
	_, err = worker.EnqueueInTx(ctx, sp, JobTypeOrderEmail, OrderEmailPayload{
		OrderID: o.ID.String(), OrgID: o.OrgID.String(), Kind: EmailRefunded, RefundID: refundID.String(),
	}, orderEmailAttempts)
	return err
}
