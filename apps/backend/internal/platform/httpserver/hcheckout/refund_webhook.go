package hcheckout

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
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
	if route.ConfigID != nil {
		// Read the refund back with the configuration that verified it.
		r = r.WithContext(refunds.WithProviderConfig(r.Context(), *route.ConfigID))
	}
	res := payments.RefundResult{
		ProviderRefundID: ev.ProviderRefundID,
		Status:           ev.RefundStatus,
		FailureCode:      ev.FailureCode,
		FailureMessage:   ev.FailureMessage,
	}
	found, err := h.refundEngine.ApplyProviderRefund(r.Context(), *route.ExpectedOrgID, route.Provider, ev.ProviderRefundID, res)
	if errors.Is(err, refunds.ErrProviderEventGivenUp) {
		ack("refund could not be confirmed with the provider; an operator is alerted", false)
		return
	}
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
		h.recordDashboardRefund(w, r, route, ack)
		return
	}
	h.recordWebhookEvent(ev.Type, webhookOutcomeProcessed)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"event_type":   ev.Type,
		"processed":    true,
	})
}

// recordDashboardRefund handles a verified refund event no arena refund
// carries: a refund the organizer made in the provider's dashboard (spec 36
// section 7). A succeeded one of one of this organization's payments is
// recorded with origin provider_dashboard and the AB-49 inbound policy is
// applied: a refund that covers the payment cancels the order's tickets, a
// smaller one flags them for human review. Anything else is acknowledged
// untouched (the organizer's provider account also serves their other sites).
func (h *Handler) recordDashboardRefund(w http.ResponseWriter, r *http.Request, route webhookRoute, ack func(string, bool)) {
	ev := *route.Event
	var outcome dashboardOutcome
	res, err := h.refundEngine.RecordDashboardRefund(r.Context(), *route.ExpectedOrgID, route.Provider, ev, h.dashboardApply(&outcome))
	if err != nil {
		// Including refunds.ErrCannotConfirm: the provider redelivers.
		h.logger.Error("webhook: recording a dashboard refund failed",
			slog.String("provider", route.Provider),
			slog.String("provider_refund_id", ev.ProviderRefundID),
			slog.String("error", err.Error()),
		)
		h.recordWebhookEvent(ev.Type, webhookOutcomeError)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"webhook.refund_failed", "failed to record the refund event", r))
		return
	}
	if res.Unconfirmed {
		ack("refund could not be confirmed with the provider; an operator is alerted", false)
		return
	}
	if res.MaybeOurs {
		ack("arena refund not recorded yet; it will be read back", false)
		return
	}
	if !res.Found {
		ack("not an arena refund", false)
		return
	}
	if res.Parked {
		h.recordWebhookEvent(ev.Type, webhookOutcomeProcessed)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   ev.Type,
			"processed":    true,
			"origin":       "provider_dashboard",
			"reason":       "the payment was already fully refunded; recorded for review",
		})
		return
	}
	if !res.Created {
		ack("refund already recorded", false)
		return
	}
	h.recordWebhookEvent(ev.Type, webhookOutcomeProcessed)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"event_type":   ev.Type,
		"processed":    true,
		"origin":       "provider_dashboard",
	})
}
