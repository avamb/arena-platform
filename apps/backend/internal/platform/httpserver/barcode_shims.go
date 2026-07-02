// barcode_shims.go bridges the *Server god-object to the hbarcode sub-package.
// All handler bodies live in hbarcode/; these thin delegating methods preserve
// the unexported *Server method surface so mount_scanning.go, test files, and
// wp_webhooks.go (the cross-domain isUniqueViolation user) compile unchanged.
package httpserver

import (
	"errors"
	"io"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbarcode"
)

// barcodeHandler constructs an hbarcode.Handler from the server's dependencies.
// A fresh handler per request keeps the wiring uniform with htickets / hcheckout
// and avoids stale captures when test code mutates *Server fields between calls.
func (s *Server) barcodeHandler() *hbarcode.Handler {
	return hbarcode.New(
		s.barcodeQueries,
		s.barcodeBatchQueries,
		s.pool,
		s.logger,
	)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference response types that now
// live in hbarcode without importing that package directly.

type barcodeAuthorityResponse = hbarcode.BarcodeAuthorityResponse
type barcodeResponse = hbarcode.BarcodeResponse

// ─── pure-function forwarders ─────────────────────────────────────────────────
// Tests call these unqualified — keep the lowercase names live in package
// httpserver so the test source-grep helpers find the symbols inside the
// aggregated shim+sub-package content, and other domain files (e.g.
// wp_webhooks.go) keep using isUniqueViolation via the same package.

func barcodeAuthorityFromRow(r gen.BarcodeAuthorityRow) barcodeAuthorityResponse {
	return hbarcode.BarcodeAuthorityFromRow(r)
}

func barcodeFromRow(r gen.BarcodeRow) barcodeResponse {
	return hbarcode.BarcodeFromRow(r)
}

func parseBarcodeBatchCSV(r io.Reader) ([]string, error) {
	return hbarcode.ParseBarcodeBatchCSV(r)
}

// isUniqueViolation is also consumed by wp_webhooks.go — keep the unexported
// package-level identifier alive here so that caller does not have to learn
// about the hbarcode sub-package.
func isUniqueViolation(err error) bool {
	return hbarcode.IsUniqueViolation(err)
}

// Compile-time witness so the goimports tool keeps "errors" in the import list
// even when no shim above touches it directly.
var _ = errors.New

// ─── barcode handler shims ────────────────────────────────────────────────────

func (s *Server) handleCreateBarcodeAuthority(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleCreateBarcodeAuthority(w, r)
}

func (s *Server) handleListBarcodeAuthorities(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleListBarcodeAuthorities(w, r)
}

func (s *Server) handleRegisterBarcode(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleRegisterBarcode(w, r)
}

func (s *Server) handleGetBarcode(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleGetBarcode(w, r)
}

func (s *Server) handleRevokeBarcode(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleRevokeBarcode(w, r)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleScan(w, r)
}

// ─── barcode-batch handler shims ──────────────────────────────────────────────

func (s *Server) handleUploadBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleUploadBarcodeBatch(w, r)
}

func (s *Server) handleListBarcodeBatches(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleListBarcodeBatches(w, r)
}

func (s *Server) handleGetBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleGetBarcodeBatch(w, r)
}

func (s *Server) handleApproveBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleApproveBarcodeBatch(w, r)
}

func (s *Server) handleRejectBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	s.barcodeHandler().HandleRejectBarcodeBatch(w, r)
}
