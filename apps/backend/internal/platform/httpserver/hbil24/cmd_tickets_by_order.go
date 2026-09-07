// cmd_tickets_by_order.go — GET_TICKETS_BY_ORDER (spec §7.10) and
// SEND_TICKETS_TO_EMAIL (spec §7.11), feature #495 (W1-B2b).
//
// These are the two commands the WordPress site runs AFTER the money has moved.
// GET_TICKETS_BY_ORDER is the poll that turns a paid order into downloadable
// PDFs; SEND_TICKETS_TO_EMAIL is the buyer pressing "send them to me again".
//
// Two design points are worth stating up front:
//
//   - "Not paid yet" is NOT an error. Spec §7.10 is explicit that an order
//     whose tickets have not been issued answers resultCode=0 with empty
//     lists, because the site polls this endpoint in a loop right after
//     PAY_ORDER and an error there would surface to the buyer as a failed
//     purchase. Only a genuinely unknown / cross-tenant order is -3.
//   - The ticket rows are assembled from the neutral orderexport projection,
//     not from gen.TicketRow. Only the projection carries the int64
//     system_seat_id that `seatId` needs and the spec §4 barcode rule (a
//     stored EAN-13 credential wins over the code derived from
//     system_ticket_id). gen.TicketRow carries neither.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET_TICKETS_BY_ORDER — spec §7.10
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetTicketsByOrder is the entry point: envelope validation and the
// optional-dependency self-gate, then the wired body.
func (h *Handler) handleBil24GetTicketsByOrder(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.OrderID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"orderId is required for GET_TICKETS_BY_ORDER",
		))
		return
	}
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for GET_TICKETS_BY_ORDER",
		))
		return
	}
	if !h.ticketsDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotImplemented,
			"GET_TICKETS_BY_ORDER is not available on this deployment",
		))
		return
	}

	ctx := r.Context()
	channel, _ := h.resolveChannelByFID(ctx, req)
	settings := parseGatewaySettings(channel.Settings)
	locale := settings.DefaultLocale
	if h.requireToken && !h.validateGatewayToken(w, req, channel.Settings) {
		return
	}

	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return
	}
	if gw.ID == uuid.Nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "ticket service unavailable",
		))
		return
	}

	order, err := resolveOrderRef(ctx, h.ticketsDeps.Q, req.OrderID, gw.OrgID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotFound, "order not found",
		))
		return
	case err != nil:
		h.ticketsTransient(w, req, "order lookup failed", err)
		return
	}

	list, err := h.ticketsProjectOrder(ctx, order)
	if err != nil {
		h.ticketsTransient(w, req, "ticket projection failed", err)
		return
	}

	ids := make([]int64, 0, len(list))
	for _, t := range list {
		ids = append(ids, t.TicketID)
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"ticketList":   list,
		"ticketIdList": ids,
	}))
}

// ticketsProjectOrder builds the spec §7.10 ticketList for a resolved order.
// An order with no issued tickets yields an EMPTY (non-nil) slice, which is the
// §7.10 "не оплачен / билеты ещё не выпущены" answer — and non-nil matters:
// a nil slice would marshal to JSON null, and the WordPress plugin iterates the
// value without a guard.
func (h *Handler) ticketsProjectOrder(
	ctx context.Context,
	order gen.OrderRow,
) ([]bil24compat.GetTicketsByOrderTicket, error) {
	out := make([]bil24compat.GetTicketsByOrderTicket, 0)

	projected, err := h.ticketsDeps.Project(ctx, order.CheckoutSessionID)
	if err != nil {
		return nil, err
	}
	if projected == nil || len(projected.Tickets) == 0 {
		return out, nil
	}

	// The checkout token is the public credential the PDF route authorises on;
	// without it there is no link to hand out, so a failure here is transient
	// rather than an empty-but-successful answer.
	cs, err := h.ticketsDeps.Q.GetCheckoutSessionByID(ctx, order.CheckoutSessionID)
	if err != nil {
		return nil, err
	}

	catalogIDs := h.ticketsCategoryPriceIDs(ctx, order.CheckoutSessionID)

	for _, t := range projected.Tickets {
		// Spec §7.10's row shape. pdfUrl and downloadUrl are the SAME link:
		// the spec writes downloadUrl as "<тот же>". Two keys exist because
		// legacy clients read one or the other, not because the deployment has
		// two endpoints.
		link := h.ticketPDFURL(cs.CheckoutToken, t.TicketUUID)
		out = append(out, bil24compat.GetTicketsByOrderTicket{
			TicketID:        t.ID,
			PDFURL:          link,
			DownloadURL:     link,
			Barcode:         ticketWireBarcode(t),
			SeatID:          t.SeatID,
			CategoryPriceID: catalogIDs[t.TicketUUID],
		})
	}
	return out, nil
}

// ticketsCategoryPriceIDs maps each ticket of the checkout session to the
// bigint wire id of its tier (spec §3.1 kind "category_price"). A failure
// anywhere degrades to a zero categoryPriceId rather than dropping the answer —
// the buyer's PDF link is what matters here, the catalog id is metadata.
func (h *Handler) ticketsCategoryPriceIDs(
	ctx context.Context,
	checkoutSessionID uuid.UUID,
) map[uuid.UUID]int64 {
	rows, err := h.ticketsDeps.Q.ListTicketsByCheckoutSession(ctx, checkoutSessionID)
	if err != nil {
		h.logger.Warn("bil24_compat: GET_TICKETS_BY_ORDER: tier lookup failed",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}

	tierOf := make(map[uuid.UUID]uuid.UUID, len(rows))
	tiers := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.TierID == nil {
			continue
		}
		tierOf[row.ID] = *row.TierID
		tiers = append(tiers, *row.TierID)
	}
	wire := h.ensureCompatIDs(ctx, compatids.KindCategoryPrice, tiers)
	if wire == nil {
		return nil
	}

	out := make(map[uuid.UUID]int64, len(tierOf))
	for ticketID, tierID := range tierOf {
		if id, ok := wire[tierID]; ok {
			out[ticketID] = id
		}
	}
	return out
}

// ticketPDFURL builds the spec §7.10 absolute link
// PUBLIC_BASE_URL + /v1/public/checkout/<token>/tickets/<uuid>/pdf.
//
// An empty PUBLIC_BASE_URL yields the bare path. That is a degraded answer, not
// a refusal: config.Validate already rejects the combination in production, so
// reaching this branch means a dev deployment, where a relative link still
// resolves against the site's own host.
func (h *Handler) ticketPDFURL(checkoutToken string, ticketID uuid.UUID) string {
	base := strings.TrimRight(strings.TrimSpace(h.ticketsDeps.PublicBaseURL), "/")
	return base + "/v1/public/checkout/" + checkoutToken + "/tickets/" + ticketID.String() + "/pdf"
}

// ticketWireBarcode implements the spec §7.10 barcode rule. orderexport already
// prefers a stored EAN-13 credential over the code derived from
// system_ticket_id; the only case it cannot cover is a ticket with no barcode
// at all, where the spec asks for the system_ticket_id rendered as a string
// (an interim shape until feature #502 gives every ticket a credential).
func ticketWireBarcode(t orderexport.Ticket) string {
	if b := strings.TrimSpace(t.Barcode); b != "" {
		return b
	}
	return strconv.FormatInt(t.ID, 10)
}

// ticketsTransient reports a retryable infrastructure failure (-1). Like
// PAY_ORDER, these commands never answer -99 for an ordinary lookup failure:
// the site polls, and a poll is safe to repeat.
func (h *Handler) ticketsTransient(w http.ResponseWriter, req bil24Request, msg string, err error) {
	h.logger.Error("bil24_compat: "+req.Command+": "+msg,
		slog.String("fid", req.FID),
		slog.String("order_id", req.OrderID),
		slog.String("error", err.Error()),
	)
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeTransient, "temporary failure, please retry",
	))
}

// ─────────────────────────────────────────────────────────────────────────────
// SEND_TICKETS_TO_EMAIL — spec §7.11
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24SendTicketsToEmail queues a delivery job for every ticket of the
// order the supplied ticketIdList belongs to (spec §7.11).
//
// The envelope carries no orderId — it is {userId, sessionId, email,
// ticketIdList} — so the order is recovered from the first ticket and the whole
// order is then re-queued. That is deliberate: the spec says "на каждый билет
// заказа", and a buyer who asks for "my tickets" means all of them, not the
// subset the plugin happened to render on the page.
func (h *Handler) handleBil24SendTicketsToEmail(w http.ResponseWriter, r *http.Request, req bil24Request) {
	email := strings.TrimSpace(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"email is required for SEND_TICKETS_TO_EMAIL",
		))
		return
	}
	if len(req.TicketIDList) == 0 {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"ticketIdList is required for SEND_TICKETS_TO_EMAIL",
		))
		return
	}
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for SEND_TICKETS_TO_EMAIL",
		))
		return
	}
	if !h.ticketsDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotImplemented,
			"SEND_TICKETS_TO_EMAIL is not available on this deployment",
		))
		return
	}

	ctx := r.Context()
	channel, _ := h.resolveChannelByFID(ctx, req)
	settings := parseGatewaySettings(channel.Settings)
	locale := settings.DefaultLocale
	if h.requireToken && !h.validateGatewayToken(w, req, channel.Settings) {
		return
	}

	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return
	}
	if gw.ID == uuid.Nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "ticket service unavailable",
		))
		return
	}

	order, err := h.sendResolveOrderFromTickets(ctx, req.TicketIDList, gw.OrgID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotFound, "ticket not found",
		))
		return
	case err != nil:
		h.ticketsTransient(w, req, "ticket lookup failed", err)
		return
	}

	queued, err := h.sendEnqueueOrderTickets(ctx, order, email)
	if err != nil {
		h.ticketsTransient(w, req, "delivery job enqueue failed", err)
		return
	}
	if queued == 0 {
		// Every ticket of the order is cancelled, or none has been issued yet.
		// There is nothing to mail and nothing the buyer can do about it, so
		// this is the user-visible 101 rather than a retryable -1.
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.no_tickets_to_send",
				"this order has no tickets to send", nil),
		))
		return
	}

	h.logger.Info("bil24_compat: SEND_TICKETS_TO_EMAIL: delivery jobs queued",
		slog.String("order_id", order.ID.String()),
		slog.Int64("order_system_id", order.SystemID),
		slog.Int("tickets", queued),
	)
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
}

// sendResolveOrderFromTickets recovers the order behind a ticketIdList. The
// first id that resolves wins; the rest are not consulted because §7.11 re-sends
// the whole order anyway. Cross-tenant ids collapse into pgx.ErrNoRows so the
// wire cannot be used to probe another org's ticket numbering.
func (h *Handler) sendResolveOrderFromTickets(
	ctx context.Context,
	ticketIDs []int64,
	orgID uuid.UUID,
) (gen.OrderRow, error) {
	for _, id := range ticketIDs {
		ticket, err := h.ticketsDeps.Q.GetTicketBySystemTicketID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return gen.OrderRow{}, err
		}
		order, err := h.ticketsDeps.Q.GetOrderByCheckoutSession(ctx, ticket.CheckoutSessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return gen.OrderRow{}, err
		}
		return payScopeOrder(order, orgID)
	}
	return gen.OrderRow{}, pgx.ErrNoRows
}

// sendEnqueueOrderTickets queues (or re-queues) one delivery job per live
// ticket of the order and reports how many were queued.
//
// RequeueDeliveryJob rather than InsertDeliveryJob: delivery_jobs.ticket_id is
// UNIQUE and InsertDeliveryJob's ON CONFLICT is a deliberate no-op so a replayed
// payment webhook cannot mail the same ticket twice. A resend is the opposite
// intent — the buyer ASKED — so the terminal 'sent'/'failed' state of the
// previous attempt has to be cleared or the worker would never look again.
func (h *Handler) sendEnqueueOrderTickets(
	ctx context.Context,
	order gen.OrderRow,
	email string,
) (int, error) {
	rows, err := h.ticketsDeps.Q.ListTicketsByCheckoutSession(ctx, order.CheckoutSessionID)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, row := range rows {
		if row.CancelledAt != nil {
			continue
		}
		if _, err := h.ticketsDeps.Q.RequeueDeliveryJob(ctx, row.ID, &email); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}
