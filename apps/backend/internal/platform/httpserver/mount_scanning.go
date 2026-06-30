package httpserver

import "github.com/go-chi/chi/v5"

// mountBarcodeRoutes mounts barcode authority federation (feature #142).
func (s *Server) mountBarcodeRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.barcodeQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.read", "barcodes")
			pr.Get("/barcodes/authorities", s.handleListBarcodeAuthorities)
			pr.Get("/barcodes/{id}", s.handleGetBarcode)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.scan", "barcodes")
			pr.Post("/scan", s.handleScan)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.barcodeQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.create", "barcodes")
			pr.Post("/barcodes/authorities", s.handleCreateBarcodeAuthority)
			pr.Post("/barcodes", s.handleRegisterBarcode)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.revoke", "barcodes")
			pr.Delete("/barcodes/{id}", s.handleRevokeBarcode)
		})
	}
}

// mountScannerRoutes mounts offline scanner snapshot + online validate
// (feature #144).
func (s *Server) mountScannerRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.barcodeQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode.scan", "barcodes")
		pr.Get("/scanner/snapshot", s.handleScannerSnapshot)
		pr.Post("/scanner/validate", s.handleScannerValidate)
	})
}

// mountScannerCallbackRoutes mounts the external-scanner ingest endpoint
// (feature #293 / S-2).  The route is intentionally OUTSIDE the JWT-gated
// applyAuth group above because it is authenticated via an agent_feed_tokens
// bearer presented in the Authorization header rather than a session JWT.
//
// Always mounted (handler self-gates on s.feedTokenQueries == nil with a
// 503 dependency.database_unavailable envelope) so that the route is
// reachable for the openapi-drift coverage check (feature #278, A-17) and
// the drift test does not need to wire a *gen.Queries handle (which would
// cascade and mount every other feed-token-gated route in mount_catalog.go).
// Mirrors the unconditional-mount + self-gating precedent established by
// POST /v1/admin/tickets/{id}/delivery/resend (feature #276, A-15).
func (s *Server) mountScannerCallbackRoutes(r chi.Router) {
	r.Post("/scanner/scan-events", s.handleScannerScanEvents)
}

// mountBarcodeBatchRoutes mounts external barcode batch import endpoints
// (feature #146).
func (s *Server) mountBarcodeBatchRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.barcodeBatchQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.read", "barcode_batches")
		pr.Get("/barcode-batches", s.handleListBarcodeBatches)
		pr.Get("/barcode-batches/{id}", s.handleGetBarcodeBatch)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.upload", "barcode_batches")
		pr.Post("/barcode-batches", s.handleUploadBarcodeBatch)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.approve", "barcode_batches")
		pr.Post("/barcode-batches/{id}/approve", s.handleApproveBarcodeBatch)
		pr.Post("/barcode-batches/{id}/reject", s.handleRejectBarcodeBatch)
	})
}
