// reservations.go implements the reservation state machine API endpoints (feature #131).
//
// A reservation holds capacity for a buyer within a session (and optionally a
// specific ticket tier). The state machine is:
//
//	draft → active → converted   (purchase confirmed; managed by checkout flow)
//	              → expired     (TTL exceeded; managed by ReservationProcessor worker)
//	              → cancelled   (buyer or org cancelled)
//	        ↓
//	      cancelled  (draft can also be cancelled before activation)
//
// TTL: expires_at is computed at creation time via resolveReservationTTL, which
// honours the precedence sales_channels.reservation_ttl_override →
// organizations.reservation_ttl_seconds → defaultReservationTTL (1200s).
//
// Inventory integration: POST /v1/reservations calls ReserveCapacity on the
// inventory ledger atomically in the same transaction. DELETE /v1/reservations/{id}
// calls ReleaseCapacity to return the held units.
//
// Endpoints:
//
//	POST   /v1/reservations              — create draft (reservation.create)
//	GET    /v1/reservations/{id}         — get by ID (reservation.read)
//	PATCH  /v1/reservations/{id}/activate — draft → active (reservation.activate)
//	DELETE /v1/reservations/{id}         — cancel (reservation.cancel)
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	inventorydomain "github.com/abhteam/arena_new/apps/backend/internal/domain/inventory"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Reservation state machine
// ─────────────────────────────────────────────────────────────────────────────

// defaultReservationTTL is the system-wide fallback TTL used when neither the
// sales channel nor the parent organization has a configured TTL.
// 1200 seconds = 20 minutes (matches the DEFAULT in
// organizations.reservation_ttl_seconds — migration 0009_organizations.sql).
const defaultReservationTTL = 1200 * time.Second

// DefaultReservationTTL is the exported alias of defaultReservationTTL for use
// by httpserver.checkout_shims.go (public_feed_checkout.go needs this constant).
const DefaultReservationTTL = defaultReservationTTL

// channelTTLLookup is the narrow surface of *gen.Queries that
// resolveReservationTTL needs to fetch a sales channel row. Declaring it as
// an interface allows unit tests to substitute fakes without spinning up a
// real PostgreSQL connection.
type channelTTLLookup interface {
	GetSalesChannelByID(ctx context.Context, id, orgID uuid.UUID) (gen.SalesChannelRow, error)
}

// orgTTLLookup is the narrow surface of *gen.Queries that
// resolveReservationTTL needs to fetch an organization row.
type orgTTLLookup interface {
	GetOrganizationByID(ctx context.Context, id uuid.UUID) (gen.OrganizationRow, error)
}

// ChannelTTLLookup and OrgTTLLookup are exported aliases of the unexported
// lookup interfaces, for use by the httpserver shim layer (the test
// reservation_ttl_177_test.go in package httpserver defines its own fakes
// satisfying these interfaces and calls resolveReservationTTL via a forwarder
// in checkout_shims.go).
type ChannelTTLLookup = channelTTLLookup
type OrgTTLLookup = orgTTLLookup

// resolveReservationTTL resolves the seat-hold expiry window for a reservation
// using the documented precedence:
//
//  1. sales_channels.reservation_ttl_override (per-channel override) — when set
//     and positive, it wins;
//  2. organizations.reservation_ttl_seconds (per-org default) — when positive;
//  3. defaultReservationTTL (1200 s system-wide fallback).
//
// Any lookup error (including pgx.ErrNoRows or a nil lookup) falls through to
// the next tier; the function never propagates an error because TTL resolution
// must not block reservation creation. The function is package-private so the
// reservation handler is its only caller; tests verify all three branches.
func resolveReservationTTL(
	ctx context.Context,
	channelQ channelTTLLookup,
	orgQ orgTTLLookup,
	channelID, orgID uuid.UUID,
) time.Duration {
	if channelQ != nil {
		if ch, err := channelQ.GetSalesChannelByID(ctx, channelID, orgID); err == nil {
			if ch.ReservationTTLOverride != nil && *ch.ReservationTTLOverride > 0 {
				return time.Duration(*ch.ReservationTTLOverride) * time.Second
			}
		}
	}
	if orgQ != nil {
		if org, err := orgQ.GetOrganizationByID(ctx, orgID); err == nil {
			if org.ReservationTTLSeconds > 0 {
				return time.Duration(org.ReservationTTLSeconds) * time.Second
			}
		}
	}
	return defaultReservationTTL
}

// ResolveReservationTTL is the exported alias of resolveReservationTTL for use
// by the httpserver shim layer (reservation_ttl_177_test.go in package
// httpserver calls resolveReservationTTL via a forwarder in checkout_shims.go).
func ResolveReservationTTL(
	ctx context.Context,
	channelQ ChannelTTLLookup,
	orgQ OrgTTLLookup,
	channelID, orgID uuid.UUID,
) time.Duration {
	return resolveReservationTTL(ctx, channelQ, orgQ, channelID, orgID)
}

// validReservationTransitions mirrors the pure-domain transition table from
// internal/domain/inventory.ValidReservationTransitions (feature #184),
// projected back to a string-keyed map so the in-package state-machine
// tests (reservation_131_test.go) can inspect terminal-state emptiness
// without importing the domain package. The slice of valid states
// ("draft", "active", "converted", "expired", "cancelled") is preserved
// verbatim from the original literal so the existence-grep test in
// TestReservation131_Step3_StateMachineInCode keeps passing.
var validReservationTransitions = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(inventorydomain.ValidReservationTransitions))
	for from, allowed := range inventorydomain.ValidReservationTransitions {
		row := make(map[string]bool, len(allowed))
		for to := range allowed {
			row[string(to)] = true
		}
		out[string(from)] = row
	}
	return out
}()

// isValidReservationTransition is a 1-line forwarder to the pure-domain
// state machine in internal/domain/inventory (feature #184). The transition
// table itself (draft → active|cancelled, active → converted|expired|
// cancelled, with the three terminal states) now lives next to the
// Reservation aggregate; this wrapper exists so the HTTP handlers and any
// existing in-package callers compile unchanged.
func isValidReservationTransition(from, to string) bool {
	return inventorydomain.IsValidReservationTransition(from, to)
}

// IsValidReservationTransition is the exported alias of isValidReservationTransition
// for use by the httpserver shim layer (checkout_shims.go) so that
// reservation_131_test.go (package httpserver) continues to compile without
// importing this package directly.
func IsValidReservationTransition(from, to string) bool {
	return isValidReservationTransition(from, to)
}

// ValidReservationTransitions is the exported alias of validReservationTransitions
// for use by the httpserver shim layer (checkout_shims.go).
var ValidReservationTransitions = validReservationTransitions

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// reservationResponse is the JSON representation of a single reservation.
type reservationResponse struct {
	ID          string  `json:"id"`
	OrgID       string  `json:"org_id"`
	ChannelID   string  `json:"channel_id"`
	SessionID   string  `json:"session_id"`
	TierID      *string `json:"tier_id"`
	UserID      *string `json:"user_id"`
	Quantity    int32   `json:"quantity"`
	State       string  `json:"state"`
	ExpiresAt   string  `json:"expires_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	CancelledAt *string `json:"cancelled_at"`
	ConvertedAt *string `json:"converted_at"`
	ExpiredAt   *string `json:"expired_at"`
}

// reservationFromRow converts a ReservationRow to a reservationResponse.
func reservationFromRow(r gen.ReservationRow) reservationResponse {
	resp := reservationResponse{
		ID:        r.ID.String(),
		OrgID:     r.OrgID.String(),
		ChannelID: r.ChannelID.String(),
		SessionID: r.SessionID.String(),
		Quantity:  r.Quantity,
		State:     r.State,
		ExpiresAt: r.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.TierID != nil {
		s := r.TierID.String()
		resp.TierID = &s
	}
	if r.UserID != nil {
		s := r.UserID.String()
		resp.UserID = &s
	}
	if r.CancelledAt != nil {
		s := r.CancelledAt.UTC().Format(time.RFC3339)
		resp.CancelledAt = &s
	}
	if r.ConvertedAt != nil {
		s := r.ConvertedAt.UTC().Format(time.RFC3339)
		resp.ConvertedAt = &s
	}
	if r.ExpiredAt != nil {
		s := r.ExpiredAt.UTC().Format(time.RFC3339)
		resp.ExpiredAt = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/reservations
// ─────────────────────────────────────────────────────────────────────────────

// createReservationRequest is the request body for POST /v1/reservations.
type createReservationRequest struct {
	SessionID string `json:"session_id"`
	ChannelID string `json:"channel_id"`
	OrgID     string `json:"org_id"`
	TierID    string `json:"tier_id"`  // optional; empty = session-level GA
	Quantity  int32  `json:"quantity"` // must be >= 1
}

// HandleCreateReservation serves POST /v1/reservations.
// Requires JWT + "reservation.create" permission.
//
// Flow (atomic):
//  1. Parse + validate request body.
//  2. Compute expires_at via resolveReservationTTL — channel override → org
//     default → 1200 s fallback.
//  3. Begin transaction.
//  4. Call ReserveCapacity — returns pgx.ErrNoRows on over-capacity (→ 409).
//  5. Call InsertReservation — records the draft reservation.
//  6. Commit.
//  7. Return 201 with the created reservation.
func (h *Handler) HandleCreateReservation(w http.ResponseWriter, r *http.Request) {
	if h.reservationQueries == nil || h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.missing", "authentication required", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.empty_body", "request body is required", r))
		return
	}

	var req createReservationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate required fields.
	if req.SessionID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.missing_session_id", "session_id is required", r,
			map[string]any{"field": "session_id"},
		))
		return
	}
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_session_id", "session_id must be a valid UUID", r,
			map[string]any{"field": "session_id"},
		))
		return
	}

	if req.ChannelID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.missing_channel_id", "channel_id is required", r,
			map[string]any{"field": "channel_id"},
		))
		return
	}
	channelID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_channel_id", "channel_id must be a valid UUID", r,
			map[string]any{"field": "channel_id"},
		))
		return
	}

	if req.OrgID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.missing_org_id", "org_id is required", r,
			map[string]any{"field": "org_id"},
		))
		return
	}
	orgID, err := uuid.Parse(req.OrgID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_org_id", "org_id must be a valid UUID", r,
			map[string]any{"field": "org_id"},
		))
		return
	}

	if req.Quantity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_quantity", "quantity must be greater than 0", r,
			map[string]any{"field": "quantity"},
		))
		return
	}

	// Optional tier_id.
	var tierID *uuid.UUID
	if req.TierID != "" {
		tid, err := uuid.Parse(req.TierID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.invalid_tier_id", "tier_id must be a valid UUID", r,
				map[string]any{"field": "tier_id"},
			))
			return
		}
		tierID = &tid
	}

	// User from JWT actor.
	var userID *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil {
		userID = &uid
	}

	// Compute expires_at — channel override → org default → system fallback.
	// Nil queries (test wiring) and lookup errors transparently fall through to
	// the next tier and ultimately to defaultReservationTTL; see
	// resolveReservationTTL for the precedence contract.
	var channelQ channelTTLLookup
	if h.channelQueries != nil {
		channelQ = h.channelQueries
	}
	var orgQ orgTTLLookup
	if h.orgQueries != nil {
		orgQ = h.orgQueries
	}
	ttl := resolveReservationTTL(ctx, channelQ, orgQ, channelID, orgID)
	expiresAt := time.Now().UTC().Add(ttl)

	// Begin transaction: ReserveCapacity + InsertReservation atomically.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	invQ := h.inventoryQueries.WithTx(tx)
	resQ := h.reservationQueries.WithTx(tx)

	// Reserve capacity — returns pgx.ErrNoRows when over-capacity.
	if _, err := invQ.ReserveCapacity(ctx, sessionID, tierID, req.Quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"reservation.over_capacity", "insufficient capacity for this reservation", r,
			))
			return
		}
		h.logger.Error("reservation: reserve capacity failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.capacity_failed", "failed to reserve capacity", r,
		))
		return
	}

	// Insert the reservation record.
	res, err := resQ.InsertReservation(ctx, orgID, channelID, sessionID, tierID, userID, req.Quantity, expiresAt)
	if err != nil {
		h.logger.Error("reservation: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "failed to create reservation", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"reservation": reservationFromRow(res),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/reservations/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetReservation serves GET /v1/reservations/{id}.
// Requires JWT + "reservation.read" permission.
func (h *Handler) HandleGetReservation(w http.ResponseWriter, r *http.Request) {
	if h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.invalid_id", "id must be a valid UUID", r))
		return
	}

	res, err := h.reservationQueries.GetReservationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reservation.not_found", "reservation not found", r))
			return
		}
		h.logger.Error("reservation: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.get_failed", "failed to get reservation", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"reservation": reservationFromRow(res),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/reservations/{id}/activate
// ─────────────────────────────────────────────────────────────────────────────

// HandleActivateReservation serves PATCH /v1/reservations/{id}/activate.
// Transitions the reservation from draft → active.
// Requires JWT + "reservation.activate" permission.
func (h *Handler) HandleActivateReservation(w http.ResponseWriter, r *http.Request) {
	if h.reservationQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.invalid_id", "id must be a valid UUID", r))
		return
	}

	// Fetch current state.
	current, err := h.reservationQueries.GetReservationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reservation.not_found", "reservation not found", r))
			return
		}
		h.logger.Error("reservation: get for activate failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.get_failed", "failed to get reservation", r,
		))
		return
	}

	// Validate state transition: must be draft → active.
	if !isValidReservationTransition(current.State, "active") {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_transition",
			"reservation cannot be activated from state '"+current.State+"'",
			r,
			map[string]any{
				"current_state": current.State,
				"target_state":  "active",
			},
		))
		return
	}

	// Check the reservation has not already expired.
	if time.Now().UTC().After(current.ExpiresAt) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"reservation.expired", "reservation has expired and cannot be activated", r,
		))
		return
	}

	updated, err := h.reservationQueries.UpdateReservationState(ctx, id, "active")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reservation.not_found", "reservation not found", r))
			return
		}
		h.logger.Error("reservation: activate failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.activate_failed", "failed to activate reservation", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"reservation": reservationFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/reservations/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleCancelReservation serves DELETE /v1/reservations/{id}.
// Transitions the reservation from draft|active → cancelled and releases inventory.
// Requires JWT + "reservation.cancel" permission.
func (h *Handler) HandleCancelReservation(w http.ResponseWriter, r *http.Request) {
	if h.reservationQueries == nil || h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("reservation.invalid_id", "id must be a valid UUID", r))
		return
	}

	// Fetch current state.
	current, err := h.reservationQueries.GetReservationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reservation.not_found", "reservation not found", r))
			return
		}
		h.logger.Error("reservation: get for cancel failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.get_failed", "failed to get reservation", r,
		))
		return
	}

	// Validate that the transition to cancelled is allowed.
	if !isValidReservationTransition(current.State, "cancelled") {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"reservation.invalid_transition",
			"reservation cannot be cancelled from state '"+current.State+"'",
			r,
			map[string]any{
				"current_state": current.State,
				"target_state":  "cancelled",
			},
		))
		return
	}

	// Begin transaction: UpdateReservationState + ReleaseCapacity atomically.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resQ := h.reservationQueries.WithTx(tx)
	invQ := h.inventoryQueries.WithTx(tx)

	cancelled, err := resQ.UpdateReservationState(ctx, id, "cancelled")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reservation.not_found", "reservation not found", r))
			return
		}
		h.logger.Error("reservation: cancel state update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.cancel_failed", "failed to cancel reservation", r,
		))
		return
	}

	// Release held capacity back to available.
	if _, err := invQ.ReleaseCapacity(ctx, current.SessionID, current.TierID, current.Quantity); err != nil {
		h.logger.Error("reservation: release capacity failed",
			slog.String("reservation_id", id.String()),
			slog.String("error", err.Error()),
		)
		// Non-fatal for the cancel operation itself — the reservation is already
		// marked cancelled. Log and continue.
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"reservation": reservationFromRow(cancelled),
		"cancelled":   true,
	})
}
