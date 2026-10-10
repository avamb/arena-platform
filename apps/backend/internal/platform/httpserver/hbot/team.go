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

	// Where the person's invitation stands (EC-16): InvitationState is
	// accepted, waiting (sent, not yet opened, not expired) or expired, and
	// absent for a member who has no bot invitation (added another way).
	// InvitationID is the id the revoke and resend routes take;
	// ResendAvailableAt is when a resend is allowed again (waiting and
	// expired only; absent once the minimum interval has passed).
	InvitationState     string     `json:"invitation_state,omitempty"`
	InvitationID        *uuid.UUID `json:"invitation_id,omitempty"`
	InvitationExpiresAt *time.Time `json:"invitation_expires_at,omitempty"`
	ResendAvailableAt   *time.Time `json:"resend_available_at,omitempty"`
}

// Invitation states of a team member.
const (
	InvitationAccepted = "accepted"
	InvitationWaiting  = "waiting"
	InvitationExpired  = "expired"
)

// InvitationStateOf names where an invitation stands: accepted once opened, else
// waiting until it expires, then expired. A person with no invitation row has
// no state ("").
func InvitationStateOf(hasInvitation bool, acceptedAt, expiresAt *time.Time, now time.Time) string {
	switch {
	case !hasInvitation:
		return ""
	case acceptedAt != nil:
		return InvitationAccepted
	case expiresAt != nil && expiresAt.After(now):
		return InvitationWaiting
	}
	return InvitationExpired
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
	if _, ok := h.requireOrgMembership(w, r, orgID, "membership.read"); !ok {
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
		member := TeamMember{
			UserID:            m.UserID,
			Email:             m.Email,
			Role:              BotRoleFor(m.MembershipRole),
			MembershipRole:    m.MembershipRole,
			TelegramLinked:    m.TelegramLinked,
			InvitationPending: m.InvitationPending,
			JoinedAt:          m.JoinedAt,
		}
		member.InvitationState = InvitationStateOf(m.InvitationID != nil, m.InvitationAcceptedAt, m.InvitationExpiresAt, h.now())
		if member.InvitationState != "" {
			member.InvitationID = m.InvitationID
			member.InvitationExpiresAt = m.InvitationExpiresAt
		}
		if (member.InvitationState == InvitationWaiting || member.InvitationState == InvitationExpired) && m.InvitationLastSentAt != nil {
			if at := m.InvitationLastSentAt.Add(ResendMinInterval); at.After(h.now()) {
				member.ResendAvailableAt = &at
			}
		}
		out.Members = append(out.Members, member)
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
