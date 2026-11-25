// channel_site_domains_shims.go bridges the site-domains routes of a sales
// channel (spec 65 E3) to the hcatalog sub-package.
package httpserver

import "net/http"

func (s *Server) handleGetChannelSiteDomains(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetChannelSiteDomains(w, r)
}

func (s *Server) handlePutChannelSiteDomains(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandlePutChannelSiteDomains(w, r)
}
