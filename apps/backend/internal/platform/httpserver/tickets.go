// tickets.go — ticket issuance and read HTTP API (feature #139).
//
// Tickets are the atomic unit of entitlement issued after:
//   - payment.succeeded (via the payment-intent webhook handler)
//   - Free-checkout completion (via POST /v1/checkout/{id}/complete with total=0)
//
// Issuance is idempotent per checkout_session_id: if tickets already exist for
// a checkout session, re-issuance returns the existing rows without inserting
// new ones.  This prevents double-issuance on webhook replay or handler retry.
//
// Endpoints:
//
//	GET /v1/checkout/{id}/tickets — list tickets for a checkout session (ticket.read)
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// ticketResponse is the JSON representation of a single tickets row.
type ticketResponse struct {
	ID                string  `json:"id"`
	CheckoutSessionID string  `json:"checkout_session_id"`
	SessionID         string  `json:"session_id"`
	TierID            *string `json:"tier_id"`
	HolderEmail       *string `json:"holder_email"`
	Status            string  `json:"status"`
	IssuedAt          string  `json:"issued_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// ticketFromRow converts a gen.TicketRow to a ticketResponse.
func ticketFromRow(t gen.TicketRow) ticketResponse {
	resp := ticketResponse{
		ID:                t.ID.String(),
		CheckoutSessionID: t.CheckoutSessionID.String(),
		SessionID:         t.SessionID.String(),
		HolderEmail:       t.HolderEmail,
		Status:            t.Status,
		IssuedAt:          t.IssuedAt.UTC().Format(time.RFC3339),
		CreatedAt:         t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if t.TierID != nil {
		s := t.TierID.String()
		resp.TierID = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// issueTicketsForCheckout — internal issuance helper (idempotent)
// ─────────────────────────────────────────────────────────────────────────────

// issueTicketsForCheckout issues tickets for the given completed checkout session.
//
// The function is idempotent per checkout_session_id:
//   - If tickets already exist for this session (detected via
//     ListTicketsByCheckoutSession), the existing rows are returned unchanged.
//   - If no tickets exist, the function loads the associated reservation to
//     determine quantity, session_id, and tier_id, then inserts one TicketRow
//     per unit of quantity.
//
// Returns an error if ticketQueries or reservationQueries are nil, or if any
// DB operation fails. Callers should log but not hard-fail when the checkout is
// already terminal (the customer got their goods; ticket retry is safe).
func (s *Server) issueTicketsForCheckout(ctx context.Context, cs gen.CheckoutSessionRow) ([]gen.TicketRow, error) {
	if s.ticketQueries == nil {
		return nil, fmt.Errorf("issueTicketsForCheckout: ticketQueries not wired")
	}
	if s.reservationQueries == nil {
		return nil, fmt.Errorf("issueTicketsForCheckout: reservationQueries not wired")
	}

	// ── Idempotency check ────────────────────────────────────────────────────
	// If tickets were already issued for this checkout session, return them.
	existing, err := s.ticketQueries.ListTicketsByCheckoutSession(ctx, cs.ID)
	if err != nil {
		return nil, fmt.Errorf("issueTicketsForCheckout: list existing tickets: %w", err)
	}
	if len(existing) > 0 {
		s.logger.Info("tickets: idempotent replay — returning existing tickets",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.Int("existing_count", len(existing)),
		)
		return existing, nil
	}

	// ── Load reservation ─────────────────────────────────────────────────────
	// The reservation holds the quantity, session_id, and optional tier_id.
	reservation, err := s.reservationQueries.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		return nil, fmt.Errorf("issueTicketsForCheckout: get reservation %s: %w",
			cs.ReservationID.String(), err)
	}

	// ── Issue one ticket per unit ────────────────────────────────────────────
	tickets := make([]gen.TicketRow, 0, reservation.Quantity)
	for i := int32(0); i < reservation.Quantity; i++ {
		t, err := s.ticketQueries.InsertTicket(ctx,
			cs.ID,
			reservation.SessionID,
			reservation.TierID,
			nil, // holderEmail — not yet known at issuance time
		)
		if err != nil {
			return nil, fmt.Errorf("issueTicketsForCheckout: insert ticket %d of %d: %w",
				i+1, reservation.Quantity, err)
		}
		tickets = append(tickets, t)
	}

	s.logger.Info("tickets: issued",
		slog.String("checkout_session_id", cs.ID.String()),
		slog.String("reservation_id", cs.ReservationID.String()),
		slog.String("session_id", reservation.SessionID.String()),
		slog.Int("quantity", int(reservation.Quantity)),
		slog.Int("tickets_issued", len(tickets)),
	)

	// Publish Bil24-compatible scanner events for each newly issued ticket
	// (feature #143). Best-effort: errors are logged internally, not returned.
	s.publishTicketIssuedEvents(ctx, tickets)

	// Enqueue email delivery jobs for each issued ticket (feature #141).
	// Best-effort: errors are logged internally, not returned.
	s.enqueueDeliveryJobs(ctx, tickets)

	return tickets, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/{id}/tickets
// ─────────────────────────────────────────────────────────────────────────────

// handleListTickets serves GET /v1/checkout/{id}/tickets.
// Returns all tickets issued for the given checkout session.
// Requires JWT + "ticket.read" permission.
func (s *Server) handleListTickets(w http.ResponseWriter, r *http.Request) {
	if s.ticketQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"ticket.invalid_checkout_id", "checkout session id must be a valid UUID", r,
		))
		return
	}

	tickets, err := s.ticketQueries.ListTicketsByCheckoutSession(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"tickets": []any{}})
			return
		}
		s.logger.Error("tickets: list failed",
			slog.String("checkout_session_id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"ticket.list_failed", "failed to retrieve tickets", r,
		))
		return
	}

	respTickets := make([]ticketResponse, 0, len(tickets))
	for _, t := range tickets {
		respTickets = append(respTickets, ticketFromRow(t))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tickets": respTickets,
	})
}
