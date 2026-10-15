package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountEventMaterialRoutes mounts the page materials of an event: the gallery
// (media-items) and the video links (HP-1, task 1-2a). Reads need event.read,
// writes event.update; the handlers check the event against the path
// organization.
func (s *Server) mountEventMaterialRoutes(r chi.Router) {
	if !s.authEnabled() || s.eventQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.read", "events")
		pr.Get("/organizations/{org_id}/events/{event_id}/media-items", s.handleListEventGallery)
		pr.Get("/organizations/{org_id}/events/{event_id}/videos", s.handleListEventVideos)
	})
	if s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.update", "events")
		pr.Post("/organizations/{org_id}/events/{event_id}/media-items", s.handleAddEventGalleryItem)
		pr.Put("/organizations/{org_id}/events/{event_id}/media-items/order", s.handleReorderEventGallery)
		pr.Patch("/organizations/{org_id}/events/{event_id}/media-items/{item_id}", s.handleUpdateEventGalleryItem)
		pr.Delete("/organizations/{org_id}/events/{event_id}/media-items/{item_id}", s.handleDeleteEventGalleryItem)
		pr.Post("/organizations/{org_id}/events/{event_id}/videos", s.handleAddEventVideo)
		pr.Put("/organizations/{org_id}/events/{event_id}/videos/order", s.handleReorderEventVideos)
		pr.Delete("/organizations/{org_id}/events/{event_id}/videos/{video_id}", s.handleDeleteEventVideo)
	})
}

func (s *Server) handleListEventGallery(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListEventGallery(w, r)
}

func (s *Server) handleAddEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleAddEventGalleryItem(w, r)
}

func (s *Server) handleUpdateEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateEventGalleryItem(w, r)
}

func (s *Server) handleDeleteEventGalleryItem(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteEventGalleryItem(w, r)
}

func (s *Server) handleReorderEventGallery(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleReorderEventGallery(w, r)
}

func (s *Server) handleListEventVideos(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListEventVideos(w, r)
}

func (s *Server) handleAddEventVideo(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleAddEventVideo(w, r)
}

func (s *Server) handleDeleteEventVideo(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteEventVideo(w, r)
}

func (s *Server) handleReorderEventVideos(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleReorderEventVideos(w, r)
}
