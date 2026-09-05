// tickets.go — ticket issuance and read HTTP API (feature #139).
//
// Tickets are the atomic unit of entitlement issued after:
//   - payment.succeeded (via the payment-intent webhook handler)
//   - Free-checkout completion (via POST /v1/checkout/{id}/complete with total=0)
//
// # Idempotency and concurrency safety (feature #366, PR2-10)
//
// IssueTicketsForCheckout is idempotent and safe under concurrent callers:
//
//  1. pg_advisory_xact_lock(low-64-bits-of-checkout_session_id) serialises
//     concurrent triggers for the same checkout session within a transaction.
//     Two callers racing on the same session queue at the lock; the second
//     caller exits immediately when it sees all tickets already issued.
//
//  2. The idempotency check is quantity-aware: len(existing) is compared to
//     the expected ticket count (len(seats) for assigned-seat sessions,
//     reservation.Quantity for GA). A partial set (len < expected) triggers
//     gap-fill: only the missing ordinals are inserted, completing the set
//     without re-inserting already-issued tickets.
//
//  3. The UNIQUE (checkout_session_id, ordinal) constraint (migration 0066)
//     is a database-level safety belt. Even if the advisory lock is bypassed
//     (e.g. a code-path bug), the DB rejects true duplicate inserts.
//
// Endpoints:
//
//	GET /v1/checkout/{id}/tickets — list tickets for a checkout session (ticket.read)
package htickets

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ean13PlatformPrefix is the GS1 "internal use" prefix minted onto every
// platform-issued EAN-13 ticket credential (feature #502, W1-B6a; spec §11).
// GS1 reserves 20-29 for internal use; "21" keeps platform-minted codes from
// ever colliding with a real Bil24 barcode (which the spec documents as
// starting with "24…").
const ean13PlatformPrefix = "21"

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// TicketResponse is the JSON representation of a single tickets row.
//
// SeatKey / SeatSector / SeatRow / SeatNumber are populated for tickets
// issued from an assigned-seats reservation (SEAT-C3, feature #311) and
// omitted (nil pointers → null in JSON) for general-admission tickets.
type TicketResponse struct {
	ID                string  `json:"id"`
	CheckoutSessionID string  `json:"checkout_session_id"`
	SessionID         string  `json:"session_id"`
	TierID            *string `json:"tier_id"`
	HolderEmail       *string `json:"holder_email"`
	Status            string  `json:"status"`
	IssuedAt          string  `json:"issued_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	SeatKey           *string `json:"seat_key,omitempty"`
	SeatSector        *string `json:"seat_sector,omitempty"`
	SeatRow           *string `json:"seat_row,omitempty"`
	SeatNumber        *string `json:"seat_number,omitempty"`
	// AB-49 cancellation + refund record (omitted while active).
	CancelledAt        *string `json:"cancelled_at,omitempty"`
	CancellationReason *string `json:"cancellation_reason,omitempty"`
	RefundMode         *string `json:"refund_mode,omitempty"`
	RefundID           *string `json:"refund_id,omitempty"`
	RefundDate         *string `json:"refund_date,omitempty"`
	RefundPrice        *int64  `json:"refund_price,omitempty"`
	ReviewHold         bool    `json:"review_hold"`
	ReviewHoldReason   *string `json:"review_hold_reason,omitempty"`
}

// TicketFromRow converts a gen.TicketRow to a TicketResponse.
func TicketFromRow(t gen.TicketRow) TicketResponse {
	resp := TicketResponse{
		ID:                t.ID.String(),
		CheckoutSessionID: t.CheckoutSessionID.String(),
		SessionID:         t.SessionID.String(),
		HolderEmail:       t.HolderEmail,
		Status:            t.Status,
		IssuedAt:          t.IssuedAt.UTC().Format(time.RFC3339),
		CreatedAt:         t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339),
		SeatKey:           t.SeatKey,
		SeatSector:        t.SeatSector,
		SeatRow:           t.SeatRow,
		SeatNumber:        t.SeatNumber,
	}
	if t.TierID != nil {
		s := t.TierID.String()
		resp.TierID = &s
	}
	// AB-49 cancellation + refund record.
	resp.CancellationReason = t.CancellationReason
	resp.RefundMode = t.RefundMode
	resp.RefundPrice = t.RefundPrice
	resp.ReviewHold = t.ReviewHold
	resp.ReviewHoldReason = t.ReviewHoldReason
	if t.CancelledAt != nil {
		s := t.CancelledAt.UTC().Format(time.RFC3339)
		resp.CancelledAt = &s
	}
	if t.RefundID != nil {
		s := t.RefundID.String()
		resp.RefundID = &s
	}
	if t.RefundDate != nil {
		s := t.RefundDate.UTC().Format(time.RFC3339)
		resp.RefundDate = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// IssueTicketsForCheckout — internal issuance helper (idempotent, concurrent-safe)
// ─────────────────────────────────────────────────────────────────────────────

// issuanceLockKey returns the int64 advisory-lock key for a checkout session.
//
// UUIDv7 layout: bytes 0-5 = unix-ts-ms, bytes 6-7 = version+rand,
// bytes 8-15 = 64 random bits. We use the low-63-bits (right-shift by 1 to
// clear the sign bit) of the fully-random portion to minimise collisions while
// keeping the result within the non-negative int64 range accepted by
// pg_try_advisory_xact_lock. 63 bits still provides 9.2 × 10¹⁸ distinct keys.
func issuanceLockKey(id uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(id[8:]) >> 1)
}

// IssueTicketsForCheckout issues tickets for the given completed checkout session.
//
// The function is idempotent and concurrent-safe:
//
//  1. It acquires a transaction-scoped advisory lock keyed on the
//     checkout_session_id UUID. Concurrent calls for the same session
//     serialise at this point.
//
//  2. It compares len(existing) to the expected ticket count (quantity-aware):
//     - len == expected → return existing (fully issued, replay-safe)
//     - 0 < len < expected → gap-fill: insert only missing ordinals
//     - len == 0 → issue all tickets
//
//  3. The UNIQUE (checkout_session_id, ordinal) DB constraint is the final
//     safety net against any bug that bypasses the advisory lock.
//
// Returns an error if required dependencies are nil, or if any DB
// operation fails. Callers should log but not hard-fail when the checkout is
// already terminal (the customer got their goods; ticket retry is safe).
func (h *Handler) IssueTicketsForCheckout(ctx context.Context, cs gen.CheckoutSessionRow) ([]gen.TicketRow, error) {
	if h.ticketQueries == nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: ticketQueries not wired")
	}
	if h.reservationQueries == nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: reservationQueries not wired")
	}
	if h.pool == nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: pool not wired")
	}

	// ── Load reservation ─────────────────────────────────────────────────────
	// Load before acquiring the lock so we know the expected ticket count.
	reservation, err := h.reservationQueries.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: get reservation %s: %w",
			cs.ReservationID.String(), err)
	}

	// ── SEAT-C3 (feature #311): load seat coordinates ────────────────────────
	// For assigned-seat reservations, load the reservation_seats join to get
	// one row per held seat with denormalized seat_key / sector_name /
	// row_name / seat_number columns. GA reservations return an empty slice.
	seats, err := h.reservationQueries.ListReservationSeats(ctx, reservation.ID)
	if err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: list reservation seats: %w", err)
	}

	// Expected ticket count: one per seat (assigned) or one per quantity unit (GA).
	expectedCount := len(seats)
	if expectedCount == 0 {
		expectedCount = int(reservation.Quantity)
	}

	// ── Transaction + advisory lock ──────────────────────────────────────────
	// pg_advisory_xact_lock is blocking (unlike pg_try_advisory_xact_lock).
	// Concurrent callers for the same checkout session queue here; the second
	// caller sees all tickets already issued and returns immediately.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	lockKey := issuanceLockKey(cs.ID)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: acquire advisory lock for %s: %w",
			cs.ID.String(), err)
	}

	txQ := gen.New(tx)

	// ── Quantity-aware idempotency check ─────────────────────────────────────
	// Compare existing ticket count to the expected total.
	// A non-zero count < expected means a previous attempt was interrupted
	// mid-loop; we complete the missing ordinals instead of giving up.
	existing, err := txQ.ListTicketsByCheckoutSession(ctx, cs.ID)
	if err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: list existing tickets: %w", err)
	}

	if len(existing) >= expectedCount {
		// All tickets are already issued. Return them without another INSERT.
		h.logger.Info("tickets: idempotent replay — returning existing tickets",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.Int("existing_count", len(existing)),
			slog.Int("expected_count", expectedCount),
		)
		// No write happened — rollback is a cheap no-op.
		_ = tx.Rollback(ctx)
		return existing, nil
	}

	// ── Gap-fill: insert only missing ordinals ───────────────────────────────
	// Build a set of ordinals already present (for partial-issue recovery).
	issuedOrdinals := make(map[int32]struct{}, len(existing))
	for _, t := range existing {
		issuedOrdinals[t.Ordinal] = struct{}{}
	}

	var newTickets []gen.TicketRow

	if len(seats) > 0 {
		// Assigned seats: one ticket per session_seats row, with seat coordinates.
		// Per-seat tier_id overrides the reservation tier so mixed-tier seated
		// reservations (VIP + Standard) issue tickets with the correct tier.
		for i, s := range seats {
			ordinal := int32(i)
			if _, alreadyIssued := issuedOrdinals[ordinal]; alreadyIssued {
				continue // skip — already present from a prior attempt
			}
			seatKey := s.SeatKey
			seatSector := s.SectorName
			seatRow := s.RowName
			seatNumber := s.SeatNumber
			tierID := s.TierID
			if tierID == nil {
				tierID = reservation.TierID
			}
			t, err := txQ.InsertTicket(ctx,
				cs.ID,
				reservation.SessionID,
				tierID,
				nil, // holderEmail — not known at issuance time
				&seatKey,
				&seatSector,
				&seatRow,
				&seatNumber,
				ordinal,
			)
			if err != nil {
				return nil, fmt.Errorf("IssueTicketsForCheckout: insert seated ticket ordinal=%d (seat_key=%s): %w",
					ordinal, seatKey, err)
			}
			newTickets = append(newTickets, t)
		}
	} else {
		// LEGACY general-admission fallback: one ticket per unit of
		// reservation.Quantity, without seat identity. Since AB-51 every
		// GA hold links concrete ga_unit rows via reservation_seats, so
		// new reservations take the branch above and their tickets carry
		// a real seat_key; only pre-AB-51 reservations land here.
		for i := int32(0); i < reservation.Quantity; i++ {
			if _, alreadyIssued := issuedOrdinals[i]; alreadyIssued {
				continue // skip — already present from a prior attempt
			}
			t, err := txQ.InsertTicket(ctx,
				cs.ID,
				reservation.SessionID,
				reservation.TierID,
				nil, // holderEmail — not known at issuance time
				nil, // seatKey — GA ticket
				nil, // seatSector — GA ticket
				nil, // seatRow — GA ticket
				nil, // seatNumber — GA ticket
				i,
			)
			if err != nil {
				return nil, fmt.Errorf("IssueTicketsForCheckout: insert GA ticket ordinal=%d: %w",
					i, err)
			}
			newTickets = append(newTickets, t)
		}
	}

	// ── EAN-13 credential + barcode (feature #502, W1-B6a) ───────────────────
	// Every newly issued ticket gets a platform-minted EAN-13 barcode: a
	// ticket_credentials row (type='ean13', 13-digit payload) plus a
	// barcodes row (authority='platform') so SCAN_TICKET / /v1/scanner/*
	// can resolve it the same way as any other barcode authority (spec §11).
	if len(newTickets) > 0 {
		platformAuthority, err := txQ.GetBarcodeAuthorityByType(ctx, "platform")
		if err != nil {
			return nil, fmt.Errorf("IssueTicketsForCheckout: get platform barcode authority: %w", err)
		}
		for _, t := range newTickets {
			code := ean13.Encode(ean13PlatformPrefix, t.SystemTicketID)
			if _, err := txQ.InsertTicketCredential(ctx, t.ID, "ean13", code); err != nil {
				return nil, fmt.Errorf("IssueTicketsForCheckout: insert ean13 credential for ticket %s: %w",
					t.ID.String(), err)
			}
			ticketID := t.ID
			if _, err := txQ.InsertBarcode(ctx, platformAuthority.ID, code, &ticketID); err != nil {
				return nil, fmt.Errorf("IssueTicketsForCheckout: insert ean13 barcode for ticket %s: %w",
					t.ID.String(), err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("IssueTicketsForCheckout: commit tx for %s: %w",
			cs.ID.String(), err)
	}

	allTickets := append(existing, newTickets...)

	h.logger.Info("tickets: issued",
		slog.String("checkout_session_id", cs.ID.String()),
		slog.String("reservation_id", cs.ReservationID.String()),
		slog.String("session_id", reservation.SessionID.String()),
		slog.Int("quantity", int(reservation.Quantity)),
		slog.Int("seat_count", len(seats)),
		slog.Int("new_tickets_issued", len(newTickets)),
		slog.Int("existing_tickets", len(existing)),
		slog.Int("total_tickets", len(allTickets)),
	)

	// Publish Bil24-compatible scanner events for each newly issued ticket
	// (feature #143). Best-effort: errors are logged internally, not returned.
	// Only newly issued tickets are published to avoid duplicate events on replay.
	if len(newTickets) > 0 && h.publishTicketIssuedEvents != nil {
		h.publishTicketIssuedEvents(ctx, newTickets)
	}

	// Enqueue email delivery jobs for each newly issued ticket (feature #141).
	// Best-effort: errors are logged internally, not returned.
	// Only new tickets get delivery jobs; existing ones were already enqueued.
	if len(newTickets) > 0 {
		h.EnqueueDeliveryJobs(ctx, newTickets)
	}

	return allTickets, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/{id}/tickets
// ─────────────────────────────────────────────────────────────────────────────

// HandleListTickets serves GET /v1/checkout/{id}/tickets.
// Returns all tickets issued for the given checkout session.
// Requires JWT + "ticket.read" permission.
func (h *Handler) HandleListTickets(w http.ResponseWriter, r *http.Request) {
	if h.ticketQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"ticket.invalid_checkout_id", "checkout session id must be a valid UUID", r,
		))
		return
	}

	tickets, err := h.ticketQueries.ListTicketsByCheckoutSession(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusOK, map[string]any{"tickets": []any{}})
			return
		}
		h.logger.Error("tickets: list failed",
			slog.String("checkout_session_id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.list_failed", "failed to retrieve tickets", r,
		))
		return
	}

	respTickets := make([]TicketResponse, 0, len(tickets))
	for _, t := range tickets {
		respTickets = append(respTickets, TicketFromRow(t))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tickets": respTickets,
	})
}
