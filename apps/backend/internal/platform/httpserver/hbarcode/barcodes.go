// barcodes.go — barcode authority federation model (feature #142).
//
// The barcode authority federation supports multiple barcode origin systems
// (Arena Platform, legacy Bil24, external platforms, guest lists) sharing a
// single scan validation endpoint. Each barcode is scoped to exactly one
// authority; duplicate external references within the same authority are
// rejected at the database level (UNIQUE constraint).
//
// Authority resolution in the scan flow:
//  1. Parse {external_ref, authority_type} from the request body.
//  2. Resolve the authority by type — unknown types return 404.
//  3. Look up the barcode by (authority_id, external_ref) — not found → 404.
//  4. Guard against double-scan (status == 'scanned') → 409 already_scanned.
//  5. Guard against revoked barcodes (status == 'revoked') → 409 barcode_revoked.
//  6. Atomically mark the barcode as 'scanned' via MarkBarcodeScanned.
//  7. Return the ticket_id (may be nil for external/guest-list barcodes).
//
// Endpoints:
//
//	POST   /v1/barcodes/authorities        (barcode.create)
//	GET    /v1/barcodes/authorities        (barcode.read)
//	POST   /v1/barcodes                    (barcode.create)
//	GET    /v1/barcodes/{id}               (barcode.read)
//	DELETE /v1/barcodes/{id}               (barcode.revoke)
//	POST   /v1/scan                        (barcode.scan)
package hbarcode

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// BarcodeResponse is the JSON representation of a barcodes row.
type BarcodeResponse struct {
	ID          string  `json:"id"`
	AuthorityID string  `json:"authority_id"`
	ExternalRef string  `json:"external_ref"`
	TicketID    *string `json:"ticket_id"`
	Status      string  `json:"status"`
	ScannedAt   *string `json:"scanned_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// BarcodeFromRow maps a gen.BarcodeRow into the JSON response shape.
func BarcodeFromRow(r gen.BarcodeRow) BarcodeResponse {
	resp := BarcodeResponse{
		ID:          r.ID.String(),
		AuthorityID: r.AuthorityID.String(),
		ExternalRef: r.ExternalRef,
		Status:      r.Status,
		CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.TicketID != nil {
		s := r.TicketID.String()
		resp.TicketID = &s
	}
	if r.ScannedAt != nil {
		s := r.ScannedAt.UTC().Format(time.RFC3339)
		resp.ScannedAt = &s
	}
	return resp
}

// scanResponse is the JSON response from POST /v1/scan.
// Includes the authority context and the resolved ticket_id (if any).
type scanResponse struct {
	BarcodeID     string  `json:"barcode_id"`
	AuthorityType string  `json:"authority_type"`
	ExternalRef   string  `json:"external_ref"`
	TicketID      *string `json:"ticket_id"`
	Status        string  `json:"status"`
	ScannedAt     string  `json:"scanned_at"`
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
// POST /v1/barcodes
// ─────────────────────────────────────────────────────────────────────────────

// HandleRegisterBarcode registers a new barcode in the federation.
//
// Request body:
//
//	{
//	  "authority_id":  "<uuid>",
//	  "external_ref":  "<barcode string>",
//	  "ticket_id":     "<uuid>" | null
//	}
//
// Returns 201 with the created barcode on success.
// Returns 409 when the same external_ref already exists for the authority
// (UNIQUE constraint violation → duplicate barcode rejected).
func (h *Handler) HandleRegisterBarcode(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}

	var body struct {
		AuthorityID string  `json:"authority_id"`
		ExternalRef string  `json:"external_ref"`
		TicketID    *string `json:"ticket_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_body", "request body must be valid JSON", r,
		))
		return
	}

	authorityID, err := uuid.Parse(body.AuthorityID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_authority_id", "authority_id must be a valid UUID", r,
		))
		return
	}
	if body.ExternalRef == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.missing_external_ref", "external_ref is required", r,
		))
		return
	}

	var ticketID *uuid.UUID
	if body.TicketID != nil && *body.TicketID != "" {
		tid, err := uuid.Parse(*body.TicketID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"barcode.invalid_ticket_id", "ticket_id must be a valid UUID", r,
			))
			return
		}
		ticketID = &tid
	}

	barcode, err := h.barcodeQueries.InsertBarcode(r.Context(), authorityID, body.ExternalRef, ticketID)
	if err != nil {
		// Detect unique_violation (SQLSTATE 23505) — duplicate barcode within authority.
		if IsUniqueViolation(err) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"barcode.duplicate",
				"a barcode with this external_ref already exists for the given authority", r,
			))
			return
		}
		h.logger.Error("barcode: insert failed",
			slog.String("authority_id", authorityID.String()),
			slog.String("external_ref", body.ExternalRef),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.register_failed", "failed to register barcode", r,
		))
		return
	}

	h.logger.Info("barcode: registered",
		slog.String("barcode_id", barcode.ID.String()),
		slog.String("authority_id", barcode.AuthorityID.String()),
		slog.String("external_ref", barcode.ExternalRef),
	)
	httputil.WriteJSON(w, http.StatusCreated, BarcodeFromRow(barcode))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/barcodes/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetBarcode returns a single barcode by its UUID.
func (h *Handler) HandleGetBarcode(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	barcode, err := h.barcodeQueries.GetBarcodeByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode.not_found", "barcode not found", r,
			))
			return
		}
		h.logger.Error("barcode: get by ID failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.fetch_failed", "failed to fetch barcode", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, BarcodeFromRow(barcode))
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/barcodes/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleRevokeBarcode marks a barcode as 'revoked'. Revocation is terminal.
func (h *Handler) HandleRevokeBarcode(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	barcode, err := h.barcodeQueries.RevokeBarcode(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode.not_found", "barcode not found", r,
			))
			return
		}
		h.logger.Error("barcode: revoke failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.revoke_failed", "failed to revoke barcode", r,
		))
		return
	}

	h.logger.Info("barcode: revoked",
		slog.String("barcode_id", barcode.ID.String()),
	)
	httputil.WriteJSON(w, http.StatusOK, BarcodeFromRow(barcode))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/scan  — authority-aware scan validation
// ─────────────────────────────────────────────────────────────────────────────

// HandleScan validates a barcode scan within the context of its authority.
//
// Request body:
//
//	{
//	  "external_ref":    "<barcode string>",
//	  "authority_type":  "platform"|"legacy_bil24"|"external_platform"|"guest_list"
//	}
//
// Scan flow:
//  1. Resolve authority by type → 404 if unknown (unknown authority rejected).
//  2. Look up barcode by (authority_id, external_ref) → 404 if not found.
//  3. If status == 'revoked' → 409 barcode.revoked.
//  4. Atomically transition 'active' → 'scanned' via MarkBarcodeScanned.
//     If MarkBarcodeScanned returns ErrNoRows the barcode was already scanned
//     between our GetBarcodeByRef and the UPDATE → 409 barcode.already_scanned.
//  5. Return scan result with ticket_id (nil for external/guest-list barcodes).
func (h *Handler) HandleScan(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}
	ctx := r.Context()

	var body struct {
		ExternalRef   string `json:"external_ref"`
		AuthorityType string `json:"authority_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.invalid_body", "request body must be valid JSON", r,
		))
		return
	}
	if body.ExternalRef == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.missing_external_ref", "external_ref is required", r,
		))
		return
	}
	if body.AuthorityType == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode.missing_authority_type", "authority_type is required", r,
		))
		return
	}

	// ── Step 1: Resolve authority by type ─────────────────────────────────────
	authority, err := h.barcodeQueries.GetBarcodeAuthorityByType(ctx, body.AuthorityType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Unknown authority type → reject the scan.
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode.unknown_authority",
				"authority type not found in federation", r,
			))
			return
		}
		h.logger.Error("barcode: resolve authority failed",
			slog.String("authority_type", body.AuthorityType),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.authority_lookup_failed", "failed to resolve barcode authority", r,
		))
		return
	}

	// ── Step 2: Look up barcode by (authority_id, external_ref) ───────────────
	barcode, err := h.barcodeQueries.GetBarcodeByRef(ctx, authority.ID, body.ExternalRef)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode.not_found",
				"barcode not found for this authority", r,
			))
			return
		}
		h.logger.Error("barcode: get by ref failed",
			slog.String("authority_id", authority.ID.String()),
			slog.String("external_ref", body.ExternalRef),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.fetch_failed", "failed to fetch barcode", r,
		))
		return
	}

	// ── Step 3: Guard against revoked barcodes ─────────────────────────────────
	if barcode.Status == "revoked" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"barcode.revoked", "barcode has been revoked and cannot be scanned", r,
		))
		return
	}

	// ── Step 4: Atomically mark as scanned ────────────────────────────────────
	// MarkBarcodeScanned uses WHERE status='active'; if barcode was already
	// scanned it returns ErrNoRows (double-scan protection).
	scanned, err := h.barcodeQueries.MarkBarcodeScanned(ctx, barcode.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"barcode.already_scanned", "barcode has already been scanned", r,
			))
			return
		}
		h.logger.Error("barcode: mark scanned failed",
			slog.String("barcode_id", barcode.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode.scan_failed", "failed to record scan", r,
		))
		return
	}

	h.logger.Info("barcode: scan recorded",
		slog.String("barcode_id", scanned.ID.String()),
		slog.String("authority_type", authority.Type),
		slog.String("external_ref", scanned.ExternalRef),
	)

	// ── Step 5: Build scan response ────────────────────────────────────────────
	resp := scanResponse{
		BarcodeID:     scanned.ID.String(),
		AuthorityType: authority.Type,
		ExternalRef:   scanned.ExternalRef,
		Status:        scanned.Status,
	}
	if scanned.TicketID != nil {
		s := scanned.TicketID.String()
		resp.TicketID = &s
	}
	if scanned.ScannedAt != nil {
		resp.ScannedAt = scanned.ScannedAt.UTC().Format(time.RFC3339)
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// isUniqueViolation detects PostgreSQL unique constraint violations (SQLSTATE 23505).
// ─────────────────────────────────────────────────────────────────────────────

// IsUniqueViolation returns true when err is a PostgreSQL unique_violation (23505).
// Used to convert duplicate barcode inserts into 409 Conflict responses, and also
// re-used by other domains (e.g. wp_webhooks) via the package-shim forwarder.
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
