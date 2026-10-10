// create.go — one "refund these tickets" operation (spec 36 §7 steps 1–2):
// check everything, then write the batch and one refund row per ticket in a
// single transaction under the payment's advisory lock. Nothing reaches the
// provider here; with Approved the rows are born provider_pending and
// CreateBatch drives them right after the commit.
package refunds

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Item is one ticket of a batch. A nil Amount refunds what is left of the
// ticket (order_items.total minus its live refunds).
type Item struct {
	TicketID uuid.UUID
	Amount   *int64
}

// BatchInput is one refund operation.
type BatchInput struct {
	OrgID   uuid.UUID
	OrderID uuid.UUID
	Items   []Item
	// Reason is required (it reaches the provider and the audit trail).
	Reason string
	// CancelTickets: cancel each ticket once the provider accepts its
	// refund (spec default true). False is a money-only partial refund.
	CancelTickets bool
	// NotifyBuyer is stored for the buyer letter (PAY-07).
	NotifyBuyer bool
	// IdempotencyKey is the Idempotency-Key of the create call, unique per
	// organization: a replay answers the first batch and writes nothing.
	IdempotencyKey string
	Actor          Actor
	// Via is the client channel (X-Client-Channel), for the audit trail.
	Via string
	// Approved: the caller holds refund.approve, so the rows skip
	// 'requested' and are driven at once (spec §7 step 2).
	Approved bool
}

// BatchResult is the outcome of CreateBatch.
type BatchResult struct {
	BatchID  uuid.UUID
	OrderID  uuid.UUID
	Replayed bool
	Refunds  []Refund
}

// order is the slice of orders CreateBatch needs.
type order struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	CheckoutSessionID uuid.UUID
	Source            string
	Currency          string
}

const maxBatchItems = 200

func validateBatch(in BatchInput) *Error {
	bad := func(msg string) *Error { return refusal(http.StatusBadRequest, CodeInvalidRequest, msg, nil) }
	key := strings.TrimSpace(in.IdempotencyKey)
	switch {
	case key == "" || len(key) > 255:
		return bad("an Idempotency-Key of 1 to 255 characters is required")
	case strings.TrimSpace(in.Reason) == "":
		return bad("reason is required")
	case len(in.Items) == 0:
		return bad("at least one ticket is required")
	case len(in.Items) > maxBatchItems:
		return bad(fmt.Sprintf("at most %d tickets per refund", maxBatchItems))
	}
	seen := make(map[uuid.UUID]bool, len(in.Items))
	for _, it := range in.Items {
		if it.TicketID == uuid.Nil || seen[it.TicketID] {
			return bad("every ticket must be named once")
		}
		seen[it.TicketID] = true
		if it.Amount != nil && *it.Amount <= 0 {
			return bad("an amount must be a positive number of minor units")
		}
	}
	return nil
}

// CreateBatch records a refund operation and, when Approved, drives it.
// A returned *Error means nothing was written.
func (e *Engine) CreateBatch(ctx context.Context, in BatchInput) (BatchResult, error) {
	if verr := validateBatch(in); verr != nil {
		return BatchResult{}, verr
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.Reason = strings.TrimSpace(in.Reason)

	// Read the order and its payment first: the advisory lock below must be
	// the first statement of the writing transaction, and it is keyed on the
	// payment.
	var (
		ord order
		pay Payment
	)
	if err := e.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		ord, pay, err = loadOrderPayment(ctx, tx, in.OrgID, in.OrderID)
		return err
	}); err != nil {
		return BatchResult{}, err
	}
	route := e.RouteFor(pay.Provider, ord.Source)
	if route == RouteUnknownProvider {
		route = RouteUnsupported
	}
	if ref := RouteRefusal(route, pay.Provider); ref != nil {
		return BatchResult{}, ref
	}
	desc, _ := e.modules.Descriptor(pay.Provider)

	var res BatchResult
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		replay, found, err := findBatch(ctx, tx, in.OrgID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if replay.OrderID != in.OrderID {
				return refusal(http.StatusConflict, CodeIdempotencyKeyReused,
					"this Idempotency-Key was already used for another order", nil)
			}
			res = replay
			return nil
		}
		plan, perr := planBatch(ctx, tx, in, ord, pay, desc.Capabilities.PartialRefund)
		if perr != nil {
			return perr
		}
		created, err := insertBatch(ctx, tx, in, pay, plan)
		if err != nil {
			return err
		}
		res = created
		ids := make([]string, 0, len(created.Refunds))
		tickets := make([]string, 0, len(created.Refunds))
		var total int64
		for _, r := range created.Refunds {
			ids = append(ids, r.ID.String())
			if r.TicketID != nil {
				tickets = append(tickets, r.TicketID.String())
			}
			total += r.Amount
		}
		return e.writeAudit(ctx, tx, audit.Event{
			ActorType: in.Actor.Type, ActorID: in.Actor.ID,
			Action: "v1.refund.batch_create", ResourceType: "refund_batch", ResourceID: created.BatchID.String(),
			Metadata: map[string]any{
				"order_id": in.OrderID.String(), "payment_intent_id": pay.ID.String(), "provider": pay.Provider,
				"refund_ids": ids, "ticket_ids": tickets, "amount": total, "currency": pay.Currency,
				"cancel_tickets": in.CancelTickets, "notify_buyer": in.NotifyBuyer, "approved": in.Approved,
				"via": in.Via,
			},
		})
	})
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) {
			return BatchResult{}, refErr
		}
		return BatchResult{}, err
	}
	if res.Replayed || !in.Approved {
		return res, nil
	}
	for i, r := range res.Refunds {
		driven, derr := e.Drive(ctx, r.ID)
		if derr != nil {
			// The row is committed provider_pending; refund.sweep picks it
			// up. The caller still gets the batch.
			e.logger.Error("refunds: drive after create failed", "refund_id", r.ID.String(), "error", derr.Error())
			continue
		}
		res.Refunds[i] = driven
	}
	return res, nil
}

func loadOrderPayment(ctx context.Context, tx pgx.Tx, orgID, orderID uuid.UUID) (order, Payment, error) {
	var o order
	err := tx.QueryRow(ctx,
		`SELECT id, org_id, checkout_session_id, source, currency FROM orders WHERE id = $1 AND org_id = $2`,
		orderID, orgID).Scan(&o.ID, &o.OrgID, &o.CheckoutSessionID, &o.Source, &o.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, Payment{}, refusal(http.StatusNotFound, CodeOrderNotFound, "order not found", nil)
	}
	if err != nil {
		return o, Payment{}, err
	}
	pay, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payment_intents
		WHERE checkout_session_id = $1 AND org_id = $2 AND state = 'succeeded'
		ORDER BY succeeded_at DESC NULLS LAST, created_at DESC LIMIT 1`, o.CheckoutSessionID, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		if o.Source == orderSourceGateway {
			return o, Payment{}, RouteRefusal(RouteSellerSite, providerManual)
		}
		return o, Payment{}, refusal(http.StatusConflict, CodeNothingToRefund,
			"the order has no payment arena took", nil)
	}
	return o, pay, err
}

func findBatch(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, key string) (BatchResult, bool, error) {
	var res BatchResult
	err := tx.QueryRow(ctx, `SELECT id, order_id FROM refund_batches WHERE org_id = $1 AND idempotency_key = $2`,
		orgID, key).Scan(&res.BatchID, &res.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, false, nil
	}
	if err != nil {
		return res, false, err
	}
	rows, err := tx.Query(ctx, `SELECT `+refundColumns+` FROM refunds WHERE batch_id = $1 ORDER BY created_at, id`, res.BatchID)
	if err != nil {
		return res, false, err
	}
	res.Refunds, err = scanRefunds(rows)
	res.Replayed = true
	return res, true, err
}

// plannedRefund is one row insertBatch writes.
type plannedRefund struct {
	TicketID uuid.UUID
	Amount   int64
}

// planBatch runs every check of spec §7 step 1 under the lock and returns
// the rows to write.
func planBatch(ctx context.Context, tx pgx.Tx, in BatchInput, ord order, pay Payment, partialOK bool) ([]plannedRefund, error) {
	ids := make([]uuid.UUID, len(in.Items))
	for i, it := range in.Items {
		ids[i] = it.TicketID
	}
	type ticketInfo struct {
		status     string
		paid       *int64
		refunded   int64
		liveCancel bool
	}
	info := make(map[uuid.UUID]*ticketInfo, len(ids))
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.status, oi.total,
		       COALESCE((SELECT SUM(r.amount) FROM refunds r
		                  WHERE r.ticket_id = t.id AND r.state NOT IN ('failed', 'rejected')), 0)::bigint,
		       EXISTS (SELECT 1 FROM refunds r
		                WHERE r.ticket_id = t.id AND r.cancel_ticket AND r.state NOT IN ('failed', 'rejected'))
		FROM   tickets t
		LEFT JOIN order_items oi ON oi.ticket_id = t.id AND oi.order_id = $2
		WHERE  t.id = ANY($1) AND t.checkout_session_id = $3`, ids, ord.ID, ord.CheckoutSessionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		ti := &ticketInfo{}
		if err := rows.Scan(&id, &ti.status, &ti.paid, &ti.refunded, &ti.liveCancel); err != nil {
			rows.Close()
			return nil, err
		}
		info[id] = ti
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	notRefundable := func(id uuid.UUID, why string) *Error {
		return refusal(http.StatusConflict, CodeTicketNotRefundable, "the ticket cannot be refunded: "+why,
			map[string]any{"ticket_id": id.String(), "reason": why})
	}
	plan := make([]plannedRefund, 0, len(in.Items))
	var batchTotal int64
	for _, it := range in.Items {
		ti, ok := info[it.TicketID]
		switch {
		case !ok:
			return nil, notRefundable(it.TicketID, "not_in_order")
		case ti.status != "active":
			return nil, notRefundable(it.TicketID, "not_active")
		case ti.paid == nil:
			return nil, notRefundable(it.TicketID, "no_price")
		case ti.liveCancel:
			return nil, refusal(http.StatusConflict, CodeInProgress, "a refund of this ticket is already in progress",
				map[string]any{"ticket_id": it.TicketID.String()})
		}
		remaining := *ti.paid - ti.refunded
		if remaining <= 0 {
			return nil, refusal(http.StatusConflict, CodeNothingToRefund, "nothing is left to refund on this ticket",
				map[string]any{"ticket_id": it.TicketID.String()})
		}
		amount := remaining
		if it.Amount != nil {
			amount = *it.Amount
		}
		if amount > remaining {
			return nil, refusal(http.StatusConflict, CodeAmountExceeds, "the amount exceeds what is left to refund on this ticket",
				map[string]any{"ticket_id": it.TicketID.String(), "requested": amount, "remaining": remaining})
		}
		plan = append(plan, plannedRefund{TicketID: it.TicketID, Amount: amount})
		batchTotal += amount
	}

	var already int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
		WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected')`, pay.ID).Scan(&already); err != nil {
		return nil, err
	}
	if already+batchTotal > pay.Amount {
		return nil, refusal(http.StatusConflict, CodeAmountExceeds, "the refunds would exceed the payment",
			map[string]any{"requested": batchTotal, "remaining": pay.Amount - already})
	}
	if !partialOK && (already != 0 || batchTotal != pay.Amount) {
		return nil, refusal(http.StatusUnprocessableEntity, CodePartialNotSupported,
			"this payment provider can only refund the whole payment at once", nil)
	}
	return plan, nil
}

func insertBatch(ctx context.Context, tx pgx.Tx, in BatchInput, pay Payment, plan []plannedRefund) (BatchResult, error) {
	res := BatchResult{OrderID: in.OrderID}
	if err := tx.QueryRow(ctx, `
		INSERT INTO refund_batches (org_id, order_id, payment_intent_id, idempotency_key, reason,
		                            notify_buyer, cancel_tickets, created_by, via)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		in.OrgID, in.OrderID, pay.ID, in.IdempotencyKey, in.Reason, in.NotifyBuyer, in.CancelTickets,
		strPtr(in.Actor.ID), strPtr(in.Via)).Scan(&res.BatchID); err != nil {
		return res, err
	}
	state := StateRequested
	if in.Approved {
		state = StateProviderPending
	}
	requestedBy := strPtr(in.Actor.ID)
	for _, p := range plan {
		r, err := scanRefund(tx.QueryRow(ctx, `
			INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, batch_id, amount, currency, reason,
			                     requested_by, state, approved_at, settlement, provider, origin, cancel_ticket)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			        CASE WHEN $10 = 'provider_pending' THEN now() END, 'provider', $11, 'arena', $12)
			RETURNING `+refundColumns,
			pay.ID, in.OrgID, in.OrderID, p.TicketID, res.BatchID, p.Amount, pay.Currency, in.Reason,
			requestedBy, state, pay.Provider, in.CancelTickets))
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return res, refusal(http.StatusConflict, CodeInProgress, "a refund of this ticket is already in progress",
					map[string]any{"ticket_id": p.TicketID.String()})
			}
			return res, err
		}
		res.Refunds = append(res.Refunds, r)
	}
	return res, nil
}
