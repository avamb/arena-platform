package httpserver

// refund_queue_routes.go — GET /v1/organizations/{org_id}/refunds/queue (EC-V5,
// spec 62 §12.2): what the organizer has to look at in one list, for the web
// event center's «Возвраты» screen and the counter on its nav item.
//
//   - refunds: failed refunds nobody retried, refunds parked for review and
//     refunds the provider accepted but never settled (refunds.ListFilter.Queue);
//     the row is the same as GET .../refunds, so retry/resolve work on it;
//   - letters: orders whose buyer most likely has no letter (emailstatus.
//     UndeliveredLetters); the fix is the existing org-scoped
//     POST .../orders/{order_id}/resend-tickets.

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// refundQueueLimit bounds each of the two lists of one answer.
const refundQueueLimit = 50

// maxQueueReason bounds the free-form failure text of a letter row.
const maxQueueReason = 200

func (s *Server) handleRefundQueue(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	page, err := engine.List(r.Context(), orgID, refunds.ListFilter{Queue: true, Limit: refundQueueLimit})
	if err != nil {
		var refErr *refunds.Error
		if errors.As(err, &refErr) {
			writeEngineError(w, r, refErr)
			return
		}
		s.logger.Error("refund queue failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.queue_failed", "failed to read the refund queue", r))
		return
	}
	items := make([]refundQueueItemJSON, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, queueItem(r, it))
	}

	letters := []map[string]any{}
	if s.pool != nil {
		rows, lerr := emailstatus.UndeliveredLetters(r.Context(), s.pool, orgID, refundQueueLimit)
		if lerr != nil {
			s.logger.Error("undelivered letters failed", slog.String("org_id", orgID.String()), slog.String("error", lerr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.queue_failed", "failed to read the refund queue", r))
			return
		}
		for _, u := range rows {
			letters = append(letters, map[string]any{
				"order_id": u.OrderID.String(), "order_number": u.OrderNumber, "event_name": u.EventName,
				"kind": u.Kind, "problem": u.Problem, "tickets": u.Tickets,
				"recipient": u.Recipient, "reason": scrubQueueReason(u.Reason),
				"since": u.Since.UTC().Format(time.RFC3339),
			})
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"refunds": items, "letters": letters,
		"refunds_total": page.Total, "letters_total": len(letters),
		"count": page.Total + len(letters),
	})
}

// scrubQueueReason keeps a worker's free-form error short and free of
// addresses, like the ops alerts do.
func scrubQueueReason(reason string) string {
	reason = strings.TrimSpace(opsalert.ScrubEmails(reason))
	if rs := []rune(reason); len(rs) > maxQueueReason {
		reason = string(rs[:maxQueueReason])
	}
	return reason
}
