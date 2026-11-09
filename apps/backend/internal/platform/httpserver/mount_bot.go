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
	// F-007 "Connect Telegram": the signed-in person's own link. The group
	// records NO required permission (like GET .../me): every member may link
	// their own Telegram, and the handler admits only a user with a
	// membership row in the path organization.
	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware())
		pr.Use(s.orgScopedFor("", true))
		pr.Get("/organizations/{org_id}/telegram-connect", s.handleTelegramConnectStatus)
		pr.Post("/organizations/{org_id}/telegram-connect-codes", s.handleCreateTelegramConnectCode)
	})
	serviceToken := ""
	if s.cfg != nil {
		serviceToken = s.cfg.BotServiceToken
	}
	r.Group(func(pr chi.Router) {
		pr.Use(hbot.RequireServiceToken(serviceToken))
		pr.Post("/bot/invitations/accept", s.handleAcceptBotInvitation)
		// "Open in browser" (spec 37 §4.3): a one-time web link for the
		// linked person, and the sign-ins the bot reports back.
		pr.Post("/bot/web-login-codes", s.handleCreateWebLoginCode)
		pr.Post("/bot/web-login-notices/claim", s.handleClaimWebLoginNotices)
		// F-007: the confirmation screen of a "Connect Telegram" link and the
		// press that links the account.
		pr.Post("/bot/telegram-connect/preview", s.handlePreviewTelegramConnect)
		pr.Post("/bot/telegram-connect/accept", s.handleAcceptTelegramConnect)
		// INV-09: the invitations issued from Telegram whose PDF the bot sends
		// into the issuer's chat, each handed out once.
		pr.Post("/bot/invitations/pdf-notices/claim", s.handleClaimInvitationPDFNotices)
		// The automatic guest list (package guestlist): the lists due now for
		// people linked to the bot, and how each one ended.
		pr.Post("/bot/guest-list-deliveries/claim", s.handleClaimGuestListDeliveries)
		pr.Post("/bot/guest-list-deliveries/{id}/finish", s.handleFinishGuestListDelivery)
	})
}
