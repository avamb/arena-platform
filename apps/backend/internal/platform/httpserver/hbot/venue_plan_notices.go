package hbot

// venue_plan_notices.go — what the bot tells the creator of a hall plan intake
// (spec 08_architecture/64 section 4.4):
//
//	POST /v1/bot/venue-plan-notices/claim  the notices not told yet, each once
//
// The route is modelled on POST /v1/bot/web-login-notices/claim: a notice is
// marked claimed in the same statement that returns it, so a crash between the
// claim and the message loses one message and never repeats it. A notice whose
// creator has no live bot link is still claimed and comes with an empty list of
// chats (the organizer reads it on the web instead).

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// venuePlanNoticeBatch caps one claim of intake notices.
const venuePlanNoticeBatch = 50

// HandleClaimVenuePlanNotices serves POST /v1/bot/venue-plan-notices/claim.
func (h *Handler) HandleClaimVenuePlanNotices(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	notices, err := h.queries.ClaimVenuePlanNotices(r.Context(), venuePlanNoticeBatch)
	if err != nil {
		h.logger.Error("hbot: claim venue plan notices failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	out := make([]map[string]any, 0, len(notices))
	for _, n := range notices {
		chats := n.ChatIDs
		if chats == nil {
			chats = []int64{}
		}
		out = append(out, map[string]any{
			"id":                n.ID.String(),
			"kind":              n.Kind,
			"intake_id":         n.IntakeID.String(),
			"org_id":            n.OrgID.String(),
			"venue_id":          n.VenueID.String(),
			"venue_name":        n.VenueName,
			"current_draft":     n.CurrentDraft,
			"note":              n.Note,
			"telegram_user_ids": chats,
			"created_at":        n.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"notices": out})
}
