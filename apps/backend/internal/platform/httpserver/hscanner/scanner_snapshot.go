// scanner_snapshot.go — offline validation snapshot endpoint (feature #144).
//
// Offline scanners download a barcode snapshot for a given session so they can
// validate tickets even when network connectivity is unavailable. When the
// scanner comes back online it falls back to POST /v1/scanner/validate for
// real-time validation.
//
// Endpoints:
//
//	GET  /v1/scanner/snapshot    — paginated barcode snapshot with since-cursor delta
//	POST /v1/scanner/validate    — read-only online barcode validation (no state change)
//
// Auth: both endpoints require a valid JWT with barcode.scan permission.
// The scanner device uses a service-account JWT issued to the scanning hardware.
//
// Rate limiting:
//
//	Per-IP:      600 requests/minute  (scanners poll frequently during admission)
//	Per-session: 300 requests/minute  (one session on one scanner)
//
// Snapshot delta protocol:
//
//	Full snapshot:  GET /v1/scanner/snapshot?session_id=<uuid>
//	Delta update:   GET /v1/scanner/snapshot?session_id=<uuid>&since=<RFC3339>
//
// The client stores the last_updated_at from the response and passes it as
// since on the next poll. This way only changed or newly-issued barcodes are
// transferred after the initial full download.
package hscanner

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotBarcodeResponse is the minimal barcode representation returned in the
// snapshot payload. Offline scanners only need external_ref and status to decide
// whether to admit a ticket.
type SnapshotBarcodeResponse struct {
	ID          string  `json:"id"`
	ExternalRef string  `json:"external_ref"`
	TicketID    *string `json:"ticket_id,omitempty"`
	Status      string  `json:"status"`
	UpdatedAt   string  `json:"updated_at"`
}

// ValidateBarcodeResponse is the JSON response from POST /v1/scanner/validate.
// Unlike POST /v1/scan it does NOT change the barcode status — it is a pure
// read. Callers use this when the scanner has connectivity and wants a
// definitive server-side validity check before admitting a ticket.
type ValidateBarcodeResponse struct {
	BarcodeID     string  `json:"barcode_id"`
	ExternalRef   string  `json:"external_ref"`
	AuthorityType string  `json:"authority_type"`
	TicketID      *string `json:"ticket_id,omitempty"`
	Status        string  `json:"status"`
	Valid         bool    `json:"valid"`
	InvalidReason string  `json:"invalid_reason,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// nil-guard helper
// ─────────────────────────────────────────────────────────────────────────────

// barcodeQueriesAvailable returns true when h.barcodeQueries is wired up.
// On nil it writes a 503 dependency.database_unavailable envelope and returns
// false. Mirrors the equivalent guard on the hbarcode Handler.
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
// GET /v1/scanner/snapshot
// ─────────────────────────────────────────────────────────────────────────────

// HandleScannerSnapshot returns a paginated list of non-revoked barcodes for all
// tickets in the given session. Supports delta updates via the since query
// parameter (RFC3339 timestamp).
//
// Query parameters:
//
//	session_id  string (UUID, required)  — event session whose barcodes to retrieve
//	since       string (RFC3339, opt)    — only return barcodes updated after this time
//	page        int (default 1)
//	per_page    int (default 200, max 500)
//
// Response (200):
//
//	{
//	  "barcodes":        [...],
//	  "total":           1234,
//	  "page":            1,
//	  "per_page":        200,
//	  "total_pages":     7,
//	  "last_updated_at": "2026-06-24T10:00:00Z"  // use as since on next poll
//	}
func (h *Handler) HandleScannerSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}
	ctx := r.Context()

	// ── Rate limit ──────────────────────────────────────────────────────────
	ip := httputil.ClientIP(r)
	if !h.rateLimiter.CheckIP(ip) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"scanner.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// ── Parse session_id ─────────────────────────────────────────────────────
	sessionIDStr := r.URL.Query().Get("session_id")
	if sessionIDStr == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"scanner.missing_session_id", "session_id query parameter is required", r,
		))
		return
	}
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"scanner.invalid_session_id", "session_id must be a valid UUID", r,
		))
		return
	}

	// ── Per-session rate limit ────────────────────────────────────────────────
	if !h.rateLimiter.CheckSession(sessionID.String()) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"scanner.rate_limited", "too many requests for this session", r,
		))
		return
	}

	// ── Parse since cursor (optional) ─────────────────────────────────────────
	// Zero time means "no cursor" — returns all non-revoked barcodes for session.
	var since time.Time // zero value = 1970-01-01 00:00:00 UTC
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		since, err = time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"scanner.invalid_since", "since must be a valid RFC3339 timestamp", r,
			))
			return
		}
	}

	// ── Parse pagination ──────────────────────────────────────────────────────
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	perPage := 200
	if pp := r.URL.Query().Get("per_page"); pp != "" {
		if v, err := strconv.Atoi(pp); err == nil && v > 0 {
			perPage = v
		}
	}
	if perPage > 500 {
		perPage = 500
	}
	offset := (page - 1) * perPage

	// ── Fetch count + page ────────────────────────────────────────────────────
	total, err := h.barcodeQueries.CountSnapshotBarcodesBySession(ctx, sessionID, since)
	if err != nil {
		h.logger.Error("scanner: count snapshot failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scanner.snapshot_failed", "failed to count snapshot barcodes", r,
		))
		return
	}

	barcodes, err := h.barcodeQueries.ListSnapshotBarcodesBySession(
		ctx, sessionID, since, int32(perPage), int32(offset), //nolint:gosec // perPage,offset bounded above by validation
	)
	if err != nil {
		h.logger.Error("scanner: list snapshot failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scanner.snapshot_failed", "failed to fetch snapshot barcodes", r,
		))
		return
	}

	// ── Build response ────────────────────────────────────────────────────────
	items := make([]SnapshotBarcodeResponse, 0, len(barcodes))
	var lastUpdatedAt time.Time
	for _, b := range barcodes {
		item := SnapshotBarcodeResponse{
			ID:          b.ID.String(),
			ExternalRef: b.ExternalRef,
			Status:      b.Status,
			UpdatedAt:   b.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if b.TicketID != nil {
			s := b.TicketID.String()
			item.TicketID = &s
		}
		items = append(items, item)
		if b.UpdatedAt.After(lastUpdatedAt) {
			lastUpdatedAt = b.UpdatedAt
		}
	}

	totalPages := int(total) / perPage
	if int(total)%perPage != 0 {
		totalPages++
	}
	if totalPages == 0 {
		totalPages = 1
	}

	resp := map[string]any{
		"barcodes":    items,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	}
	if !lastUpdatedAt.IsZero() {
		resp["last_updated_at"] = lastUpdatedAt.UTC().Format(time.RFC3339)
	}

	h.logger.Info("scanner: snapshot served",
		slog.String("session_id", sessionID.String()),
		slog.Int("count", len(items)),
		slog.Int64("total", total),
	)

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/scanner/validate
// ─────────────────────────────────────────────────────────────────────────────

// HandleScannerValidate performs a read-only barcode validity check.
// Unlike POST /v1/scan it does NOT mark the barcode as scanned — it simply
// reports whether the barcode is valid (active), already scanned, or revoked.
// Scanners use this when online to confirm a ticket before admitting.
//
// Request body:
//
//	{
//	  "external_ref":   "<barcode string>",
//	  "authority_type": "platform"|"legacy_bil24"|"external_platform"|"guest_list"
//	}
//
// Response (200):
//
//	{ "valid": true, "status": "active", "barcode_id": "...", ... }
//	{ "valid": false, "status": "revoked", "invalid_reason": "barcode_revoked", ... }
//	{ "valid": false, "status": "scanned", "invalid_reason": "already_scanned", ... }
func (h *Handler) HandleScannerValidate(w http.ResponseWriter, r *http.Request) {
	if !h.barcodeQueriesAvailable(w, r) {
		return
	}
	ctx := r.Context()

	// ── Rate limit ────────────────────────────────────────────────────────────
	ip := httputil.ClientIP(r)
	if !h.rateLimiter.CheckIP(ip) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"scanner.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// ── Parse body ────────────────────────────────────────────────────────────
	var body struct {
		ExternalRef   string `json:"external_ref"`
		AuthorityType string `json:"authority_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"scanner.invalid_body", "request body must be valid JSON", r,
		))
		return
	}
	if body.ExternalRef == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"scanner.missing_external_ref", "external_ref is required", r,
		))
		return
	}
	if body.AuthorityType == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"scanner.missing_authority_type", "authority_type is required", r,
		))
		return
	}

	// ── Resolve authority ─────────────────────────────────────────────────────
	authority, err := h.barcodeQueries.GetBarcodeAuthorityByType(ctx, body.AuthorityType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"scanner.unknown_authority", "authority type not found in federation", r,
			))
			return
		}
		h.logger.Error("scanner: validate — resolve authority failed",
			slog.String("authority_type", body.AuthorityType),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scanner.authority_lookup_failed", "failed to resolve barcode authority", r,
		))
		return
	}

	// ── Look up barcode ───────────────────────────────────────────────────────
	barcode, err := h.barcodeQueries.GetBarcodeByRef(ctx, authority.ID, body.ExternalRef)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"scanner.barcode_not_found", "barcode not found for this authority", r,
			))
			return
		}
		h.logger.Error("scanner: validate — get barcode by ref failed",
			slog.String("authority_id", authority.ID.String()),
			slog.String("external_ref", body.ExternalRef),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scanner.validate_failed", "failed to fetch barcode", r,
		))
		return
	}

	// ── Build validate response (no state change) ─────────────────────────────
	resp := ValidateBarcodeResponse{
		BarcodeID:     barcode.ID.String(),
		ExternalRef:   barcode.ExternalRef,
		AuthorityType: authority.Type,
		Status:        barcode.Status,
		Valid:         barcode.Status == "active",
	}
	if barcode.TicketID != nil {
		s := barcode.TicketID.String()
		resp.TicketID = &s
	}
	switch barcode.Status {
	case "revoked":
		resp.InvalidReason = "barcode_revoked"
	case "scanned":
		resp.InvalidReason = "already_scanned"
	}

	h.logger.Info("scanner: validate",
		slog.String("barcode_id", barcode.ID.String()),
		slog.String("authority_type", authority.Type),
		slog.String("external_ref", barcode.ExternalRef),
		slog.String("status", barcode.Status),
		slog.Bool("valid", resp.Valid),
	)

	httputil.WriteJSON(w, http.StatusOK, resp)
}
