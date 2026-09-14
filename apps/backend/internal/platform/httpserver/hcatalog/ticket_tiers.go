// ticket_tiers.go implements the ticket tier CRUD API endpoints (feature #127).
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ValidPricingModes lists the allowed pricing_mode values.
var ValidPricingModes = map[string]bool{
	string(catalogdomain.PricingModeFixed): true,
	string(catalogdomain.PricingModeFree):  true,
	string(catalogdomain.PricingModePWYW):  true,
}

// ValidatePricingMode enforces pricing-mode invariants. Returns (errorCode,
// errorMessage) on failure, ("", "") on success. Exported so httpserver
// shims and tests can call it without importing the domain layer.
func ValidatePricingMode(mode string, priceAmount int64, pwywMin, pwywMax *int64) (string, string) {
	return catalogdomain.ValidatePricingMode(mode, priceAmount, pwywMin, pwywMax)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type tierResponse struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"`
	Currency        string  `json:"currency"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       int32   `json:"sort_order"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	// AB-48 step 3: inventory bound to this category (list endpoint only):
	// physical seats with this tier and GA units in this tier's pool.
	SeatCount   *int64 `json:"seat_count,omitempty"`
	GAUnitCount *int64 `json:"ga_unit_count,omitempty"`
	// IsOpen is false for a category an operator closed: it accepts no new
	// holds, while places already held or sold are untouched and an order
	// already placed can still be paid (migration 0101, decision 1).
	IsOpen bool `json:"is_open"`
	// Kind is "seated" when the category's places come from the plan
	// geometry (its quantity is read-only) and "ga" when it owns General
	// Admission places. Derived, never stored. List endpoint only.
	Kind *string `json:"kind,omitempty"`
	// Quantity / Held / Sold / Available are the category's own place
	// counters (plan 08_architecture/23 step 5). Quantity is how many
	// places it owns — the quota mechanism keeps ticket_tiers.capacity
	// equal to it. List endpoint only; absent for a category that owns no
	// place at all.
	Quantity  *int32 `json:"quantity,omitempty"`
	Held      *int32 `json:"held,omitempty"`
	Sold      *int32 `json:"sold,omitempty"`
	Available *int32 `json:"available,omitempty"`
}

// TierResponse is the exported alias of tierResponse for use by the httpserver
// shim layer (ticket_tiers_test.go in package httpserver references tierFromRow
// via catalog_shims.go and reads response fields directly).
type TierResponse = tierResponse

// TierFromRow is the exported alias of tierFromRow for use by the httpserver
// shim layer (ticket_tiers_test.go calls tierFromRow via catalog_shims.go).
func TierFromRow(t gen.TicketTierRow) TierResponse { return tierFromRow(t) }

func tierFromRow(t gen.TicketTierRow) tierResponse {
	resp := tierResponse{
		ID:          t.ID.String(),
		SessionID:   t.SessionID.String(),
		Name:        t.Name,
		PricingMode: t.PricingMode,
		PriceAmount: t.PriceAmount,
		Currency:    strings.TrimSpace(t.Currency),
		PwywMin:     t.PwywMin,
		PwywMax:     t.PwywMax,
		Capacity:    t.Capacity,
		SortOrder:   t.SortOrder,
		IsOpen:      t.IsOpen,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if t.SaleWindowStart != nil {
		s := t.SaleWindowStart.UTC().Format(time.RFC3339)
		resp.SaleWindowStart = &s
	}
	if t.SaleWindowEnd != nil {
		s := t.SaleWindowEnd.UTC().Format(time.RFC3339)
		resp.SaleWindowEnd = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST .../sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

// createTierRequest carries the operator inputs for a new tier. Currency is
// deliberately NOT an input (AB-38): every tier is denominated in its
// session's currency — the handler resolves it server-side, and the
// composite FK ticket_tiers_currency_matches_session would reject anything
// else. A "currency" key sent by an older client is silently ignored.
type createTierRequest struct {
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       int32   `json:"sort_order"`
}

func (h *Handler) HandleCreateTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req createTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PricingMode = strings.TrimSpace(req.PricingMode)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.missing_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	if req.PricingMode == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.missing_pricing_mode", "pricing_mode is required", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}
	if !ValidPricingModes[req.PricingMode] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}

	if errCode, errMsg := ValidatePricingMode(req.PricingMode, req.PriceAmount, req.PwywMin, req.PwywMax); errCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	if req.Capacity != nil && *req.Capacity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	// Currency follows the session (AB-38). Resolved after every syntactic
	// validation so cheap 400s never cost a DB round trip; resolving it
	// also verifies the session exists before the INSERT.
	sessionCurrency, err := h.tierQueries.GetSessionCurrency(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("tier: session currency lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.insert_failed", "failed to resolve session currency", r,
		))
		return
	}

	// Plan 08_architecture/23 step 3: on a general-admission or hybrid
	// session a category OWNS its places, so creating one is a quota
	// operation — the tier row alone sells nothing.
	quantity, qOK := h.resolveNewCategoryQuantity(w, r, sessionID, req.Capacity)
	if !qOK {
		return
	}

	var tier gen.TicketTierRow
	insert := func(txq *gen.Queries) error {
		row, insErr := txq.InsertTicketTier(ctx,
			sessionID,
			req.Name, req.PricingMode,
			req.PriceAmount, sessionCurrency,
			req.PwywMin, req.PwywMax,
			req.Capacity,
			saleStart, saleEnd,
			req.SortOrder,
		)
		if insErr != nil {
			return insErr
		}
		tier = row
		if quantity <= 0 {
			// No quantity and none derivable: the tier is a pure pricing /
			// geometry-mapping row (a seated session's category before its
			// plan is bound). It owns no place — today's behaviour.
			return nil
		}
		// Same transaction as the INSERT: a category that fails to
		// materialize its places must not survive as an unsellable row.
		// CreateCategory also flips an assigned_seats session to hybrid
		// (decision 10) and recomputes capacity_total + the ledger.
		if err := gaquota.CreateCategory(ctx, txq, sessionID, row.ID, quantity); err != nil {
			return err
		}
		fresh, getErr := txq.GetTicketTierByID(ctx, row.ID, sessionID)
		if getErr != nil {
			return getErr
		}
		tier = fresh
		return nil
	}

	if err := gaquota.InTx(ctx, h.pool, h.tierQueries, insert); err != nil {
		h.logger.Error("tier: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.insert_failed", "failed to create ticket tier", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// resolveNewCategoryQuantity decides how many places a category created
// through POST .../tiers should own (plan 08_architecture/23 step 3).
//
//   - an explicit `capacity` is the quantity, on every admission mode — on a
//     seated session it makes the new category a GA one and flips the session
//     to hybrid (decision 10);
//   - without one, a general_admission / hybrid session falls back to the
//     wave-A default: the session's capacity_override (its capacity_total
//     while no category owns a place yet) minus the quantities already
//     claimed by its categories. A non-positive remainder is refused with
//     400 tier.capacity_required — decision 3 forbids a GA category without
//     a quantity;
//   - without one on an assigned_seats session the result is 0: the tier is
//     created as a pure geometry-mapping row, exactly as before.
//
// Returns ok=false after writing the error envelope.
func (h *Handler) resolveNewCategoryQuantity(
	w http.ResponseWriter, r *http.Request, sessionID uuid.UUID, requested *int32,
) (int32, bool) {
	if requested != nil {
		return *requested, true
	}
	ctx := r.Context()
	sess, err := h.tierQueries.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return 0, false
		}
		h.logger.Error("tier: session admission lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.insert_failed", "failed to resolve session admission mode", r,
		))
		return 0, false
	}
	if sess.AdmissionMode == "assigned_seats" {
		return 0, true
	}

	stats, err := gaquota.SessionStats(ctx, h.tierQueries, sessionID)
	if err != nil {
		h.logger.Error("tier: category place counters failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.insert_failed", "failed to read category quantities", r,
		))
		return 0, false
	}
	var claimed int32
	for _, st := range stats {
		claimed += st.Quantity
	}
	var basis int32
	switch {
	case sess.CapacityOverride != nil:
		basis = *sess.CapacityOverride
	case len(stats) == 0:
		// Nothing owns a place yet, so capacity_total is still the
		// provisional value the session was created with.
		basis = sess.CapacityTotal
	}
	if basis-claimed <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.capacity_required",
			"capacity is required: a general-admission category owns its places and "+
				"the session has no capacity left to derive a default quantity from", r,
			map[string]any{
				"field":             "capacity",
				"session_capacity":  basis,
				"claimed_by_tiers":  claimed,
				"remaining_default": basis - claimed,
			},
		))
		return 0, false
	}
	return basis - claimed, true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET .../sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListTiers(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	rows, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("tier: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.list_failed", "failed to list ticket tiers", r,
		))
		return
	}

	// AB-48 step 3: seat count / GA capacity beside each category.
	seatCounts := map[uuid.UUID]int64{}
	gaCounts := map[uuid.UUID]int64{}
	if counts, cntErr := h.tierQueries.CountSessionSeatsByTier(ctx, sessionID); cntErr != nil {
		h.logger.Warn("tier: seat counts failed (non-fatal)", slog.String("error", cntErr.Error()))
	} else {
		for _, c := range counts {
			if c.Kind == "ga_unit" {
				gaCounts[c.TierID] += c.Count
			} else {
				seatCounts[c.TierID] += c.Count
			}
		}
	}

	// Plan 08_architecture/23 step 5: the admin category table shows what
	// each category owns and what is left of it. Non-fatal — a failure
	// leaves the counters absent rather than failing the list.
	placeStats, statsErr := gaquota.SessionStats(ctx, h.tierQueries, sessionID)
	if statsErr != nil {
		h.logger.Warn("tier: category place counters failed (non-fatal)",
			slog.String("error", statsErr.Error()))
		placeStats = nil
	}

	result := make([]tierResponse, 0, len(rows))
	for _, t := range rows {
		tr := tierFromRow(t)
		sc, gc := seatCounts[t.ID], gaCounts[t.ID]
		tr.SeatCount, tr.GAUnitCount = &sc, &gc

		// A category is SEATED exactly when it owns coordinate-bearing
		// seats — the same rule gaquota.CategoryKind applies, derived here
		// from the counts already in hand rather than re-querying per row.
		kind := string(gaquota.KindGA)
		if sc > 0 {
			kind = string(gaquota.KindSeated)
		}
		tr.Kind = &kind

		if st, ok := placeStats[t.ID]; ok {
			quantity, held, sold, available := st.Quantity, st.Held, st.Sold, st.Available
			tr.Quantity, tr.Held, tr.Sold, tr.Available = &quantity, &held, &sold, &available
		}
		result = append(result, tr)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tiers": result,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateTierRequest carries the patchable tier fields. Currency is not
// patchable (AB-38): a tier always carries its session's currency; changing
// the currency happens on the session and cascades to every tier.
type updateTierRequest struct {
	Name            *string `json:"name"`
	PricingMode     *string `json:"pricing_mode"`
	PriceAmount     *int64  `json:"price_amount"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       *int32  `json:"sort_order"`
	// IsOpen opens or closes the category (migration 0101, decision 1): a
	// closed category accepts no NEW hold while everything already held or
	// sold stays untouched and an order already placed can still be paid.
	// Optional — omitting the key leaves the flag alone.
	IsOpen *bool `json:"is_open"`
}

func (h *Handler) HandleUpdateTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req updateTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	pricingMode := ""
	if req.PricingMode != nil {
		pricingMode = strings.TrimSpace(*req.PricingMode)
		if pricingMode != "" && !ValidPricingModes[pricingMode] {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
				map[string]any{"field": "pricing_mode"},
			))
			return
		}
	}

	if req.Capacity != nil && *req.Capacity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	current, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: get for update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

	effectiveMode := current.PricingMode
	if pricingMode != "" {
		effectiveMode = pricingMode
	}
	effectivePrice := current.PriceAmount
	if req.PriceAmount != nil {
		effectivePrice = *req.PriceAmount
	}
	effectivePwywMin := current.PwywMin
	if req.PwywMin != nil {
		effectivePwywMin = req.PwywMin
	}
	effectivePwywMax := current.PwywMax
	if req.PwywMax != nil {
		effectivePwywMax = req.PwywMax
	}

	if errCode, errMsg := ValidatePricingMode(effectiveMode, effectivePrice, effectivePwywMin, effectivePwywMax); errCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_name", "name cannot be empty", r,
				map[string]any{"field": "name"},
			))
			return
		}
	}
	// Currency is never patched here — empty string means "keep existing"
	// in UpdateTicketTier, and the existing value already equals the
	// session's currency (composite FK invariant, AB-38).
	//
	// capacity is deliberately NOT passed to UpdateTicketTier: on a
	// category that owns places it is the QUANTITY, and only the quota
	// mechanism may write it (plan 08_architecture/23 step 3) — it has to
	// mint or remove the matching places in the same transaction.
	var updated gen.TicketTierRow
	err = gaquota.InTx(ctx, h.pool, h.tierQueries, func(txq *gen.Queries) error {
		row, updErr := txq.UpdateTicketTier(ctx,
			tierID, sessionID,
			name, pricingMode,
			req.PriceAmount, "",
			req.PwywMin, req.PwywMax,
			nil,
			saleStart, saleEnd,
			req.SortOrder,
		)
		if updErr != nil {
			return updErr
		}
		updated = row
		if req.IsOpen != nil && *req.IsOpen != row.IsOpen {
			if err := gaquota.SetOpen(ctx, txq, sessionID, tierID, *req.IsOpen); err != nil {
				return err
			}
		}
		if req.Capacity != nil {
			if err := applyCategoryQuantity(ctx, txq, sessionID, tierID, *req.Capacity); err != nil {
				return err
			}
		}
		if req.IsOpen != nil || req.Capacity != nil {
			fresh, getErr := txq.GetTicketTierByID(ctx, tierID, sessionID)
			if getErr != nil {
				return getErr
			}
			updated = fresh
		}
		return nil
	})
	if err != nil {
		if writeQuotaError(w, r, err) {
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.update_failed", "failed to update ticket tier", r,
		))
		return
	}

	// AB-48 step 6: a price edit is money — audit who changed which
	// category from what to what, when. Post-commit, best-effort (the
	// update itself is already durable; a lost audit row is logged loudly).
	if h.audit != nil && updated.PriceAmount != current.PriceAmount {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.tier.price.update",
			ResourceType: "ticket_tier",
			ResourceID:   tierID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"session_id":        sessionID.String(),
				"tier_name":         updated.Name,
				"price_amount_from": current.PriceAmount,
				"price_amount_to":   updated.PriceAmount,
				"currency":          updated.Currency,
			},
		}
		if err := h.audit.Write(ctx, ev); err != nil {
			h.logger.Error("tier: price audit write failed — price change is NOT in the audit log",
				slog.String("tier_id", tierID.String()), slog.String("error", err.Error()))
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(updated),
	})
}

// applyCategoryQuantity sets a category's quantity to the requested value
// through the quota mechanism (plan 08_architecture/23 step 3).
//
// A category that owns no place yet is CREATED — that is what mints its
// places, assigns its stable per-session number and, on an assigned_seats
// session, flips the session to hybrid (decision 10). One that already owns
// places is RESIZED, which mints or removes the difference. A request that
// changes nothing is a no-op rather than a second mint: CreateCategory adds
// places unconditionally, so re-sending the same capacity must not reach it.
func applyCategoryQuantity(
	ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID, quantity int32,
) error {
	stats, err := gaquota.CategoryStats(ctx, txq, sessionID, tierID)
	if err != nil {
		return err
	}
	if stats.Quantity == quantity {
		return nil
	}
	if stats.Quantity == 0 {
		kind, kErr := gaquota.CategoryKind(ctx, txq, sessionID, tierID)
		if kErr != nil {
			return kErr
		}
		if kind == gaquota.KindSeated {
			// The category's places are the plan geometry; its quantity is
			// read-only (decision 10).
			return gaquota.ErrSeatedCategory
		}
		return gaquota.CreateCategory(ctx, txq, sessionID, tierID, quantity)
	}
	_, err = gaquota.SetQuantity(ctx, txq, sessionID, tierID, quantity)
	return err
}

// writeQuotaError maps the typed errors of the quota mechanism onto the
// ticket-tier error envelope. Returns true when it wrote a response.
func writeQuotaError(w http.ResponseWriter, r *http.Request, err error) bool {
	var belowUsed *gaquota.BelowUsedError
	switch {
	case errors.As(err, &belowUsed):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"tier.quantity_below_used",
			"the requested quantity is below the places this category cannot give up", r,
			map[string]any{
				"field": "capacity",
				"used":  belowUsed.Used,
				"floor": belowUsed.Floor(),
			},
		))
		return true
	case errors.Is(err, gaquota.ErrSeatedCategory):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"tier.seated_category",
			"this category's places come from the seating plan; its quantity is read-only "+
				"and it cannot be deleted", r,
			map[string]any{"field": "capacity"},
		))
		return true
	case errors.Is(err, gaquota.ErrCategoryInUse):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"tier.in_use",
			"this category still holds or has sold places; close it instead of deleting it", r,
		))
		return true
	case errors.Is(err, gaquota.ErrInvalidQuantity):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return true
	case errors.Is(err, gaquota.ErrCategoryNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"tier.not_found", "ticket tier not found", r,
		))
		return true
	case errors.Is(err, gaquota.ErrSessionNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"session.not_found", "session not found", r,
		))
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.tierQueries.WithTx(tx)

	// Plan 08_architecture/23 step 3: deleting a category also removes the
	// places it owns and recomputes the session capacity + ledger, and is
	// refused outright for a seated category or one that still holds or has
	// sold a place (decision 2). A tier with no place at all — a legacy row
	// or a pure plan-mapping one — still just soft-deletes.
	//
	// The response row is read BEFORE the delete: the soft delete only sets
	// deleted_at/updated_at, neither of which the envelope carries.
	deleted, err := qtx.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: get for delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.delete_failed", "failed to delete ticket tier", r,
		))
		return
	}
	if err := gaquota.DeleteCategory(ctx, qtx, sessionID, tierID); err != nil {
		if writeQuotaError(w, r, err) {
			return
		}
		h.logger.Error("tier: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.delete_failed", "failed to delete ticket tier", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.tier.delete",
			ResourceType: "ticket_tier",
			ResourceID:   tierID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"session_id":   sessionID.String(),
				"pricing_mode": deleted.PricingMode,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("tier: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"tier.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier":    tierFromRow(deleted),
		"deleted": true,
	})
}
