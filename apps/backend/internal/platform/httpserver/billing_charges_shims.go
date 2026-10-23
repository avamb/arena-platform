package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
)

// billingChargesHandler builds the handler of the organizer's read-only
// billing charges (task B2-6). Both routes carry {org_id}, so the caller's
// membership and billing.read are checked in the PATH organization before the
// handler runs.
func (s *Server) billingChargesHandler() *hbilling.ChargesHandler {
	return hbilling.NewCharges(s.pgxPool, s.logger)
}

func (s *Server) handleListOrgCharges(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.billingChargesHandler().HandleListCharges(w, r)
}

func (s *Server) handleGetEventBillingSummary(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.billingChargesHandler().HandleEventSummary(w, r)
}
