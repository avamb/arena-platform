// barcodes.go — barcode authority federation model (feature #142).
//
// The barcode authority federation supports multiple barcode origin systems
// (Arena Platform, legacy Bil24, external platforms, guest lists). Each
// barcode is scoped to exactly one authority; duplicate external references
// within the same authority are rejected at the database level (UNIQUE
// constraint).
//
// Endpoints:
//
//	POST   /v1/barcodes/authorities        (barcode.create)
//	GET    /v1/barcodes/authorities        (barcode.read)
//
// The flat single-barcode register/get/revoke and scan routes were retired
// on 2026-11-21. Barcodes are written by ticket issuance and the batch
// import, and checked at the gate by MACS and the Bil24 SCAN_TICKET.
package hbarcode

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// BarcodeAuthorityResponse is the JSON representation of a barcode_authorities row.
type BarcodeAuthorityResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

// BarcodeAuthorityFromRow maps a gen.BarcodeAuthorityRow into the JSON response shape.
func BarcodeAuthorityFromRow(r gen.BarcodeAuthorityRow) BarcodeAuthorityResponse {
	return BarcodeAuthorityResponse{
		ID:        r.ID.String(),
		Type:      r.Type,
		Label:     r.Label,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// nil-guard helper
// ─────────────────────────────────────────────────────────────────────────────

// barcodeQueriesAvailable returns true when h.barcodeQueries is wired up.
// On nil it writes a 503 dependency.database_unavailable envelope and returns false.
func (h *Handler) barcodeQueriesAvailable(w http.ResponseWriter, r *http.Request) bool {
	if h.barcodeQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/barcodes/authorities
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateBarcodeAuthority creates a new barcode authority.
//
// Request body:
//
//	{ "type": "platform"|"legacy_bil24"|"external_platform"|"guest_list", "label": "..." }
//
// Returns 201 with the created authority on success.
// Returns 400 when the request body is missing required fields or type is invalid.
func (h *Handler) HandleCreateBarcodeAuthority(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}

	var body struct {
		Type  string `json:"type"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_body", "request body must be valid JSON", r,
		))
		return
	}

	validTypes := map[string]bool{
		"platform": true, "legacy_bil24": true,
		"external_platform": true, "guest_list": true,
	}
	if !validTypes[body.Type] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_authority_type",
			"type must be one of: platform, legacy_bil24, external_platform, guest_list", r,
		))
		return
	}
	if body.Label == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.missing_label", "label is required", r,
		))
		return
	}

	authority, err := h.barcodeQueries.InsertBarcodeAuthority(r.Context(), body.Type, body.Label)
	if err != nil {
		h.logger.Error("barcode: insert authority failed",
			slog.String("type", body.Type),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.create_authority_failed", "failed to create barcode authority", r,
		))
		return
	}

	h.logger.Info("barcode: authority created",
		slog.String("authority_id", authority.ID.String()),
		slog.String("type", authority.Type),
	)
	httputil.WriteJSON(w, http.StatusCreated, BarcodeAuthorityFromRow(authority))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/barcodes/authorities
// ─────────────────────────────────────────────────────────────────────────────

// HandleListBarcodeAuthorities returns all registered barcode authorities.
func (h *Handler) HandleListBarcodeAuthorities(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}

	authorities, err := h.barcodeQueries.ListBarcodeAuthorities(r.Context())
	if err != nil {
		h.logger.Error("barcode: list authorities failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.list_authorities_failed", "failed to list barcode authorities", r,
		))
		return
	}

	result := make([]BarcodeAuthorityResponse, 0, len(authorities))
	for _, a := range authorities {
		result = append(result, BarcodeAuthorityFromRow(a))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"authorities": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// isUniqueViolation detects PostgreSQL unique constraint violations (SQLSTATE 23505).
// ─────────────────────────────────────────────────────────────────────────────

// IsUniqueViolation returns true when err is a PostgreSQL unique_violation (23505).
// Re-used by other domains (e.g. wp_webhooks) via the package-shim forwarder.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return ContainsSQLState(err, "23505")
}

// ContainsSQLState checks whether the error message contains a specific SQLSTATE code.
// This is a lightweight alternative to importing pgconn just for error type assertions.
func ContainsSQLState(err error, code string) bool {
	type pgErr interface {
		SQLState() string
	}
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == code
	}
	// Fallback: check the error string (covers wrapped errors in tests).
	return len(err.Error()) >= 5 && err.Error()[:5] == code
}
