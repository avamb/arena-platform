package httpserver

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbot"
)

// mountBotRoutes mounts the Telegram event-center bot's two routes
// (08_architecture/28_telegram_event_center_bot_ru.md §3.3).
//
// The org-scoped invitation route is an ordinary membership.grant surface
// (a JWT user who is a member, an organization API key of that org, or the
// platform superadmin with X-Admin-Reason). The accept route is called by
// the bot PROCESS, not by a user: it is guarded by BOT_SERVICE_TOKEN alone
// and answers 503 while that token is unset — never open.
func (s *Server) mountBotRoutes(r chi.Router) {
	if !s.authEnabled() || s.membershipQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.grant", "memberships")
		pr.Post("/organizations/{org_id}/bot-invitations", s.handleCreateBotInvitation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.read", "memberships")
		pr.Get("/organizations/{org_id}/bot-team", s.handleListBotTeam)
	})
	// EC-16: send the letter again (membership.grant, like the invitation
	// itself) and take an invitation back (membership.revoke, because it may
	// remove the membership it created). The manager holds neither.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.grant", "memberships")
		pr.Post("/organizations/{org_id}/bot-invitations/{id}/resend", s.handleResendBotInvitation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.revoke", "memberships")
		pr.Delete("/organizations/{org_id}/bot-invitations/{id}", s.handleRevokeBotInvitation)
	})
	serviceToken := ""
	if s.cfg != nil {
		serviceToken = s.cfg.BotServiceToken
	}
	r.Group(func(pr chi.Router) {
		pr.Use(hbot.RequireServiceToken(serviceToken))
		pr.Post("/bot/invitations/accept", s.handleAcceptBotInvitation)
	})
}
