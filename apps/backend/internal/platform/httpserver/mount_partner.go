package httpserver

import "github.com/go-chi/chi/v5"

// mountAllocationRoutes mounts the external allocation quota endpoints
// (feature #145).
func (s *Server) mountAllocationRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.allocationQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "allocation.read", "allocations")
		pr.Get("/organizations/{org_id}/external-allocations", s.handleListExternalAllocations)
		pr.Get("/organizations/{org_id}/external-allocations/{id}", s.handleGetExternalAllocation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "allocation.create", "allocations")
		pr.Post("/organizations/{org_id}/external-allocations", s.handleCreateExternalAllocation)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "allocation.update", "allocations")
		pr.Patch("/organizations/{org_id}/external-allocations/{id}", s.handlePatchExternalAllocation)
	})
}

// mountComplimentaryRoutes mounts complimentary ticket issuance + revocation
// (features #148, #150).
func (s *Server) mountComplimentaryRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.complimentaryQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.read", "complimentary")
		pr.Get("/organizations/{org_id}/complimentary", s.handleListComplimentaryIssuances)
		pr.Get("/organizations/{org_id}/complimentary/{id}", s.handleGetComplimentaryIssuance)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.issue", "complimentary")
		pr.Post("/organizations/{org_id}/complimentary", s.handleCreateComplimentaryIssuance)
	})
	// Revocation (POST /v1/complimentary/{id}/revoke, feature #150) reuses the
	// complimentary.issue permission — the documented contract is that the
	// "complimentary.revoke" capability is implied by complimentary.issue, since
	// the same actor who can issue a comp should also be able to revoke it.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "complimentary.issue", "complimentary")
		pr.Post("/complimentary/{id}/revoke", s.handleRevokeComplimentaryIssuance)
	})
}

// mountReconciliationRoutes mounts external reconciliation report endpoints
// (feature #147).
func (s *Server) mountReconciliationRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.reconciliationQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "reconciliation.read", "reconciliation")
		pr.Get("/reconciliation/reports/{id}", s.handleGetReconciliationReport)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "reconciliation.review", "reconciliation")
		pr.Get("/reconciliation/exceptions", s.handleListReconciliationExceptions)
		pr.Patch("/reconciliation/reports/{id}/review", s.handleReviewReconciliationReport)
		pr.Patch("/reconciliation/reports/{id}/lines/{line_id}", s.handleResolveReconciliationException)
	})
	if s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "reconciliation.submit", "reconciliation")
			pr.Post("/reconciliation/reports", s.handleSubmitReconciliationReport)
		})
	}
}
