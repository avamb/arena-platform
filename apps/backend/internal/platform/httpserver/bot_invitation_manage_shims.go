package httpserver

// bot_invitation_manage_shims.go — the two routes that take a bot invitation
// back and send its letter again (EC-16, hbot/invitation_manage.go). Kept
// beside bot_shims.go rather than in it so that file stays as it was.

import "net/http"

func (s *Server) handleRevokeBotInvitation(w http.ResponseWriter, r *http.Request) {
	s.botHandler().HandleRevokeInvitation(w, r)
}

func (s *Server) handleResendBotInvitation(w http.ResponseWriter, r *http.Request) {
	s.botHandler().HandleResendInvitation(w, r)
}
