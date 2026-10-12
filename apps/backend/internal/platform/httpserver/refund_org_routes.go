package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// mountRefundOrgRoutes mounts the organizer's refund API (PAY-06, spec 36
// section 8): what an order can return, and one refund operation per call.
// Both check membership of the path organization and load the order INSIDE
// it, so another organization's order is the route's own 404.
func (s *Server) mountRefundOrgRoutes(r chi.Router) {
	if !s.authEnabled() || s.refundQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "refund.read", "refunds")
		pr.Get("/organizations/{org_id}/orders/{order_id}/refund-options", s.handleRefundOptions)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "refund.create", "refunds")
		pr.Post("/organizations/{org_id}/orders/{order_id}/refunds", s.handleCreateOrderRefunds)
	})
}

type refundItemJSON struct {
	ID               string  `json:"id"`
	TicketID         *string `json:"ticket_id"`
	Amount           int64   `json:"amount"`
	Currency         string  `json:"currency"`
	State            string  `json:"state"`
	ProviderStatus   *string `json:"provider_status"`
	FailureCode      *string `json:"failure_code"`
	FailureReason    *string `json:"failure_reason"`
	CancelTicket     bool    `json:"cancel_ticket"`
	ProviderRefundID *string `json:"provider_refund_id"`
	CreatedAt        string  `json:"created_at"`
}

func refundItem(r refunds.Refund) refundItemJSON {
	var ticket *string
	if r.TicketID != nil {
		v := r.TicketID.String()
		ticket = &v
	}
	return refundItemJSON{
		ID: r.ID.String(), TicketID: ticket, Amount: r.Amount, Currency: r.Currency, State: r.State,
		ProviderStatus: r.ProviderStatus, FailureCode: r.FailureCode, FailureReason: r.FailureReason,
		CancelTicket: r.CancelTicket, ProviderRefundID: r.ProviderRefundID,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func writeEngineError(w http.ResponseWriter, r *http.Request, e *refunds.Error) {
	if len(e.Details) > 0 {
		httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelopeWithDetails(e.Code, e.Message, r, e.Details))
		return
	}
	httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelope(e.Code, e.Message, r))
}

func (s *Server) handleRefundOptions(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	opts, err := engine.Options(r.Context(), orgID, orderID)
	if err != nil {
		var refErr *refunds.Error
		if errors.As(err, &refErr) {
			writeEngineError(w, r, refErr)
			return
		}
		s.logger.Error("refund options failed", slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.options_failed", "failed to read the refund options", r))
		return
	}
	type ticketJSON struct {
		TicketID   string `json:"ticket_id"`
		Status     string `json:"status"`
		Paid       *int64 `json:"paid"`
		Refunded   int64  `json:"refunded"`
		Refundable int64  `json:"refundable"`
		InProgress bool   `json:"in_progress"`
	}
	tickets := make([]ticketJSON, 0, len(opts.Tickets))
	for _, t := range opts.Tickets {
		tickets = append(tickets, ticketJSON{t.TicketID.String(), t.Status, t.Paid, t.Refunded, t.Refundable, t.InProgress})
	}
	active := make([]refundItemJSON, 0, len(opts.Active))
	for _, a := range opts.Active {
		active = append(active, refundItem(a))
	}
	resp := map[string]any{
		"order_id": opts.OrderID.String(), "route": string(opts.Route), "provider": opts.Provider,
		"currency": opts.Currency, "partial_refund": opts.PartialRefund,
		"payment_left": opts.PaymentLeft, "tickets": tickets, "active_refunds": active,
	}
	if opts.Refusal != nil {
		resp["refusal"] = map[string]any{"code": opts.Refusal.Code, "message": opts.Refusal.Message}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateOrderRefunds(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	var body struct {
		Items []struct {
			TicketID string `json:"ticket_id"`
			Amount   *int64 `json:"amount"`
		} `json:"items"`
		Reason        string `json:"reason"`
		CancelTickets *bool  `json:"cancel_tickets"`
		NotifyBuyer   *bool  `json:"notify_buyer"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, "request body is not valid JSON", r))
		return
	}
	in := refunds.BatchInput{
		OrgID: orgID, OrderID: orderID, Reason: body.Reason,
		CancelTickets:  body.CancelTickets == nil || *body.CancelTickets,
		NotifyBuyer:    body.NotifyBuyer == nil || *body.NotifyBuyer,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
	for _, it := range body.Items {
		id, perr := uuid.Parse(it.TicketID)
		if perr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, "every ticket_id must be a UUID", r))
			return
		}
		in.Items = append(in.Items, refunds.Item{TicketID: id, Amount: it.Amount})
	}
	if actor, ok := auth.ActorFromContext(r.Context()); ok {
		in.Actor = refunds.Actor{Type: "user", ID: actor.ID}
	}
	if via, ok := audit.ClientChannelFromContext(r.Context()); ok {
		in.Via = via
	}
	// A caller who may approve skips the "requested" step and the refund is
	// sent at once; a key without refund.approve leaves it for an approver.
	in.Approved = s.perms != nil && s.perms.Check(r.Context(), "refund.approve", "refunds") == nil

	res, err := engine.CreateBatch(r.Context(), in)
	if err != nil {
		var refErr *refunds.Error
		if errors.As(err, &refErr) {
			writeEngineError(w, r, refErr)
			return
		}
		s.logger.Error("refund batch failed", slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.create_failed", "failed to create the refund", r))
		return
	}
	items := make([]refundItemJSON, 0, len(res.Refunds))
	for _, rf := range res.Refunds {
		items = append(items, refundItem(rf))
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"batch_id": res.BatchID.String(), "order_id": res.OrderID.String(),
		"replayed": res.Replayed, "refunds": items,
	})
}
