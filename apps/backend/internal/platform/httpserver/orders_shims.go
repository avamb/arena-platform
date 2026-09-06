// orders_shims.go bridges the *Server god-object to the horders sub-package
// (feature #489, W1-A6d, spec §14.2). Thin delegating methods keep the
// routing table's method surface uniform with the other domains
// (customers_shims.go, etc.) while the actual handler bodies live in
// horders/.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/horders"
)

// ordersHandler constructs an horders.Handler from the server's
// dependencies. A fresh handler per request keeps the wiring uniform with
// hcustomers / hinventory and avoids stale captures when test code mutates
// *Server fields between calls.
func (s *Server) ordersHandler() *horders.Handler {
	return horders.New(s.customerQueries, s.pool, s.logger)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ordersHandler().HandleList(w, r)
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ordersHandler().HandleGet(w, r)
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.ordersHandler().HandleCancel(w, r)
}
