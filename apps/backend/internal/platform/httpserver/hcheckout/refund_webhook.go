package hcheckout

import (
	"log/slog"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// processRefundWebhook handles a VERIFIED refund event of the per-config route
// (spec 36 section 7, PAY-05). The refund is found by (provider, provider
// refund id) inside the organization that owns the config; one that is not
// ours is acknowledged and nothing is written (the organizer's provider
// account also refunds payments of their other sites). A refund we do own is
// handed to the refund engine, which moves only a provider_pending row, under
// the payment lock, so a replay or a reordered delivery changes nothing.
//
// A failure to record answers 500 so the provider redelivers: losing a
// refund outcome is worse than seeing it twice.
func (h *Handler) processRefundWebhook(w http.ResponseWriter, r *http.Request, route webhookRoute) {
	ev := *route.Event
	ack := func(reason string, processed bool) {
		h.recordWebhookEvent(ev.Type, webhookOutcomeIgnored)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   ev.Type,
			"processed":    processed,
			"reason":       reason,
		})
	}
	if route.ExpectedOrgID == nil || route.Provider == "" || h.refundEngine == nil || ev.RefundStatus == "" {
		ack("refund event; nothing to record", false)
		return
	}
	res := payments.RefundResult{
		ProviderRefundID: ev.ProviderRefundID,
		Status:           ev.RefundStatus,
		FailureCode:      ev.FailureCode,
		FailureMessage:   ev.FailureMessage,
	}
	found, err := h.refundEngine.ApplyProviderRefund(r.Context(), *route.ExpectedOrgID, route.Provider, ev.ProviderRefundID, res)
	if err != nil {
		h.logger.Error("webhook: recording a refund event failed",
			slog.String("provider", route.Provider),
			slog.String("provider_refund_id", ev.ProviderRefundID),
			slog.String("error", err.Error()),
		)
		h.recordWebhookEvent(ev.Type, webhookOutcomeError)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"webhook.refund_failed", "failed to record the refund event", r))
		return
	}
	if !found {
		ack("not an arena refund", false)
		return
	}
	h.recordWebhookEvent(ev.Type, webhookOutcomeProcessed)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"event_type":   ev.Type,
		"processed":    true,
	})
}
