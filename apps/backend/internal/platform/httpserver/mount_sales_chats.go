package httpserver

// mount_sales_chats.go — the sales bot's chats (spec 08_architecture/61
// sections 7a.4 and 7b, stage M5): the platform superadmin's "Telegram chats"
// screen (superadmin.read AND the platform superadmin marker, X-Admin-Reason
// and an audit row on every write) and the routes the sales bot in arena-bot
// calls with BOT_SERVICE_TOKEN (503 while it is unset, never open).

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsaleschats"
)

func (s *Server) salesChatsHandler() *hsaleschats.Handler {
	return hsaleschats.New(s.pgxPool, s.shopService(), s.audit, s.logger)
}

func (s *Server) mountSalesChatRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	h := s.salesChatsHandler()
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "superadmin")
		pr.Use(s.requireModulesSuperadmin)
		pr.Get("/admin/organizations/{org_id}/sales-chats", h.HandleListOrg)
		pr.Post("/admin/organizations/{org_id}/sales-chats/connect-codes", h.HandleCreateOrgCode)
		pr.Get("/admin/sales-chats/operator", h.HandleListOperator)
		pr.Post("/admin/sales-chats/operator/connect-codes", h.HandleCreateOperatorCode)
		pr.Patch("/admin/sales-chats/{id}", h.HandlePatch)
		pr.Delete("/admin/sales-chats/{id}", h.HandleDelete)
	})
	serviceToken := ""
	if s.cfg != nil {
		serviceToken = s.cfg.BotServiceToken
	}
	r.Group(func(pr chi.Router) {
		pr.Use(hbot.RequireServiceToken(serviceToken))
		pr.Post("/bot/shop-delivery/steps", h.HandleStep)
		pr.Post("/bot/shop-delivery/card", h.HandleCard)
		pr.Post("/bot/sales-chats/connect", h.HandleConnect)
		pr.Post("/bot/sales-chats/removed", h.HandleRemoved)
		pr.Post("/bot/sales-chats/migrated", h.HandleMigrated)
	})
}
