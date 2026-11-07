// external_refund.go — П-14 (spec 62 §14.6): the operator action "refund
// outside the platform" for an order.
//
//	POST /v1/organizations/{org_id}/orders/{order_id}/external-refund   (refund.create)
//	body: {"items": [{"ticket_id", "amount_minor", "provider_refund_ref"}]}
//
// The money was returned by the organizer in the payment provider's own
// cabinet (an order of the WordPress era, paid through AllPay on the site).
// Arena moves no money: in ONE transaction it cancels each named ticket
// through CancelTicketInTx with the Refund record, so the ticket's inventory
// and capacity are released, its barcodes revoked, and the refund registry gets
// a settlement='external' row with the FINAL amount returned for that ticket.
//
// The rule is one final refund per ticket: a ticket that already has an
// external refund answers 409 with the amount recorded. The amount may not
// exceed what the buyer paid for the ticket (order_items.total). After the
// commit v1.ticket.cancelled is published once per ticket, which the sites and
// MACS hear as ticket.refunded. Nothing here e-mails the buyer: the refund
// letter is driven by the provider refund engine's operations, and the
// external path (like the gateway's REFUND_TICKET) has none.
package htickets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

const (
	externalRefundBodyLimit  = 64 << 10
	externalRefundMaxItems   = 200
	externalRefundMaxRefLen  = 120
	externalRefundReason     = "refund outside the platform"
	externalRefundAuditEvent = "v1.order.external_refund"
)

type externalRefundItemRequest struct {
	TicketID          string `json:"ticket_id"`
	AmountMinor       int64  `json:"amount_minor"`
	ProviderRefundRef string `json:"provider_refund_ref"`
}

type externalRefundRequest struct {
	Items []externalRefundItemRequest `json:"items"`
}

type externalRefundItemResponse struct {
	TicketID          string `json:"ticket_id"`
	RefundID          string `json:"refund_id"`
	AmountMinor       int64  `json:"amount_minor"`
	Currency          string `json:"currency"`
	ProviderRefundRef string `json:"provider_refund_ref"`
}

type externalRefundResponse struct {
	OrderID     string                       `json:"order_id"`
	OrderStatus string                       `json:"order_status"`
	TotalMinor  int64                        `json:"total_minor"`
	Currency    string                       `json:"currency"`
	Items       []externalRefundItemResponse `json:"items"`
}

// ExternalRefundItem is one validated line of an external refund.
type ExternalRefundItem struct {
	TicketID          uuid.UUID
	AmountMinor       int64
	ProviderRefundRef string
}

// ExternalRefundParams is the input of ExternalRefundOrder.
type ExternalRefundParams struct {
	OrgID   uuid.UUID
	OrderID uuid.UUID
	// ActorID is the caller's UUID (audit_events.actor_id is a uuid column).
	ActorID string
	Items   []ExternalRefundItem
}

// ExternalRefundResult is what the committed transaction recorded.
type ExternalRefundResult struct {
	OrderStatus string
	Currency    string
	Total       int64
	Items       []externalRefundItemResponse
}

// ExternalRefundError is a refusal with the HTTP shape the route answers.
type ExternalRefundError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Err     error
}

func (e *ExternalRefundError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *ExternalRefundError) Unwrap() error { return e.Err }

func extFail(status int, code, msg string, details map[string]any) *ExternalRefundError {
	return &ExternalRefundError{Status: status, Code: code, Message: msg, Details: details}
}

func extInternal(code, msg string, err error) *ExternalRefundError {
	return &ExternalRefundError{Status: http.StatusInternalServerError, Code: code, Message: msg, Err: err}
}

// HandleExternalRefund serves POST .../orders/{order_id}/external-refund.
func (h *Handler) HandleExternalRefund(w http.ResponseWriter, r *http.Request) {
	if h.ticketQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	items, ok := decodeExternalRefundItems(w, r)
	if !ok {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	res, err := h.ExternalRefundOrder(r.Context(), ExternalRefundParams{
		OrgID: orgID, OrderID: orderID, ActorID: actor.ID, Items: items,
	})
	if err != nil {
		var xe *ExternalRefundError
		if errors.As(err, &xe) {
			if xe.Err != nil {
				h.logger.Error("order.external_refund failed", slog.String("code", xe.Code), slog.Any("error", xe.Err))
			}
			httputil.WriteJSON(w, xe.Status, httputil.ErrorEnvelopeWithDetails(xe.Code, xe.Message, r, xe.Details))
			return
		}
		h.logger.Error("order.external_refund failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"order.external_refund_failed", "failed to record the refund", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, externalRefundResponse{
		OrderID: orderID.String(), OrderStatus: res.OrderStatus, TotalMinor: res.Total,
		Currency: res.Currency, Items: res.Items,
	})
}

func decodeExternalRefundItems(w http.ResponseWriter, r *http.Request) ([]ExternalRefundItem, bool) {
	bad := func(code, msg string, field string) ([]ExternalRefundItem, bool) {
		var d map[string]any
		if field != "" {
			d = map[string]any{"field": field}
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, d))
		return nil, false
	}
	var body externalRefundRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, externalRefundBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return bad("order.invalid_body", "request body is not valid JSON: "+err.Error(), "")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return bad("order.invalid_body", "request body must hold exactly one JSON object", "")
	}
	if len(body.Items) == 0 || len(body.Items) > externalRefundMaxItems {
		return bad("order.invalid_items", "items must hold between 1 and 200 tickets", "items")
	}
	seen := map[uuid.UUID]bool{}
	out := make([]ExternalRefundItem, 0, len(body.Items))
	for _, it := range body.Items {
		id, err := uuid.Parse(strings.TrimSpace(it.TicketID))
		if err != nil {
			return bad("order.invalid_ticket_id", "ticket_id must be a valid UUID", "items.ticket_id")
		}
		if seen[id] {
			return bad("order.duplicate_ticket", "a ticket may appear only once", "items.ticket_id")
		}
		seen[id] = true
		if it.AmountMinor <= 0 {
			return bad("order.invalid_amount", "amount_minor must be a positive number of minor units", "items.amount_minor")
		}
		ref := strings.TrimSpace(it.ProviderRefundRef)
		if len([]rune(ref)) > externalRefundMaxRefLen {
			return bad("order.invalid_provider_refund_ref", "provider_refund_ref is too long", "items.provider_refund_ref")
		}
		out = append(out, ExternalRefundItem{TicketID: id, AmountMinor: it.AmountMinor, ProviderRefundRef: ref})
	}
	return out, true
}

// ExternalRefundOrder records the external refunds of one order in ONE
// transaction. Any refusal rolls everything back.
func (h *Handler) ExternalRefundOrder(ctx context.Context, p ExternalRefundParams) (ExternalRefundResult, error) {
	var res ExternalRefundResult
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return res, &ExternalRefundError{Status: http.StatusServiceUnavailable,
			Code: "dependency.database_unavailable", Message: "failed to begin transaction", Err: err}
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := h.ticketQueries.WithTx(tx)

	// The refund engine's payment lock comes FIRST, before the order row, in
	// the order the engine itself takes them: a provider refund of the same
	// ticket racing this one must see the external amount, or the two
	// together could return more than the ticket was paid. An order without
	// a payment (a gateway order) has nothing to lock.
	if lerr := lockOrderPayments(ctx, tx, p.OrderID, p.OrgID); lerr != nil {
		return res, extInternal("order.external_refund_failed", "failed to lock the order's payment", lerr)
	}

	// A foreign organization's order is indistinguishable from a missing one.
	order, err := txq.LockOrderForUpdate(ctx, p.OrderID, p.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, extFail(http.StatusNotFound, "orders.not_found", "order not found", nil)
	}
	if err != nil {
		return res, extInternal("order.external_refund_failed", "failed to load the order", err)
	}
	if order.Status != "paid" && order.Status != "partially_refunded" {
		return res, extFail(http.StatusConflict, "order.not_refundable",
			"a refund can be recorded only for a paid order", map[string]any{"current_status": order.Status})
	}
	lines, err := txq.ListOrderTicketDetails(ctx, order.ID)
	if err != nil {
		return res, extInternal("order.external_refund_failed", "failed to load the order's tickets", err)
	}
	byTicket := make(map[uuid.UUID]gen.OrderTicketDetailRow, len(lines))
	for _, l := range lines {
		byTicket[l.TicketID] = l
	}

	// Validate every line BEFORE the first write, so the answer names the
	// first problem and nothing is half done.
	for _, it := range p.Items {
		line, in := byTicket[it.TicketID]
		if !in {
			return res, extFail(http.StatusUnprocessableEntity, "order.ticket_not_in_order",
				"the ticket does not belong to this order", map[string]any{"ticket_id": it.TicketID.String()})
		}
		var amount int64
		var exists bool
		qerr := tx.QueryRow(ctx, `
			SELECT amount, true FROM refunds
			WHERE  ticket_id = $1 AND settlement = 'external'`, it.TicketID).Scan(&amount, &exists)
		if qerr == nil {
			return res, extFail(http.StatusConflict, "order.external_refund_exists",
				"a refund is already recorded for this ticket", map[string]any{
					"ticket_id": it.TicketID.String(), "amount_minor": amount, "currency": order.Currency})
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return res, extInternal("order.external_refund_failed", "failed to check earlier refunds", qerr)
		}
		if line.Status != "active" {
			return res, extFail(http.StatusConflict, "ticket.not_active",
				"only an active ticket can be refunded", map[string]any{
					"ticket_id": it.TicketID.String(), "current_status": line.Status})
		}
		// What is left of the ticket's price after every earlier refund
		// that may still return money (a partial provider refund leaves the
		// ticket active).
		var refunded int64
		if serr := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
			WHERE  ticket_id = $1 AND state NOT IN ('failed', 'rejected')`, it.TicketID).Scan(&refunded); serr != nil {
			return res, extInternal("order.external_refund_failed", "failed to sum earlier refunds", serr)
		}
		if remaining := line.Price - refunded; it.AmountMinor > remaining {
			return res, extFail(http.StatusUnprocessableEntity, "order.refund_exceeds_price",
				"the amount is more than what is left of the ticket's price", map[string]any{
					"ticket_id": it.TicketID.String(), "price_minor": line.Price,
					"refunded_minor": refunded, "remaining_minor": max(remaining, 0),
					"amount_minor": it.AmountMinor})
		}
	}

	now := time.Now().UTC()
	actor := p.ActorID
	var outcomes []CancelTicketOutcome
	var cancelParams []CancelTicketParams
	for _, it := range p.Items {
		amount := it.AmountMinor
		cp := CancelTicketParams{
			TicketID:     it.TicketID,
			Reason:       externalRefundReason,
			RefundMode:   RefundModeManual,
			RefundAmount: &amount,
			ActorType:    "user",
			ActorID:      actor,
			ScanGuard:    true,
			Refund: &CancelRefundRecord{
				Date: now, Price: &amount, OrgID: p.OrgID,
				Reason: externalRefundReason, RequestedBy: actor,
			},
		}
		out, cerr := h.CancelTicketInTx(ctx, tx, cp)
		if cerr != nil {
			return res, &ExternalRefundError{Status: cerr.Status, Code: cerr.Code, Message: cerr.Message,
				Details: map[string]any{"ticket_id": it.TicketID.String()}, Err: cerr.Err}
		}
		outcomes = append(outcomes, out)
		cancelParams = append(cancelParams, cp)
		var refundID uuid.UUID
		if rerr := tx.QueryRow(ctx, `
			UPDATE refunds SET external_ref = NULLIF($2, ''), updated_at = now()
			WHERE  ticket_id = $1 AND settlement = 'external'
			RETURNING id`, it.TicketID, it.ProviderRefundRef).Scan(&refundID); rerr != nil {
			return res, extInternal("order.external_refund_failed", "failed to record the refund", rerr)
		}
		res.Items = append(res.Items, externalRefundItemResponse{
			TicketID: it.TicketID.String(), RefundID: refundID.String(), AmountMinor: it.AmountMinor,
			Currency: order.Currency, ProviderRefundRef: it.ProviderRefundRef,
		})
		res.Total += it.AmountMinor
	}

	// The order aggregate: refunded once EVERY ticket is cancelled.
	status := "partially_refunded"
	remaining := 0
	for _, l := range lines {
		if l.Status != "active" {
			continue
		}
		if _, hit := indexOf(p.Items, l.TicketID); !hit {
			remaining++
		}
	}
	if remaining == 0 {
		status = "refunded"
	}
	if _, uerr := txq.UpdateOrderStatus(ctx, order.ID, order.OrgID, status, nil, nil); uerr != nil {
		return res, extInternal("order.external_refund_failed", "failed to update the order", uerr)
	}
	payload, _ := json.Marshal(map[string]any{
		"reason": externalRefundReason, "refund_mode": RefundModeManual, "settlement": "external",
		"total_minor": res.Total, "tickets": len(p.Items), "refund_date": now.Format(time.RFC3339),
	})
	if _, eerr := txq.InsertOrderEvent(ctx, order.ID, "ticket_refunded", actorLabel(actor), payload); eerr != nil {
		return res, extInternal("order.external_refund_failed", "failed to write the order history", eerr)
	}
	if h.audit != nil {
		refs := make([]map[string]any, 0, len(res.Items))
		for _, it := range res.Items {
			refs = append(refs, map[string]any{"ticket_id": it.TicketID, "amount_minor": it.AmountMinor,
				"provider_refund_ref": it.ProviderRefundRef})
		}
		if aerr := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt: now, ActorType: "user", ActorID: actor,
			Action: externalRefundAuditEvent, ResourceType: "order", ResourceID: order.ID.String(),
			RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx),
			Metadata: map[string]any{"currency": order.Currency, "total_minor": res.Total, "refunds": refs,
				"order_status": status},
		}); aerr != nil {
			return res, extInternal("order.external_refund_failed", "failed to write audit event", aerr)
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return res, extInternal("order.external_refund_failed", "failed to commit the refund", cerr)
	}
	for i := range outcomes {
		h.PublishTicketCancelled(ctx, outcomes[i], cancelParams[i])
	}
	res.OrderStatus = status
	res.Currency = order.Currency
	return res, nil
}

func indexOf(items []ExternalRefundItem, id uuid.UUID) (int, bool) {
	for i, it := range items {
		if it.TicketID == id {
			return i, true
		}
	}
	return -1, false
}

func actorLabel(id string) string {
	if id == "" {
		return "operator"
	}
	return "user:" + id
}

// lockOrderPayments takes the refund engine's advisory lock
// (refunds.LockPayment) on every succeeded payment of the order's checkout,
// in id order so two callers never wait on each other crosswise.
func lockOrderPayments(ctx context.Context, tx pgx.Tx, orderID, orgID uuid.UUID) error {
	rows, err := tx.Query(ctx, `
		SELECT pi.id FROM orders o
		JOIN   payment_intents pi ON pi.checkout_session_id = o.checkout_session_id AND pi.org_id = o.org_id
		WHERE  o.id = $1 AND o.org_id = $2 AND pi.state = 'succeeded'
		ORDER  BY pi.id`, orderID, orgID)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := refunds.LockPayment(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
