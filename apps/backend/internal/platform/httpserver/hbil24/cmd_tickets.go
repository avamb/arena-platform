// cmd_tickets.go — Bil24-compatible ticket commands: SCAN_TICKET (barcode
// validation + scan recording). Extracted from bil24_compat.go by feature
// #476 so per-command files stay well under 700 lines. Feature #472
// (W1-A1c, spec §5 item 3 + §7.14) rewired SCAN_TICKET to the unified
// authenticateCommand path (fid → display_number → channel) and to search
// barcodes across ALL authorities (platform EAN-13 + legacy_bil24 imports),
// then enforce tickets → sessions → events.org_id == channel.OrgID with
// resultCode=-3 on any cross-tenant match.
package hbil24

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// SCAN_TICKET — validate and record a barcode scan (ScanTicket)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24ScanTicket maps SCAN_TICKET to the barcode scan validation flow.
//
// Bil24 request fields used:
//   - fid      : sales_channels.display_number (spec §5.2)
//   - token    : bcrypt-verified against channel gateway_token_hash (spec §5)
//   - ticketId : barcode external_ref (any authority — spec §7.14)
//   - locale   : optional wire locale for description localization (spec §6)
//
// Resolution (spec §5 / §7.14):
//  1. authenticateCommand() resolves fid → channel, checks the token.
//  2. barcodeQueries.GetBarcodeByExternalRefAny() looks up the barcode
//     across every authority (WordPress-side clients don't know which one
//     minted the ticket).
//  3. If the barcode is linked to a platform ticket (barcode.TicketID != nil),
//     the handler walks tickets → sessions → events.org_id and rejects any
//     match whose owning org differs from the fid channel's org
//     (resultCode=-3 / bil24.not_found).
//  4. active → mark scanned; scanned/revoked → resultCode=-2.
//
// Response on success:
//
//	{ "resultCode": 0, "command": "SCAN_TICKET", "scanStatus": "OK", "ticketId": "..." }
func (h *Handler) handleBil24ScanTicket(w http.ResponseWriter, r *http.Request, req bil24Request) {
	sq := h.scanQuerier()
	if sq == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, "", "bil24.internal",
				"scan service unavailable", nil),
		))
		return
	}

	ctx := r.Context()

	// Feature #472 (W1-A1c, spec §5 item 3): SCAN_TICKET runs through the
	// unified authenticate path. When requireToken=false and no fid
	// resolves, the pre-W1 unauthenticated code path is preserved for the
	// unit-test suites that don't wire channelQ; production wiring always
	// sets channelQ so the credential gate always runs.
	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return // envelope already written
	}

	if strings.TrimSpace(req.TicketID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.invalid_request", "ticketId is required", nil),
		))
		return
	}

	// Feature #472 (spec §7.14): look up the barcode across all authorities
	// in a single round-trip. The WordPress clients pass whatever string
	// they received on issuance (an EAN-13 for platform tickets, a
	// legacy_bil24 external ref for imported batches) and do not know which
	// authority minted it.
	barcode, err := sq.GetBarcodeByExternalRefAny(ctx, req.TicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				h.localizeDesc(req.Locale, gwDefaultLocale(channel),
					"bil24.not_found", "ticket not found", nil),
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: barcode lookup failed",
			slog.String("ticket_id", req.TicketID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.internal", "failed to look up ticket", nil),
		))
		return
	}

	// Feature #472 (spec §5 item 3 + §7.14): org-scope enforcement. When
	// the caller was authenticated (authed=true, so channel is known) and
	// the barcode is linked to a platform ticket, walk tickets → sessions
	// → events.org_id and reject any match owned by a different tenant.
	// The check is skipped for unauthenticated legacy dev-mode calls
	// (authed=false, requireToken=false) since there is no channel to
	// scope against.
	if authed && barcode.TicketID != nil && h.resDeps.CtxQ != nil {
		ticket, tErr := sq.GetTicketByID(ctx, *barcode.TicketID)
		if tErr != nil {
			// A dangling barcode.ticket_id is a data-integrity bug we log
			// but do not surface — treat as not-found for the caller.
			if errors.Is(tErr, pgx.ErrNoRows) {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeNotFound,
					h.localizeDesc(req.Locale, gwDefaultLocale(channel),
						"bil24.not_found", "ticket not found", nil),
				))
				return
			}
			h.logger.Error("bil24_compat: SCAN_TICKET: ticket lookup for org check failed",
				slog.String("ticket_id", barcode.TicketID.String()),
				slog.String("error", tErr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError,
				h.localizeDesc(req.Locale, gwDefaultLocale(channel),
					"bil24.internal", "failed to resolve ticket", nil),
			))
			return
		}
		sessCtx, sErr := h.resDeps.CtxQ.GetSessionOrgContext(ctx, ticket.SessionID)
		if sErr != nil {
			if errors.Is(sErr, pgx.ErrNoRows) {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeNotFound,
					h.localizeDesc(req.Locale, gwDefaultLocale(channel),
						"bil24.not_found", "ticket not found", nil),
				))
				return
			}
			h.logger.Error("bil24_compat: SCAN_TICKET: session org lookup failed",
				slog.String("session_id", ticket.SessionID.String()),
				slog.String("error", sErr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError,
				h.localizeDesc(req.Locale, gwDefaultLocale(channel),
					"bil24.internal", "failed to resolve session", nil),
			))
			return
		}
		if sessCtx.OrgID != channel.OrgID {
			h.logger.Warn("bil24_compat: SCAN_TICKET: cross-tenant scan rejected",
				slog.String("ticket_id", barcode.TicketID.String()),
				slog.String("channel_org", channel.OrgID.String()),
				slog.String("ticket_org", sessCtx.OrgID.String()),
				slog.Int64("fid_display_number", channel.DisplayNumber),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				h.localizeDesc(req.Locale, gwDefaultLocale(channel),
					"bil24.not_found",
					"ticket not found in this channel's organization", nil),
			))
			return
		}
	}

	// Guard against already-scanned barcodes (spec §7.14: keep the
	// existing -2 semantic for double-scan detection).
	if barcode.Status == "scanned" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.invalid_request", "ticket already scanned", nil),
		))
		return
	}

	// Guard against revoked barcodes.
	if barcode.Status == "revoked" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.invalid_request", "ticket has been revoked", nil),
		))
		return
	}

	// Atomically mark as scanned.
	scanned, err := sq.MarkBarcodeScanned(ctx, barcode.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				h.localizeDesc(req.Locale, gwDefaultLocale(channel),
					"bil24.invalid_request", "ticket already scanned", nil),
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: mark scanned failed",
			slog.String("barcode_id", barcode.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.internal", "failed to record scan", nil),
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
		// Spec §4 / §7.14: platformTicketId on the wire is the int64
		// tickets.system_ticket_id (migration 0088), not the internal UUID.
		// Load the ticket row so we can emit the stable bigint identity that
		// legacy Bil24 clients expect. Fall back to the UUID string when the
		// ticket queries handle is absent (unit tests) or the row cannot be
		// read (rare — the barcode.TicketID FK guarantees it exists on the
		// production path) so a hiccup never black-holes an otherwise
		// successful scan.
		if trow, terr := sq.GetTicketByID(ctx, *scanned.TicketID); terr == nil {
			scanResult["platformTicketId"] = trow.SystemTicketID
		} else {
			h.logger.Warn("bil24_compat: SCAN_TICKET: ticket lookup for platformTicketId failed; falling back to UUID string",
				slog.String("ticket_id", scanned.TicketID.String()),
				slog.String("error", terr.Error()),
			)
			scanResult["platformTicketId"] = TranslatePlatformID(*scanned.TicketID)
		}
	}
	if scanned.ScannedAt != nil {
		scanResult["scannedAt"] = scanned.ScannedAt.UTC().Format(time.RFC3339)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, scanResult))
}

// scanQuerier returns the ScanQuerier used by handleBil24ScanTicket. The
// injected h.scanQ wins so unit tests can substitute a deterministic fake;
// otherwise the handler falls back to the concrete *gen.Queries that
// production wiring passes via New()/bil24_shims.go. Returns nil only when
// neither is set, in which case the handler self-gates with a -99
// "scan service unavailable" envelope.
func (h *Handler) scanQuerier() ScanQuerier {
	if h.scanQ != nil {
		return h.scanQ
	}
	if h.barcodeQueries != nil {
		return h.barcodeQueries
	}
	return nil
}

// gwDefaultLocale extracts the channel's default_locale (spec §5.1) from a
// SalesChannelRow when authentication resolved one, or returns "" when the
// caller is in the unauthenticated pre-W1 pass-through branch. Split out
// so every localizeDesc callsite in the SCAN_TICKET handler stays a
// single line.
func gwDefaultLocale(ch gen.SalesChannelRow) string {
	if ch.ID == uuid.Nil {
		return ""
	}
	return parseGatewaySettings(ch.Settings).DefaultLocale
}
