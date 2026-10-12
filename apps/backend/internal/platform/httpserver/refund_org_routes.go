package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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
		s.applyAuth(pr, "refund.read", "refunds")
		pr.Get("/organizations/{org_id}/refunds", s.handleListOrgRefunds)
		pr.Get("/organizations/{org_id}/refunds/{refund_id}", s.handleGetOrgRefund)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "refund.create", "refunds")
		pr.Post("/organizations/{org_id}/orders/{order_id}/refunds", s.handleCreateOrderRefunds)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "refund.approve", "refunds")
		pr.Post("/organizations/{org_id}/refunds/{refund_id}/retry", s.handleRetryOrgRefund)
		pr.Post("/organizations/{org_id}/refunds/{refund_id}/resolve", s.handleResolveOrgRefund)
		pr.Post("/organizations/{org_id}/tickets/{ticket_id}/review-hold/clear", s.handleClearTicketReviewHold)
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
		TicketID   string  `json:"ticket_id"`
		Status     string  `json:"status"`
		Paid       *int64  `json:"paid"`
		Refunded   int64   `json:"refunded"`
		Refundable int64   `json:"refundable"`
		InProgress bool    `json:"in_progress"`
		Reason     *string `json:"not_refundable_reason"`
	}
	tickets := make([]ticketJSON, 0, len(opts.Tickets))
	for _, t := range opts.Tickets {
		var reason *string
		if t.NotRefundableReason != "" {
			reason = &t.NotRefundableReason
		}
		tickets = append(tickets, ticketJSON{t.TicketID.String(), t.Status, t.Paid, t.Refunded, t.Refundable, t.InProgress, reason})
	}
	active := make([]refundItemJSON, 0, len(opts.Active))
	for _, a := range opts.Active {
		active = append(active, refundItem(a))
	}
	resp := map[string]any{
		"order_id": opts.OrderID.String(), "route": string(opts.Route), "provider": opts.Provider,
		"currency": opts.Currency, "partial_refund": opts.PartialRefund,
		"tickets": tickets, "active_refunds": active,
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
		kind := "user"
		if actor.IsService() {
			kind = "api_key" // ID is the API key's id; the queue resolves its name
		}
		in.Actor = refunds.Actor{Type: kind, ID: actor.ID}
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

type refundCreatorJSON struct {
	Kind  string  `json:"kind"`
	Label *string `json:"label"`
}

type refundQueueItemJSON struct {
	refundItemJSON
	OrderNumber  *int64             `json:"order_number"`
	EventName    *string            `json:"event_name"`
	TicketNumber *int64             `json:"ticket_number"`
	Creator      *refundCreatorJSON `json:"creator"`
}

func queueItem(r *http.Request, it refunds.ListItem) refundQueueItemJSON {
	out := refundQueueItemJSON{refundItem(it.Refund), it.OrderSystemID, it.EventName, it.TicketNumber, nil}
	if it.CreatorKind != nil {
		label := it.CreatorLabel
		if it.CreatorLabelIsEmail && callerIsAPIKey(r) {
			// A member's e-mail is shown to people of the organization and the
			// platform, not to an integration acting with an API key.
			label = nil
		}
		out.Creator = &refundCreatorJSON{Kind: *it.CreatorKind, Label: label}
	}
	return out
}

// callerIsAPIKey reports whether the request acts with an organization API key.
func callerIsAPIKey(r *http.Request) bool {
	actor, ok := auth.ActorFromContext(r.Context())
	return ok && actor.IsService()
}

func (s *Server) handleListOrgRefunds(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	q := r.URL.Query()
	f := refunds.ListFilter{State: q.Get("state")}
	if v := q.Get("event_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, "event_id must be a UUID", r))
			return
		}
		f.EventID = &id
	}
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, name+" must be a non-negative number", r))
				return
			}
			*dst = n
		}
	}
	page, err := engine.List(r.Context(), orgID, f)
	if err != nil {
		var refErr *refunds.Error
		if errors.As(err, &refErr) {
			writeEngineError(w, r, refErr)
			return
		}
		s.logger.Error("refund queue failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.list_failed", "failed to read the refunds", r))
		return
	}
	items := make([]refundQueueItemJSON, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, queueItem(r, it))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"refunds": items, "total": page.Total, "has_more": page.HasMore})
}

func (s *Server) handleGetOrgRefund(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	refundID, ok := httputil.UUIDPathParam(w, r, "refund_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	d, err := engine.Get(r.Context(), orgID, refundID)
	if err != nil {
		var refErr *refunds.Error
		if errors.As(err, &refErr) {
			writeEngineError(w, r, refErr)
			return
		}
		s.logger.Error("refund detail failed", slog.String("refund_id", refundID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.get_failed", "failed to read the refund", r))
		return
	}
	type historyJSON struct {
		Action      string  `json:"action"`
		At          string  `json:"at"`
		Outcome     *string `json:"outcome"`
		State       *string `json:"state"`
		FailureCode *string `json:"failure_code"`
	}
	nonEmpty := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	history := make([]historyJSON, 0, len(d.History))
	for _, h := range d.History {
		history = append(history, historyJSON{h.Action, h.At.UTC().Format(time.RFC3339), nonEmpty(h.Outcome), nonEmpty(h.State), nonEmpty(h.FailureCode)})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"refund": queueItem(r, d.ListItem), "history": history, "history_total": d.HistoryTotal})
}
