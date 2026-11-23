package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// requireBarcodeSuperadmin refuses every caller that the server did not mark
// as a platform superadmin. Barcode authorities and external barcode batches
// are platform records shared by every organization, so organization owners
// and API keys never reach them. Refused callers get 403
// barcode.superadmin_only.
func (s *Server) requireBarcodeSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasSuperadminOrgAccess(r.Context()) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"barcode.superadmin_only", "platform superadmin access is required", r,
			))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountBarcodeRoutes mounts the barcode authority list and create routes
// (feature #142). The flat single-barcode, scan and scanner routes were
// retired on 2026-11-21: the gate is MACS, the Bil24 SCAN_TICKET and the
// scan-events callbacks below. Creating an authority is for a platform
// superadmin only.
func (s *Server) mountBarcodeRoutes(r chi.Router) {
	if s.authEnabled() && s.barcodeQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.read", "barcodes")
			pr.Get("/barcodes/authorities", s.handleListBarcodeAuthorities)
		})
	}
	if s.authEnabled() && s.barcodeQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "barcode.create", "barcodes")
			pr.Use(s.requireBarcodeSuperadmin)
			pr.Post("/barcodes/authorities", s.handleCreateBarcodeAuthority)
		})
	}
}

// mountScannerCallbackRoutes mounts the external-scanner ingest endpoint
// (feature #293 / S-2).  The route is intentionally OUTSIDE the JWT-gated
// applyAuth groups because it is authenticated via an agent_feed_tokens
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
	// Scans MACS posts back (task M1): authenticated by the HMAC of the MACS
	// subscriber named in X-MACS-Subscriber-Id, not by a JWT. Always mounted,
	// self-gated with 503 like the scanner callback above.
	r.Post("/macs/scan-events", s.handleMACSScanEvents)
}

// mountBarcodeBatchRoutes mounts external barcode batch import endpoints
// (feature #146). Every batch route is for a platform superadmin only.
func (s *Server) mountBarcodeBatchRoutes(r chi.Router) {
	if !s.authEnabled() || s.barcodeBatchQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.read", "barcode_batches")
		pr.Use(s.requireBarcodeSuperadmin)
		pr.Get("/barcode-batches", s.handleListBarcodeBatches)
		pr.Get("/barcode-batches/{id}", s.handleGetBarcodeBatch)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.upload", "barcode_batches")
		pr.Use(s.requireBarcodeSuperadmin)
		pr.Post("/barcode-batches", s.handleUploadBarcodeBatch)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "barcode_batch.approve", "barcode_batches")
		pr.Use(s.requireBarcodeSuperadmin)
		pr.Post("/barcode-batches/{id}/approve", s.handleApproveBarcodeBatch)
		pr.Post("/barcode-batches/{id}/reject", s.handleRejectBarcodeBatch)
	})
}
