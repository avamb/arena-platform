package httpserver

import "net/http"

// sales_state_shims.go — the session's sales state (migration 0182, package
// salesstate): one state for every sales channel, set by the owner or a
// manager from the event center or the Telegram bot.

func (s *Server) handleGetSessionSalesState(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetSessionSalesState(w, r)
}

func (s *Server) handleSetSessionSalesState(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleSetSessionSalesState(w, r)
}

func (s *Server) handleListSessionSalesStateHistory(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListSessionSalesStateHistory(w, r)
}
