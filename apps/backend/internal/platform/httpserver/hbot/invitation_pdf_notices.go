package hbot

// invitation_pdf_notices.go — the bot's side of "the invitation PDF reaches
// the issuer's chat" (spec 46, INV-09; owner decision 2026-10-29: this is the
// way a Telegram user gets the PDF, no download link of any kind is made).
//
//	POST /v1/bot/invitations/pdf-notices/claim
//
// Called by the bot PROCESS under BOT_SERVICE_TOKEN. It hands out the staff
// invitations issued from Telegram (the bot or the Mini App) whose tickets are
// all issued, each once, and the bot then fetches tickets.pdf as the issuer
// and sends it as a document. The answer names no e-mail or phone of the guest.

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// invitationPDFNoticeBatch caps one claim.
const invitationPDFNoticeBatch = 20

// HandleClaimInvitationPDFNotices serves POST /v1/bot/invitations/pdf-notices/claim.
func (h *Handler) HandleClaimInvitationPDFNotices(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	notices, err := h.queries.ClaimInvitationPDFNotices(r.Context(), invitationPDFNoticeBatch)
	if err != nil {
		h.logger.Error("hbot: claim invitation pdf notices failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	out := make([]map[string]any, 0, len(notices))
	for _, n := range notices {
		out = append(out, map[string]any{
			"order_id":         n.OrderID.String(),
			"org_id":           n.OrgID.String(),
			"system_id":        n.SystemID,
			"telegram_user_id": n.TelegramUserID,
			"locale":           n.Locale,
			"guest_name":       n.GuestName,
			"event_name":       n.EventName,
			"session_start_at": n.SessionStartAt.UTC().Format(time.RFC3339),
			"session_timezone": n.SessionTimezone,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"notices": out})
}
