package hbot

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// TeamMember is one member of the organization as the bot shows it.
type TeamMember struct {
	UserID            uuid.UUID `json:"user_id"`
	Email             string    `json:"email"`
	Role              string    `json:"role"`            // owner | manager
	MembershipRole    string    `json:"membership_role"` // org_admin | organizer
	TelegramLinked    bool      `json:"telegram_linked"`
	InvitationPending bool      `json:"invitation_pending"`
	JoinedAt          time.Time `json:"joined_at"`
}

// TeamResponse is GET /v1/organizations/{org_id}/bot-team.
type TeamResponse struct {
	Members []TeamMember `json:"members"`
}

// HandleListTeam answers the organization's owners and managers with whether
// each has a Telegram account linked and whether an invitation is still
// waiting to be opened — the "Team" screen of the bot. Requires
// membership.read and membership of the organization (or the superadmin).
func (h *Handler) HandleListTeam(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if _, ok := h.requireOrgMembership(w, r, orgID); !ok {
		return
	}
	rows, err := h.queries.ListBotTeam(r.Context(), orgID)
	if err != nil {
		h.logger.Error("hbot: list team failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.team_failed", "failed to list the team", r,
		))
		return
	}
	out := TeamResponse{Members: make([]TeamMember, 0, len(rows))}
	for _, m := range rows {
		out.Members = append(out.Members, TeamMember{
			UserID:            m.UserID,
			Email:             m.Email,
			Role:              BotRoleFor(m.MembershipRole),
			MembershipRole:    m.MembershipRole,
			TelegramLinked:    m.TelegramLinked,
			InvitationPending: m.InvitationPending,
			JoinedAt:          m.JoinedAt,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
