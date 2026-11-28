package httpserver

import "net/http"

// handleCreateControlSession creates a session for entry control only and its
// own container event in one request (migration 0228, MA-40). It lives in its
// own shim file because catalog_shims.go is at the file-size budget.
func (s *Server) handleCreateControlSession(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateControlSession(w, r)
}
