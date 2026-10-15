package httpserver

// ticketsBaseURL is PUBLIC_TICKETS_BASE_URL, the origin of the hosted sales
// pages, or "" when it is not configured. The event card's public_url is
// built from it (hcatalog.Handler.WithTicketsBaseURL).
func (s *Server) ticketsBaseURL() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.PublicTicketsBaseURL
}
