// public_checkout_status.go — anonymous order-status endpoint (feature #319 WID-0b).
//
// GET /v1/public/checkout/{checkout_token}
//
// No JWT required. The checkout_token in the path is the credential.
// Rate-limited like the public feed (per-token + per-IP, shared publicFeedRL limiter).
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
package hfeed

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
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

// checkoutStatusToPublic maps internal checkout_session.state to the
// public-facing status enum (pending/paid/expired/failed).
func checkoutStatusToPublic(state string) string {
	switch state {
	case "completed":
		return "paid"
	case "expired":
		return "expired"
	case "abandoned":
		return "failed"
	default:
		// created, pricing_confirmed, payment_started, manual_review → pending
		return "pending"
	}
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
	Status            string                         `json:"status"`
	CheckoutToken     string                         `json:"checkout_token"`
	CheckoutSessionID string                         `json:"checkout_session_id"`
	ExpiresAt         *string                        `json:"expires_at,omitempty"`
	Subtotal          *int64                         `json:"subtotal,omitempty"`
	Discount          *int64                         `json:"discount,omitempty"`
	PlatformFee       *int64                         `json:"platform_fee,omitempty"`
	ProviderFee       *int64                         `json:"provider_fee,omitempty"`
	Tax               *int64                         `json:"tax,omitempty"`
	Total             *int64                         `json:"total,omitempty"`
	Currency          *string                        `json:"currency,omitempty"`
	Items             []checkoutStatusItemResponse   `json:"items"`
	Tickets           []checkoutStatusTicketResponse `json:"tickets"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/public/checkout/{checkout_token}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPublicCheckoutStatus serves GET /v1/public/checkout/{checkout_token}.
//
// No JWT required. The checkout_token path parameter is the credential.
//
// Flow:
//  1. Rate-limit by token + IP (shared publicFeedRL limiter).
//  2. Look up the checkout session by checkout_token.
//  3. Load reservation for expires_at.
//  4. For pending sessions: load reservation_seats (assigned) or use
//     reservation.Quantity (GA).
//  5. For paid sessions: load tickets + static_qr human_code.
//  6. Return 200 with the status envelope.
func (h *Handler) HandleGetPublicCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	checkoutToken := chi.URLParam(r, "checkout_token")
	clientIP := httputil.ExtractClientIP(r)

	// Rate-limit: use checkout_token as the "token" key (same pool as feed tokens).
	if !h.rl.CheckToken(checkoutToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"checkout.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	ctx := r.Context()

	// ── 2. Look up checkout session by token ─────────────────────────────────
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "checkout not found", r,
			))
			return
		}
		h.logger.Error("public_checkout_status: session lookup failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "failed to retrieve checkout session", r,
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
			"checkout.reservation_failed", "failed to retrieve reservation", r,
		))
		return
	}

	publicStatus := checkoutStatusToPublic(cs.State)
	expiresAtStr := reservation.ExpiresAt.UTC().Format(time.RFC3339)

	resp := checkoutStatusResponse{
		Status:            publicStatus,
		CheckoutToken:     checkoutToken,
		CheckoutSessionID: cs.ID.String(),
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

	// ── 4. Load held cart items (pending only) ────────────────────────────────
	// A mixed hold carries BOTH assigned seats (reservation_seats) and GA
	// lines (reservation_ga_items, migration 0063); the response includes
	// every item with its label + unit price (WID-0b contract).
	if publicStatus == "pending" {
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
				// Load static_qr credential for human_code.
				if h.credentialQueries != nil {
					cred, credErr := h.credentialQueries.GetCredentialByTicketID(ctx, t.ID, "static_qr")
					if credErr == nil && cred.HumanCode != nil {
						formatted := humancode.Format(*cred.HumanCode)
						tr.HumanCode = &formatted
					}
				}
				// PDF URL — a public endpoint that verifies ownership via checkout_token.
				pdfURL := "/v1/public/checkout/" + checkoutToken + "/tickets/" + t.ID.String() + "/pdf"
				tr.PDFURL = &pdfURL
				ticketResps = append(ticketResps, tr)
			}
			resp.Tickets = ticketResps
		}
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
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
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	checkoutToken := chi.URLParam(r, "checkout_token")
	ticketIDStr := chi.URLParam(r, "ticket_id")
	clientIP := httputil.ExtractClientIP(r)

	if !h.rl.CheckToken(checkoutToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"checkout.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_ticket_id", "ticket_id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// Look up the checkout session by token.
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "checkout not found", r,
			))
			return
		}
		h.logger.Error("public_ticket_pdf: session lookup failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "failed to retrieve checkout session", r,
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
			"checkout.tickets_failed", "failed to retrieve tickets", r,
		))
		return
	}

	found := false
	for _, t := range tickets {
		if t.ID == ticketID {
			found = true
			break
		}
	}
	if !found {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.ticket_not_found", "ticket not found in this checkout", r,
		))
		return
	}

	// Fetch PDF credential.
	cred, err := h.credentialQueries.GetCredentialByTicketID(ctx, ticketID, "pdf")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.pdf_not_found", "PDF not yet generated for this ticket", r,
			))
			return
		}
		h.logger.Error("public_ticket_pdf: credential lookup failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.credential_failed", "failed to retrieve ticket credential", r,
		))
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
			"checkout.credential_malformed", "ticket credential payload is malformed", r,
		))
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"ticket-"+ticketID.String()+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}
