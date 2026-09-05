// cmd_tickets.go — Bil24-compatible ticket commands: SCAN_TICKET (barcode
// validation + scan recording). Extracted from bil24_compat.go by feature
// #476 so per-command files stay well under 700 lines.
package hbil24

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// SCAN_TICKET — validate and record a barcode scan (ScanTicket)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24ScanTicket maps SCAN_TICKET to the barcode scan validation flow.
//
// Bil24 request fields used:
//   - ticketId: barcode external_ref (or UUID if already on platform)
//
// The scan uses the "legacy_bil24" barcode authority type. If no such
// authority exists, returns NOT_FOUND.
//
// Response:
//
//	{ "resultCode": 0, "command": "SCAN_TICKET", "scanStatus": "OK", "ticketId": "..." }
func (h *Handler) handleBil24ScanTicket(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.barcodeQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "scan service unavailable",
		))
		return
	}

	// Feature #390 / PR2-32: credential enforcement for SCAN_TICKET.
	// SCAN_TICKET mutates state (MarkBarcodeScanned) and therefore requires
	// the same fid/token validation as RESERVATION and UN_RESERVE. Quick-reject
	// when the token is absent — no DB round-trip needed.
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		h.logger.Warn("bil24_compat: SCAN_TICKET: token missing in request; rejecting",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for SCAN_TICKET",
		))
		return
	}
	// Full credential validation: resolve the channel addressed by fid and
	// verify the token against its stored gateway_token_hash.
	if h.requireToken {
		if !h.validateScanTicketToken(r.Context(), w, req) {
			return
		}
	}

	if req.TicketID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticketId is required",
		))
		return
	}

	ctx := r.Context()

	// Resolve the legacy_bil24 barcode authority.
	authority, err := h.barcodeQueries.GetBarcodeAuthorityByType(ctx, "legacy_bil24")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"legacy_bil24 barcode authority not registered; "+
					"create it first via POST /v1/barcodes/authorities",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: authority lookup failed",
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve barcode authority",
		))
		return
	}

	// Look up the barcode by (authority_id, external_ref).
	barcode, err := h.barcodeQueries.GetBarcodeByRef(ctx, authority.ID, req.TicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "ticket not found",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: barcode lookup failed",
			slog.String("ticket_id", req.TicketID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to look up ticket",
		))
		return
	}

	// Guard against already-scanned barcodes.
	if barcode.Status == "scanned" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticket already scanned",
		))
		return
	}

	// Guard against revoked barcodes.
	if barcode.Status == "revoked" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticket has been revoked",
		))
		return
	}

	// Atomically mark as scanned.
	scanned, err := h.barcodeQueries.MarkBarcodeScanned(ctx, barcode.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest, "ticket already scanned",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: mark scanned failed",
			slog.String("barcode_id", barcode.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to record scan",
		))
		return
	}

	h.logger.Info("bil24_compat: SCAN_TICKET: scan recorded",
		slog.String("barcode_id", scanned.ID.String()),
		slog.String("external_ref", scanned.ExternalRef),
	)

	scanResult := map[string]any{
		"scanStatus": "OK",
		"ticketId":   req.TicketID,
	}
	if scanned.TicketID != nil {
		scanResult["platformTicketId"] = TranslatePlatformID(*scanned.TicketID)
	}
	if scanned.ScannedAt != nil {
		scanResult["scannedAt"] = scanned.ScannedAt.UTC().Format(time.RFC3339)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, scanResult))
}
