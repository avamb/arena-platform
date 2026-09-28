// bot_shims.go bridges the *Server god-object to the hbot sub-package
// (Telegram event-center bot, 08_architecture/28_telegram_event_center_bot_ru.md).
// All handler logic lives in hbot/; these thin methods keep the unexported
// *Server surface the mount files use.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbot"
)

// botHandler constructs an hbot.Handler from the server's dependencies.
func (s *Server) botHandler() *hbot.Handler {
	username := ""
	if s.cfg != nil {
		username = s.cfg.EventsTelegramBotUsername
	}
	return hbot.New(s.membershipQueries, s.pool, s.audit, s.logger).
		WithMembershipQueries(s.membershipQueries).
		WithBotUsername(username)
}

func (s *Server) handleCreateBotInvitation(w http.ResponseWriter, r *http.Request) {
	s.botHandler().HandleCreateInvitation(w, r)
}

func (s *Server) handleAcceptBotInvitation(w http.ResponseWriter, r *http.Request) {
	s.botHandler().HandleAcceptInvitation(w, r)
}
