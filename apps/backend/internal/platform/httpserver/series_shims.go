package httpserver

import "net/http"

// Tours of an organization (migration 0222, hcatalog/series.go).
func (s *Server) handleListSeries(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListSeries(w, r)
}

func (s *Server) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetSeries(w, r)
}

func (s *Server) handleCreateSeries(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateSeries(w, r)
}

func (s *Server) handleUpdateSeries(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateSeries(w, r)
}

func (s *Server) handleArchiveSeries(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleArchiveSeries(w, r)
}
