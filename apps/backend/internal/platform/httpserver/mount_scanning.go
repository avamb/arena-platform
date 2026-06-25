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
