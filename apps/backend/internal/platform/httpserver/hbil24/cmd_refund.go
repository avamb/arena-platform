// cmd_refund.go — REFUND_TICKET, the arena extension of the Bil24 command
// set (feature #509, W1-B8, spec §7.13). The WordPress site calls it when an
// operator refunds a single ticket in WooCommerce: the gateway runs the very
// same cancellation transaction the operator UI runs
// (htickets.Handler.CancelTicketTx) with refund_mode='manual' and audit actor
// "gateway:<fid>", records the money side on the ticket
// (tickets.refund_date / refund_price), projects the outcome onto the order
// aggregate (orders.status → refunded | partially_refunded plus an
// order_events.ticket_refunded row), and answers {ticketId, refundDate}.
//
// The cancellation itself — inventory release, capacity restore, barcode
// revocation, the v1.ticket.cancelled outbox event that fans out to the site
// webhook (ticket.refunded, §9.2) and to MACS — lives entirely in htickets;
// this file is the protocol shell around it.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// handleBil24RefundTicket implements REFUND_TICKET (spec §7.13).
//
// Bil24 request fields used:
//   - fid         : sales_channels.display_number (spec §5.2)
//   - token       : bcrypt-verified against the channel's gateway_token_hash
//   - ticketId    : tickets.system_ticket_id (int64 on the wire, spec §4)
//   - reason      : optional; defaults to "REFUND_TICKET via gateway fid=<fid>"
//   - refundPrice : optional MAJOR-unit amount; nil leaves refund_price NULL
//   - locale      : optional wire locale for description localization (§6)
//
// Result codes:
//
//	 0 — refunded, or the ticket was already cancelled (idempotent replay)
//	-2 — ticketId missing / not an int64
//	-3 — no such ticket, or it belongs to another organization
//	-4 — credential failure (written by authenticateCommand)
//	-99 — the refund surface is not wired on this deployment
func (h *Handler) handleBil24RefundTicket(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.refundQ == nil || h.refundTicket == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, "", "bil24.internal",
				"refund service unavailable", nil),
		))
		return
	}

	ctx := r.Context()

	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return // envelope already written
	}

	systemTicketID, ok := parseWireTicketID(req.TicketID)
	if !ok {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.invalid_request", "ticketId is required", nil),
		))
		return
	}

	ticket, err := h.refundQ.GetTicketBySystemTicketID(ctx, systemTicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeRefundNotFound(w, req, channel)
			return
		}
		h.logger.Error("bil24_compat: REFUND_TICKET: ticket lookup failed",
			slog.Int64("system_ticket_id", systemTicketID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.internal", "failed to look up ticket", nil),
		))
		return
	}

	// Spec §7.13: the ticket must belong to an order of the channel's
	// organization; anything else is -3 (never leak that it exists).
	orgID, scopeOK := h.refundTicketOrg(ctx, w, req, channel, authed, ticket)
	if !scopeOK {
		return // envelope already written
	}

	// Spec §7.13: an already-cancelled ticket answers 0 — the site retries
	// the same refund after a network hiccup and must not see an error, and
	// the cancellation must not run twice.
	if ticket.Status == "cancelled" {
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, refundPayload(
			ticket.SystemTicketID,
			refundDateString(ticket.RefundDate, ticket.CancelledAt),
		)))
		return
	}

	actor := gatewayRefundActor(channel.DisplayNumber)
	out, refErr := h.refundTicket(ctx, GatewayRefundInput{
		TicketID:    ticket.ID,
		OrgID:       orgID,
		Reason:      refundReason(req.Reason, channel.DisplayNumber),
		RefundPrice: refundPriceMinorUnits(req.RefundPrice),
		Actor:       actor,
	})
	if refErr != nil {
		h.logger.Error("bil24_compat: REFUND_TICKET: refund failed",
			slog.Int64("system_ticket_id", systemTicketID),
			slog.String("actor", actor),
			slog.String("error", refErr.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.internal", "failed to refund ticket", nil),
		))
		return
	}

	h.logger.Info("bil24_compat: REFUND_TICKET: ticket refunded",
		slog.Int64("system_ticket_id", ticket.SystemTicketID),
		slog.String("ticket_id", ticket.ID.String()),
		slog.String("actor", actor),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, refundPayload(
		ticket.SystemTicketID,
		out.RefundDate.UTC().Format(time.RFC3339),
	)))
}

// refundPayload builds the spec §7.13 success payload. The key names are
// the ones bil24compat.RefundTicketResponse declares — the struct is the
// documentation of the wire shape, while the handlers of this package all
// answer through the flat map bil24OK merges into the envelope.
func refundPayload(systemTicketID int64, refundDate string) map[string]any {
	return map[string]any{
		"ticketId":   systemTicketID,
		"refundDate": refundDate,
	}
}

// refundTicketOrg resolves the organization that owns the ticket and
// enforces the spec §7.13 scope rule against the authenticated channel.
// It returns ok=false after writing the envelope itself.
//
// When the caller is unauthenticated (only possible with requireToken=false,
// i.e. the pre-W1 dev-mode path the unit tests use) there is no channel to
// scope against, so the ticket's own org is returned unchecked.
func (h *Handler) refundTicketOrg(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	channel gen.SalesChannelRow,
	authed bool,
	ticket gen.TicketRow,
) (uuid.UUID, bool) {
	if h.resDeps.CtxQ == nil {
		return channel.OrgID, true
	}
	sessCtx, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, ticket.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeRefundNotFound(w, req, channel)
			return uuid.Nil, false
		}
		h.logger.Error("bil24_compat: REFUND_TICKET: session org lookup failed",
			slog.String("session_id", ticket.SessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gwDefaultLocale(channel),
				"bil24.internal", "failed to resolve session", nil),
		))
		return uuid.Nil, false
	}
	if authed && sessCtx.OrgID != channel.OrgID {
		h.logger.Warn("bil24_compat: REFUND_TICKET: cross-tenant refund rejected",
			slog.String("ticket_id", ticket.ID.String()),
			slog.String("channel_org", channel.OrgID.String()),
			slog.String("ticket_org", sessCtx.OrgID.String()),
			slog.Int64("fid_display_number", channel.DisplayNumber),
		)
		h.writeRefundNotFound(w, req, channel)
		return uuid.Nil, false
	}
	return sessCtx.OrgID, true
}

// writeRefundNotFound emits the single -3 envelope shape REFUND_TICKET uses
// for "no such ticket" and "ticket of another organization" alike — the two
// must be indistinguishable on the wire (spec §7.13).
func (h *Handler) writeRefundNotFound(w http.ResponseWriter, req bil24Request, channel gen.SalesChannelRow) {
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeNotFound,
		h.localizeDesc(req.Locale, gwDefaultLocale(channel),
			"bil24.not_found",
			"ticket not found in this channel's organization", nil),
	))
}

// parseWireTicketID accepts the wire representation of REFUND_TICKET's
// `ticketId` — tickets.system_ticket_id, a bigint that the WP client may
// serialise either as a JSON number or as a quoted string (spec §4) — and
// returns it. Empty, non-numeric or non-positive input yields ok=false.
func parseWireTicketID(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// refundReason returns the operator-supplied reason, or the spec §7.13
// default when the site left the field empty.
func refundReason(raw string, fid int64) string {
	if s := strings.TrimSpace(raw); s != "" {
		return s
	}
	return "REFUND_TICKET via gateway fid=" + strconv.FormatInt(fid, 10)
}

// gatewayRefundActor is the spec §7.13 audit/order-event principal for a
// gateway-originated refund.
func gatewayRefundActor(fid int64) string {
	return "gateway:" + strconv.FormatInt(fid, 10)
}

// refundPriceMinorUnits converts the optional MAJOR-unit wire amount into
// the integer minor units arena stores in tickets.refund_price, rounding
// half away from zero to avoid the 24.999999 → 2499 float artefact. A nil
// or negative input yields nil ("amount not decided"), which leaves the
// column NULL — the cancellation still happens either way (AB-49).
func refundPriceMinorUnits(p *float64) *int64 {
	if p == nil || *p < 0 {
		return nil
	}
	v := int64(math.Round(*p * 100))
	return &v
}

// refundDateString renders the refund timestamp of an already-cancelled
// ticket for the idempotent replay answer. tickets.refund_date is the
// authoritative value; a manual cancellation always sets it, but a ticket
// cancelled through some other path may not have one, in which case the
// cancellation timestamp is the honest answer. Both missing yields "".
func refundDateString(refundDate, cancelledAt *time.Time) string {
	if refundDate != nil {
		return refundDate.UTC().Format(time.RFC3339)
	}
	if cancelledAt != nil {
		return cancelledAt.UTC().Format(time.RFC3339)
	}
	return ""
}
