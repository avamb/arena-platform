// refund_engine_routes.go — the flat refund routes' PAY-03 checks: refuse a
// payment arena cannot refund through before anything is written, and
// answer an approval with the refund AFTER its provider call.
package hcheckout

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// writeRefundError answers a refunds.Error.
func writeRefundError(w http.ResponseWriter, r *http.Request, e *refunds.Error) {
	if len(e.Details) > 0 {
		httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelopeWithDetails(e.Code, e.Message, r, e.Details))
		return
	}
	httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelope(e.Code, e.Message, r))
}

// refundRouteAllowed is the create route's pre-check on the pool. A missing
// payment passes: the transactional path answers its own 404. A foreign one
// answers the same 404 as a missing one (PAY-00) before its route could
// leak anything.
func (h *Handler) refundRouteAllowed(w http.ResponseWriter, r *http.Request, paymentIntentID uuid.UUID) bool {
	pi, err := h.paymentIntentQueries.GetPaymentIntentByID(r.Context(), paymentIntentID)
	if err != nil {
		return true
	}
	if !h.rowOrgAllowed(w, r, pi.OrgID, true, "refund.payment_intent_not_found", "payment intent not found") {
		return false
	}
	if ref := refunds.RouteRefusal(refundRoute(r.Context(), h.refundQueries, pi), pi.Provider); ref != nil {
		writeRefundError(w, r, ref)
		return false
	}
	return true
}

// approvePrecheckResult is what the approve route decided before its
// transaction.
type approvePrecheckResult struct {
	paymentIntentID *uuid.UUID
	route           refunds.Route
}

// approvePrecheck loads the refund and its payment on the pool and decides
// the route. ok=false means a response was written. A refund or payment
// that cannot be read here is left to the transactional path, which answers
// its own 404.
func (h *Handler) approvePrecheck(w http.ResponseWriter, r *http.Request, id uuid.UUID) (approvePrecheckResult, bool) {
	var res approvePrecheckResult
	ctx := r.Context()
	rf, err := h.refundQueries.GetRefundByID(ctx, id)
	if err != nil || rf.PaymentIntentID == nil || h.paymentIntentQueries == nil {
		return res, true
	}
	if !h.rowOrgAllowed(w, r, rf.OrgID, true, "refund.not_found", "refund not found") {
		return res, false
	}
	res.paymentIntentID = rf.PaymentIntentID
	pi, err := h.paymentIntentQueries.GetPaymentIntentByID(ctx, *rf.PaymentIntentID)
	if err != nil {
		return res, true
	}
	res.route = refundRoute(ctx, h.refundQueries, pi)
	if rf.State != "requested" {
		// The transactional path answers refund.invalid_state.
		return res, true
	}
	if ref := refunds.RouteRefusal(res.route, pi.Provider); ref != nil {
		writeRefundError(w, r, ref)
		return res, false
	}
	if res.route == refunds.RouteArenaProvider && h.refundEngine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			refunds.CodeEngineUnavailable, "refunds cannot be sent to the payment provider right now", r,
		))
		return res, false
	}
	return res, true
}

// respondDrivenRefund drives an approved refund through its provider and
// answers 200 with the refund as it stands afterwards: succeeded, failed
// (failure_reason says why; the tickets are untouched), or still
// provider_pending (an asynchronous provider, or an unknown outcome
// refund.sweep retries). The approval itself is committed in every case, so
// the route never answers an error for the provider's verdict.
func (h *Handler) respondDrivenRefund(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	if _, err := h.refundEngine.Drive(ctx, id); err != nil {
		h.logger.Error("refund: provider call after approval failed; refund.sweep retries it",
			slog.String("id", id.String()), slog.String("error", err.Error()))
	}
	row, err := h.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.get_failed", "failed to retrieve refund", r,
		))
		return
	}
	h.logger.Info("refund: approved and sent to the provider",
		slog.String("id", id.String()), slog.String("state", row.State))
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"refund": refundFromRow(row)})
}
