package hcheckout

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// DisputeRecorder records a verified dispute event (spec 51.3 section 4).
type DisputeRecorder interface {
	RecordProviderEvent(ctx context.Context, in disputes.RecordInput) (disputes.RecordResult, error)
}

// WithDisputes wires the dispute service into the per-config webhook route.
// Without it a dispute event is acknowledged and nothing is written.
func (h *Handler) WithDisputes(d DisputeRecorder) *Handler {
	h.disputes = d
	return h
}

func isDisputeEvent(ev *payments.NormalizedEvent) bool {
	return ev != nil && ev.Dispute != nil &&
		(ev.Kind == payments.EventDisputeOpened || ev.Kind == payments.EventDisputeUpdated)
}

// processDisputeWebhook handles a VERIFIED dispute event of the per-config
// route. A dispute of a payment that is not this organization's is
// acknowledged with nothing written (the organizer's provider account also
// serves their other sites). A failure to record answers 500 so the
// provider redelivers: a lost dispute is a lost deadline.
func (h *Handler) processDisputeWebhook(w http.ResponseWriter, r *http.Request, route webhookRoute) {
	ev := *route.Event
	ack := func(reason string, processed bool) {
		outcome := webhookOutcomeIgnored
		if processed {
			outcome = webhookOutcomeProcessed
		}
		h.recordWebhookEvent(ev.Type, outcome)
		body := map[string]any{"acknowledged": true, "event_type": ev.Type, "processed": processed}
		if reason != "" {
			body["reason"] = reason
		}
		httputil.WriteJSON(w, http.StatusOK, body)
	}
	if route.ExpectedOrgID == nil || route.Provider == "" || h.disputes == nil {
		ack("dispute event; nothing to record", false)
		return
	}
	res, err := h.disputes.RecordProviderEvent(r.Context(), disputes.RecordInput{
		OrgID: *route.ExpectedOrgID, ConfigID: route.ConfigID, Provider: route.Provider, Event: ev,
	})
	if err != nil {
		h.logger.Error("webhook: recording a dispute event failed",
			slog.String("provider", route.Provider),
			slog.String("provider_dispute_id", ev.Dispute.ID),
			slog.String("error", err.Error()),
		)
		h.recordWebhookEvent(ev.Type, webhookOutcomeError)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"webhook.dispute_failed", "failed to record the dispute event", r))
		return
	}
	if !res.Found {
		ack("not an arena payment", false)
		return
	}
	ack("", true)
}
