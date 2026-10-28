package shop

import (
	"context"
	"fmt"

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
)

// OrderEmailPayload is the payload of JobTypeOrderEmail.
type OrderEmailPayload struct {
	OrderID string `json:"order_id"`
	OrgID   string `json:"org_id"`
	// Kind is EmailReceived or EmailShipped.
	Kind string `json:"kind"`
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
