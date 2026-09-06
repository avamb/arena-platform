// customers_shims.go bridges the *Server god-object to the hcustomers
// sub-package (feature #482, W1-A4d, spec §12.3). Thin delegating methods
// keep the routing table's method surface uniform with the other domains
// (inventory_shims.go, etc.) while the actual handler bodies live in
// hcustomers/.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcustomers"
)

// customersHandler constructs an hcustomers.Handler from the server's
// dependencies. A fresh handler per request keeps the wiring uniform with
// hinventory / hbilling / hgeo and avoids stale captures when test code
// mutates *Server fields between calls.
func (s *Server) customersHandler() *hcustomers.Handler {
	return hcustomers.New(s.customerQueries, s.logger)
}

func (s *Server) handleListCustomers(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.customersHandler().HandleList(w, r)
}

func (s *Server) handleGetCustomer(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.customersHandler().HandleGet(w, r)
}
