// public_checkout_status.go — anonymous order-status endpoint (feature #319 WID-0b).
//
// GET /v1/public/checkout/{checkout_token}
//
// No JWT required. The checkout_token in the path is the credential.
// Rate-limited by the shared publicFeedRL limiter: per-checkout-token
// (PUBLIC_CHECKOUT_TOKEN_RATE_LIMIT, one buyer's journey) + per-IP
// (PUBLIC_API_IP_RATE_LIMIT), both always evaluated.
//
// The checkout_token is an opaque 64-char hex string minted at checkout creation
// (either by the DB DEFAULT or by the caller via mintCheckoutToken in
// public_feed_checkout.go). It is NOT the checkout session UUID.
//
// Also provides:
//
//	GET /v1/public/checkout/{checkout_token}/tickets/{ticket_id}/pdf
//
// which returns the raw PDF bytes for a ticket that belongs to the given checkout.
//
// Both record the buyer's access in ticket_access_events (ticket_access.go).
package hfeed

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/humancode"
)

// ─────────────────────────────────────────────────────────────────────────────
// Status mapping
// ─────────────────────────────────────────────────────────────────────────────

// The public order statuses (spec 62 §5.5, item P-16). The first four are the
// original set; needs_help, refunded and cancelled are explicit so a thank-you
// page can tell "we are checking your order" from "your money came back" from
// "the order was cancelled".
const (
	publicStatusPending   = "pending"
	publicStatusPaid      = "paid"
	publicStatusExpired   = "expired"
	publicStatusFailed    = "failed"
	publicStatusNeedsHelp = "needs_help"
	publicStatusRefunded  = "refunded"
	publicStatusCancelled = "cancelled"
)

// checkoutStatusToPublic maps internal checkout_session.state to the
// public-facing status enum. manual_review used to be folded into pending,
// which kept a buyer on a spinner for an order an operator has to look at.
func checkoutStatusToPublic(state string) string {
	switch state {
	case "completed":
		return publicStatusPaid
	case "expired":
		return publicStatusExpired
	case "abandoned":
		return publicStatusFailed
	case "manual_review":
		return publicStatusNeedsHelp
	default:
		// created, pricing_confirmed, payment_started → pending
		return publicStatusPending
	}
}

// publicOrderStatus refines the checkout-session status with the order the
// session produced. The order is the truth for what happened after payment:
// a refunded or cancelled order has a completed checkout session, and an
// order parked for review is "needs help" whatever the session says. A
// partially refunded order stays paid (its remaining tickets are valid).
// An order that is paid while the session disagrees is reported paid (the
// safety net for a path that marks an order paid without completing it).
func publicOrderStatus(sessionStatus, orderStatus string) string {
	switch orderStatus {
	case "refunded":
		return publicStatusRefunded
	case "cancelled":
		return publicStatusCancelled
	case "manual_review":
		return publicStatusNeedsHelp
	case "paid", "partially_refunded":
		return publicStatusPaid
	}
	return sessionStatus
}

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// checkoutStatusItemResponse represents a single item held in the cart.
// For assigned-seat items the seat_key, sector, row, number fields are
// populated. For GA items tier_id / tier_name label the zone and quantity
// carries the ticket count. unit_price is the per-ticket price snapshot in
// smallest currency units (WID-0b requires labels + prices on every item).
type checkoutStatusItemResponse struct {
	Type      string  `json:"type"` // "seat" or "general_admission"
	SeatKey   *string `json:"seat_key,omitempty"`
	Sector    *string `json:"sector,omitempty"`
	Row       *string `json:"row,omitempty"`
	Number    *string `json:"number,omitempty"`
	TierID    *string `json:"tier_id,omitempty"`
	TierName  *string `json:"tier_name,omitempty"`
	UnitPrice *int64  `json:"unit_price,omitempty"`
	Quantity  *int32  `json:"quantity,omitempty"`
}

// checkoutStatusTicketResponse represents a single ticket returned when the
// order has been paid.
type checkoutStatusTicketResponse struct {
	TicketID  string  `json:"ticket_id"`
	Sector    *string `json:"sector,omitempty"`
	Row       *string `json:"row,omitempty"`
	Number    *string `json:"number,omitempty"`
	HumanCode *string `json:"human_code,omitempty"`
	PDFURL    *string `json:"pdf_url,omitempty"`
}

// checkoutStatusResponse is the full JSON envelope returned by the anonymous
// order-status endpoint.
type checkoutStatusResponse struct {
	Status            string `json:"status"`
	CheckoutToken     string `json:"checkout_token"`
	CheckoutSessionID string `json:"checkout_session_id"`
	// OrderNumber is orders.system_id: the number the ticket e-mail and the
	// PDF print, so the widget's success screen names the same order.
	OrderNumber *int64  `json:"order_number,omitempty"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
	// PaymentExpiresAt is when the provider's page stops taking the payment
	// (the channel's payment window, spec 62 П-3): expires_at minus the
	// grace arena waits before it lets the places go. Only while pending.
	PaymentExpiresAt *string `json:"payment_expires_at,omitempty"`
	Subtotal         *int64  `json:"subtotal,omitempty"`
	Discount         *int64  `json:"discount,omitempty"`
	PlatformFee      *int64  `json:"platform_fee,omitempty"`
	ProviderFee      *int64  `json:"provider_fee,omitempty"`
	Tax              *int64  `json:"tax,omitempty"`
	Total            *int64  `json:"total,omitempty"`
	Currency         *string `json:"currency,omitempty"`
	// PaymentURL is the provider-hosted payment page for a still-pending
	// order whose window has not closed. A buyer who bounced off the Stripe
	// page — closed the tab, lost signal, hit "back" — used to have no way
	// back to it: their seats stayed held and they had to build the cart
	// again. With this the widget can offer "continue to payment" for the
	// SAME hosted session.
	//
	// Omitted once the order is paid, expired or failed, and once the hold's
	// expires_at has passed (the hosted session itself is already dead by
	// then — its own expiry is set to fire before arena releases the seats).
	PaymentURL *string `json:"payment_url,omitempty"`
	// Event is what the order is for (name, session start, venue and its
	// time zone) so a static page can render the order without a second
	// call. Omitted when the session can no longer be resolved.
	Event *checkoutStatusEvent `json:"event,omitempty"`
	// LatePayment is set when a payment arrived after the order stopped
	// waiting for it (spec 62 П-15): refund_requested (the money goes back),
	// operator_review (the organizer returns it) or reacquired (the places
	// were taken again and the order is paid).
	LatePayment *string                        `json:"late_payment,omitempty"`
	Items       []checkoutStatusItemResponse   `json:"items"`
	Tickets     []checkoutStatusTicketResponse `json:"tickets"`
}

// checkoutStatusEvent is the event block of the order status.
type checkoutStatusEvent struct {
	EventID   string  `json:"event_id"`
	SessionID string  `json:"session_id"`
	Name      string  `json:"name"`
	Slug      *string `json:"slug"`
	// StartAt is the session start, RFC 3339 UTC; Timezone is the venue's
	// IANA zone ("" when unknown) to render it in.
	StartAt   string  `json:"start_at"`
	VenueName *string `json:"venue_name"`
	Timezone  *string `json:"timezone"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/public/checkout/{checkout_token}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPublicCheckoutStatus serves GET /v1/public/checkout/{checkout_token}.
//
// No JWT required. The checkout_token path parameter is the credential.
//
// Flow:
//  1. Rate-limit by checkout token + IP (shared publicFeedRL limiter).
//  2. Look up the checkout session by checkout_token.
//  3. Load reservation for expires_at.
//  4. For pending sessions: load reservation_seats (assigned) or use
//     reservation.Quantity (GA).
//  5. For paid sessions: load tickets + static_qr human_code.
//  6. Return 200 with the status envelope.
func (h *Handler) HandleGetPublicCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}

	checkoutToken := chi.URLParam(r, "checkout_token")

	// Rate-limit: per-checkout-token (one buyer's journey) + per-IP, both
	// always evaluated.
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}

	ctx := r.Context()

	// ── 2. Look up checkout session by token ─────────────────────────────────
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_status: session lookup failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "We could not load this checkout. Try again in a minute.", r,
		))
		return
	}

	// ── 3. Load reservation for expires_at ───────────────────────────────────
	reservation, err := h.reservationQueries.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		h.logger.Error("public_checkout_status: reservation lookup failed",
			slog.String("reservation_id", cs.ReservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.reservation_failed", "We could not load your hold. Try again in a minute.", r,
		))
		return
	}

	publicStatus := checkoutStatusToPublic(cs.State)

	// Defense in depth (HIGH-severity fix, 2026-09-13): the payment webhook
	// now completes the checkout session in the same transaction as the
	// order mark-paid step (see hcheckout.HandlePaymentIntentWebhook Step
	// 2b), so this branch should no longer trigger in normal operation. It
	// stays as a safety net for any other path that marks an order paid
	// without also completing its checkout session (e.g. a future bug, or a
	// session that predates that fix) — checkoutQueries carries every
	// gen.Queries method, GetOrderByCheckoutSession included, so no extra
	// wiring is needed to check the order directly. The same row gives the
	// order number the buyer's e-mail and PDF print (order_number).
	var orderNumber *int64
	if h.checkoutQueries != nil {
		if ord, ordErr := h.checkoutQueries.GetOrderByCheckoutSession(ctx, cs.ID); ordErr == nil {
			num := ord.SystemID
			orderNumber = &num
			if publicStatus != publicStatusPaid && ord.Status == "paid" {
				h.logger.Warn("public_checkout_status: order is paid but checkout session disagrees; reporting paid",
					slog.String("checkout_session_id", cs.ID.String()),
					slog.String("checkout_session_state", cs.State),
					slog.String("order_id", ord.ID.String()),
				)
			}
			publicStatus = publicOrderStatus(publicStatus, ord.Status)
		}
	}

	expiresAtStr := reservation.ExpiresAt.UTC().Format(time.RFC3339)

	resp := checkoutStatusResponse{
		Status:            publicStatus,
		CheckoutToken:     checkoutToken,
		CheckoutSessionID: cs.ID.String(),
		OrderNumber:       orderNumber,
		ExpiresAt:         &expiresAtStr,
		Subtotal:          cs.Subtotal,
		Discount:          cs.Discount,
		PlatformFee:       cs.PlatformFee,
		ProviderFee:       cs.ProviderFee,
		Tax:               cs.Tax,
		Total:             cs.Total,
		Currency:          cs.Currency,
		Items:             []checkoutStatusItemResponse{},
		Tickets:           []checkoutStatusTicketResponse{},
	}

	resp.Event = h.checkoutEventBlock(ctx, reservation.SessionID)

	if publicStatus == publicStatusPending && h.paymentWindow > 0 && cs.Total != nil && *cs.Total > 0 {
		pay := reservation.ExpiresAt.Add(-h.paymentGrace).UTC().Format(time.RFC3339)
		resp.PaymentExpiresAt = &pay
	}

	if publicStatus != publicStatusPaid && publicStatus != publicStatusPending {
		if outcome := h.latePaymentOutcome(ctx, cs.ID); outcome != "" {
			resp.LatePayment = &outcome
		}
	}

	// ── 3b. Offer the hosted payment page back while it can still be paid ────
	if publicStatus == publicStatusPending && reservation.ExpiresAt.After(time.Now().UTC()) {
		if url := h.pendingPaymentURL(ctx, cs.ID); url != "" {
			resp.PaymentURL = &url
		}
	}

	// ── 4. Load held cart items (pending only) ────────────────────────────────
	// A mixed hold carries BOTH assigned seats (reservation_seats) and GA
	// lines (reservation_ga_items, migration 0063); the response includes
	// every item with its label + unit price (WID-0b contract).
	if publicStatus == publicStatusPending {
		seats, seatErr := h.reservationQueries.ListReservationSeats(ctx, reservation.ID)
		if seatErr != nil {
			// Non-fatal: log + return empty items (UI can still display totals).
			h.logger.Warn("public_checkout_status: list reservation seats failed",
				slog.String("reservation_id", reservation.ID.String()),
				slog.String("error", seatErr.Error()),
			)
		}

		gaItems, gaErr := h.reservationQueries.ListReservationGAItems(ctx, reservation.ID)
		if gaErr != nil {
			// Non-fatal for the same reason.
			h.logger.Warn("public_checkout_status: list reservation GA lines failed",
				slog.String("reservation_id", reservation.ID.String()),
				slog.String("error", gaErr.Error()),
			)
		}

		items := make([]checkoutStatusItemResponse, 0, len(seats)+len(gaItems)+1)

		// Seat items — labelled by seat_key/sector/row/number and priced from
		// the seat's tier binding (best-effort; seats without a tier or with an
		// unresolvable tier omit unit_price rather than failing the status).
		seatTierPrice := make(map[string]*int64)
		for _, s := range seats {
			item := checkoutStatusItemResponse{
				Type:    "seat",
				SeatKey: &s.SeatKey,
			}
			if s.SectorName != "" {
				sn := s.SectorName
				item.Sector = &sn
			}
			if s.RowName != "" {
				rn := s.RowName
				item.Row = &rn
			}
			if s.SeatNumber != "" {
				num := s.SeatNumber
				item.Number = &num
			}
			if s.TierID != nil {
				key := s.TierID.String()
				item.TierID = &key
				price, cached := seatTierPrice[key]
				if !cached && h.tierQueries != nil {
					if tier, terr := h.tierQueries.GetTicketTierByID(ctx, *s.TierID, reservation.SessionID); terr == nil {
						if unit, errCode := resolvePublicTierUnitPrice(tier); errCode == "" {
							price = &unit
						}
					}
					seatTierPrice[key] = price
				}
				item.UnitPrice = price
			}
			items = append(items, item)
		}

		// GA items — one line per tier with name, quantity, and the unit-price
		// snapshot taken when the hold was priced.
		for i := range gaItems {
			g := gaItems[i]
			tierID := g.TierID.String()
			tierName := g.TierName
			qty := g.Quantity
			unit := g.UnitPrice
			items = append(items, checkoutStatusItemResponse{
				Type:      "general_admission",
				TierID:    &tierID,
				TierName:  &tierName,
				Quantity:  &qty,
				UnitPrice: &unit,
			})
		}

		// Legacy fallback: pre-0063 GA reservations have neither seats nor GA
		// lines — expose the aggregate quantity (label/price best-effort from
		// the reservation's tier when one is recorded).
		if len(items) == 0 {
			qty := reservation.Quantity
			item := checkoutStatusItemResponse{
				Type:     "general_admission",
				Quantity: &qty,
			}
			if reservation.TierID != nil && h.tierQueries != nil {
				if tier, terr := h.tierQueries.GetTicketTierByID(ctx, *reservation.TierID, reservation.SessionID); terr == nil {
					tid := tier.ID.String()
					name := tier.Name
					item.TierID = &tid
					item.TierName = &name
					if unit, errCode := resolvePublicTierUnitPrice(tier); errCode == "" {
						item.UnitPrice = &unit
					}
				}
			}
			items = append(items, item)
		}

		resp.Items = items
	}

	// ── 5. Load tickets when paid ─────────────────────────────────────────────
	if publicStatus == "paid" && h.ticketQueries != nil {
		tickets, tErr := h.ticketQueries.ListTicketsByCheckoutSession(ctx, cs.ID)
		if tErr != nil {
			h.logger.Warn("public_checkout_status: list tickets failed",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("error", tErr.Error()),
			)
		} else {
			ticketResps := make([]checkoutStatusTicketResponse, 0, len(tickets))
			for _, t := range tickets {
				tr := checkoutStatusTicketResponse{
					TicketID: t.ID.String(),
					Sector:   t.SeatSector,
					Row:      t.SeatRow,
					Number:   t.SeatNumber,
				}
				// A ticket that no longer admits (cancelled, refunded, replaced)
				// keeps its line but loses its code and its PDF link (spec 63, R-1).
				if t.Status != "active" {
					ticketResps = append(ticketResps, tr)
					continue
				}
				// Load static_qr credential for human_code.
				if h.credentialQueries != nil {
					cred, credErr := h.credentialQueries.GetCredentialByTicketID(ctx, t.ID, "static_qr")
					if credErr == nil && cred.HumanCode != nil && cred.RevokedAt == nil {
						formatted := humancode.Format(*cred.HumanCode)
						tr.HumanCode = &formatted
					}
				}
				// PDF URL — a public endpoint that verifies ownership via checkout_token.
				pdfURL := h.absoluteAPIURL("/v1/public/checkout/" + checkoutToken + "/tickets/" + t.ID.String() + "/pdf")
				tr.PDFURL = &pdfURL
				ticketResps = append(ticketResps, tr)
			}
			resp.Tickets = ticketResps
			if len(ticketResps) > 0 {
				h.recordTicketsView(r, cs.ID, cs.OrgID)
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// latePaymentOutcome is the outcome of the newest late payment of the
// checkout (migration 0205), "" when there is none or it cannot be read.
func (h *Handler) latePaymentOutcome(ctx context.Context, checkoutSessionID uuid.UUID) string {
	if h.checkoutQueries == nil {
		return ""
	}
	outcome, err := h.checkoutQueries.LatestLatePaymentOutcome(ctx, checkoutSessionID)
	if err != nil {
		return ""
	}
	return outcome
}

// pendingPaymentURL returns the hosted payment page of the newest payment
// intent on this checkout that is still open, or "" when there is none.
//
// "Still open" means the intent has not reached a terminal state: a
// succeeded one needs no payment, and a failed one (card declined, or the
// hosted session hit its own expires_at and Stripe sent
// checkout.session.expired) points at a page that will refuse the buyer.
// Offering either would be worse than offering nothing.
//
// Best-effort throughout: the order status must render even when the payment
// lookup fails.
func (h *Handler) pendingPaymentURL(ctx context.Context, checkoutSessionID uuid.UUID) string {
	if h.checkoutQueries == nil {
		return ""
	}
	intents, err := h.checkoutQueries.ListPaymentIntentsByCheckout(ctx, checkoutSessionID)
	if err != nil {
		h.logger.Warn("public_checkout_status: payment intent lookup failed",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
		return ""
	}
	// ListPaymentIntentsByCheckout returns newest first; a retry after a
	// failure adds a row rather than replacing one.
	for _, pi := range intents {
		if pi.State == "succeeded" || pi.State == "failed" {
			continue
		}
		if pi.HostedCheckoutURL != nil && *pi.HostedCheckoutURL != "" {
			return *pi.HostedCheckoutURL
		}
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/public/checkout/{checkout_token}/tickets/{ticket_id}/pdf
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPublicTicketPDF serves
// GET /v1/public/checkout/{checkout_token}/tickets/{ticket_id}/pdf.
//
// No JWT required. Ownership is verified by confirming that the ticket's
// checkout_session_id matches the checkout session identified by checkout_token.
// Rate-limited like the public feed.
func (h *Handler) HandleGetPublicTicketPDF(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.ticketQueries == nil || h.credentialQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}

	checkoutToken := chi.URLParam(r, "checkout_token")
	ticketIDStr := chi.URLParam(r, "ticket_id")

	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}

	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_ticket_id", "Send ticket_id as a UUID.", r,
		))
		return
	}

	ctx := r.Context()

	// Look up the checkout session by token.
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_ticket_pdf: session lookup failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "We could not load this checkout. Try again in a minute.", r,
		))
		return
	}

	// Verify the ticket belongs to this checkout session (ownership check).
	tickets, err := h.ticketQueries.ListTicketsByCheckoutSession(ctx, cs.ID)
	if err != nil {
		h.logger.Error("public_ticket_pdf: list tickets failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.tickets_failed", "We could not load the tickets. Try again in a minute.", r,
		))
		return
	}

	found, active := false, false
	for _, t := range tickets {
		if t.ID == ticketID {
			found, active = true, t.Status == "active"
			break
		}
	}
	if !found {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.ticket_not_found", "This checkout has no such ticket.", r,
		))
		return
	}

	if !active {
		writeTicketNotValid(w, r)
		return
	}

	// Fetch PDF credential.
	cred, err := h.credentialQueries.GetCredentialByTicketID(ctx, ticketID, "pdf")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.pdf_not_found", "The ticket PDF is not ready yet. Try again in a minute.", r,
			))
			return
		}
		h.logger.Error("public_ticket_pdf: credential lookup failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.credential_failed", "We could not load the ticket code. Try again in a minute.", r,
		))
		return
	}

	if cred.RevokedAt != nil {
		writeTicketNotValid(w, r)
		return
	}

	// Decode base64 PDF payload.
	pdfBytes, err := base64.StdEncoding.DecodeString(cred.Payload)
	if err != nil {
		h.logger.Error("public_ticket_pdf: base64 decode failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.credential_malformed", "The stored ticket code is damaged. Write to support.", r,
		))
		return
	}

	h.recordPDFDownload(r, cs.ID, cs.OrgID, ticketID)

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"ticket-"+ticketID.String()+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// checkoutEventBlock resolves the event block of the order status. Best
// effort: a failed lookup omits the block, the status itself still renders.
func (h *Handler) checkoutEventBlock(ctx context.Context, sessionID uuid.UUID) *checkoutStatusEvent {
	if h.checkoutQueries == nil {
		return nil
	}
	row, err := h.checkoutQueries.GetPublicOrderEvent(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_checkout_status: event block lookup failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
		}
		return nil
	}
	ev := &checkoutStatusEvent{
		EventID:   row.EventID.String(),
		SessionID: row.SessionID.String(),
		Name:      row.Name,
		Slug:      row.Slug,
		StartAt:   row.StartAt.UTC().Format(time.RFC3339),
		VenueName: row.VenueName,
	}
	if row.Timezone != nil && strings.TrimSpace(*row.Timezone) != "" {
		tz := strings.TrimSpace(*row.Timezone)
		ev.Timezone = &tz
	}
	return ev
}

// WithAPIPublicBase wires the API's public origin (Config.APIPublicBaseURL,
// read per call) so links in public answers are absolute. A static site on
// another origin cannot resolve a host-relative path against the API.
func (h *Handler) WithAPIPublicBase(base func() string) *Handler {
	h.apiPublicBase = base
	return h
}

// absoluteAPIURL prefixes a host-relative API path with the public origin;
// without a configured origin the path stays relative (unit tests, dev).
func (h *Handler) absoluteAPIURL(path string) string {
	if h == nil || h.apiPublicBase == nil {
		return path
	}
	base := strings.TrimRight(strings.TrimSpace(h.apiPublicBase()), "/")
	if base == "" || !strings.HasPrefix(path, "/") {
		return path
	}
	return base + path
}

// writeTicketNotValid answers a PDF request for a ticket that no longer admits:
// cancelled, refunded or replaced, or whose stored PDF was revoked (spec 63,
// R-1). The PDF is never handed out, because its barcode no longer opens the
// door and a replaced ticket's PDF may sit in a stranger's mailbox.
func writeTicketNotValid(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusGone, httputil.ErrorEnvelope(
		"checkout.ticket_not_valid", "This ticket is no longer valid. Use the tickets from your latest letter.", r,
	))
}
