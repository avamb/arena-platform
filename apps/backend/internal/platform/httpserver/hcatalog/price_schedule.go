// price_schedule.go — AB-48 scheduled pricing + bulk pricing grids.
//
//	GET  .../sessions/{session_id}/tiers/{id}/price-schedule
//	PUT  .../sessions/{session_id}/tiers/{id}/price-schedule
//	POST .../events/{event_id}/sessions/pricing-bulk
//
// Windows live in ticket_tier_prices (0087). Overlap is impossible at the
// DB level (GiST exclusion); this file validates shape, maps 23P01 to a
// 422, and audits every change — post-sale price edits are money, and an
// untracked edit is unacceptable (AB-48 step 6).
//
// Mid-sale rules (AB-48 step 10) are a property of read-time resolution,
// not of this writer: future windows are freely editable; a change to the
// currently active window applies to NEW carts only because every
// reservation locks its quoted price at creation (reservation_ga_items);
// issued tickets are never repriced.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// pgExclusionViolation is SQLSTATE 23P01 — raised by the GiST constraint
// when two windows of one tier overlap.
const pgExclusionViolation = "23P01"

// maxPriceWindows bounds a single schedule; a real schedule has a handful
// of phases (early bird / standard / last call).
const maxPriceWindows = 50

// priceWindowInput is one window on the wire.
type priceWindowInput struct {
	ValidFrom   string  `json:"valid_from"`
	ValidTo     *string `json:"valid_to"`
	PriceAmount int64   `json:"price_amount"`
}

// priceWindowResponse is one ticket_tier_prices row on the wire.
type priceWindowResponse struct {
	ID          string  `json:"id"`
	TierID      string  `json:"tier_id"`
	ValidFrom   string  `json:"valid_from"`
	ValidTo     *string `json:"valid_to"`
	PriceAmount int64   `json:"price_amount"`
}

func priceWindowFromRow(r gen.TicketTierPriceRow) priceWindowResponse {
	out := priceWindowResponse{
		ID:          r.ID.String(),
		TierID:      r.TierID.String(),
		ValidFrom:   r.ValidFrom.UTC().Format(time.RFC3339),
		PriceAmount: r.PriceAmount,
	}
	if r.ValidTo != nil {
		s := r.ValidTo.UTC().Format(time.RFC3339)
		out.ValidTo = &s
	}
	return out
}

// priceScheduleResponse is the GET/PUT envelope: the windows plus the
// resolved "now" view so the admin shows what buyers see.
type priceScheduleResponse struct {
	TierID            string                `json:"tier_id"`
	BasePriceAmount   int64                 `json:"base_price_amount"`
	CurrentPrice      int64                 `json:"current_price"`
	NextPriceChangeAt *string               `json:"next_price_change_at"`
	Windows           []priceWindowResponse `json:"windows"`
}

// parsedWindow is a validated, time-typed window.
type parsedWindow struct {
	from   time.Time
	to     *time.Time
	amount int64
}

// parsePriceWindows validates the wire shape: RFC3339 times, to > from,
// non-negative amounts, bounded count, and an app-side overlap pre-check
// (the DB constraint remains the authority). Returns (nil, code, msg) on
// failure — code is a 422 error code.
func parsePriceWindows(in []priceWindowInput) ([]parsedWindow, string, string) {
	if len(in) > maxPriceWindows {
		return nil, "tier.too_many_price_windows", "a schedule may carry at most 50 windows"
	}
	out := make([]parsedWindow, 0, len(in))
	for i, w := range in {
		from, err := time.Parse(time.RFC3339, strings.TrimSpace(w.ValidFrom))
		if err != nil {
			return nil, "tier.invalid_price_window", "windows[" + strconv.Itoa(i) + "].valid_from must be RFC3339"
		}
		var to *time.Time
		if w.ValidTo != nil && strings.TrimSpace(*w.ValidTo) != "" {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(*w.ValidTo))
			if err != nil {
				return nil, "tier.invalid_price_window", "windows[" + strconv.Itoa(i) + "].valid_to must be RFC3339"
			}
			if !t.After(from) {
				return nil, "tier.invalid_price_window", "windows[" + strconv.Itoa(i) + "].valid_to must be after valid_from"
			}
			to = &t
		}
		if w.PriceAmount < 0 {
			return nil, "tier.invalid_price_window", "windows[" + strconv.Itoa(i) + "].price_amount must be >= 0"
		}
		out = append(out, parsedWindow{from: from, to: to, amount: w.PriceAmount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	for i := 1; i < len(out); i++ {
		prev := out[i-1]
		if prev.to == nil || out[i].from.Before(*prev.to) {
			return nil, "tier.price_windows_overlap",
				"price windows overlap — each moment may carry exactly one scheduled price"
		}
	}
	return out, "", ""
}

// scheduleResponse builds the envelope for a tier from its current rows.
func scheduleResponse(ctx context.Context, q *gen.Queries, tier gen.TicketTierRow, rows []gen.TicketTierPriceRow) priceScheduleResponse {
	resp := priceScheduleResponse{
		TierID:          tier.ID.String(),
		BasePriceAmount: tier.PriceAmount,
		CurrentPrice:    tier.PriceAmount,
		Windows:         make([]priceWindowResponse, 0, len(rows)),
	}
	for _, r := range rows {
		resp.Windows = append(resp.Windows, priceWindowFromRow(r))
	}
	if eff, err := priceresolve.ForTier(ctx, q, tier, time.Now().UTC()); err == nil {
		resp.CurrentPrice = eff.Amount
		if eff.NextChangeAt != nil {
			s := eff.NextChangeAt.UTC().Format(time.RFC3339)
			resp.NextPriceChangeAt = &s
		}
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// GET .../tiers/{id}/price-schedule
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetTierPriceSchedule serves the tier's windows plus the resolved
// current price. Requires JWT + tier.read.
func (h *Handler) HandleGetTierPriceSchedule(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, sessionID, tierID, ok := h.tierPathParams(w, r)
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
		h.logger.Error("tier: schedule get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.get_failed", "failed to get ticket tier", r))
		return
	}
	rows, err := h.tierQueries.ListTierPriceWindows(ctx, []uuid.UUID{tier.ID})
	if err != nil {
		h.logger.Error("tier: schedule list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.schedule_failed", "failed to load price schedule", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"price_schedule": scheduleResponse(ctx, h.tierQueries, tier, rows),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT .../tiers/{id}/price-schedule  (replace-all)
// ─────────────────────────────────────────────────────────────────────────────

type putPriceScheduleRequest struct {
	Windows []priceWindowInput `json:"windows"`
}

// HandlePutTierPriceSchedule replaces a tier's whole schedule atomically
// and audits old → new. Requires JWT + tier.update.
func (h *Handler) HandlePutTierPriceSchedule(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, sessionID, tierID, ok := h.tierPathParams(w, r)
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	var req putPriceScheduleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"tier.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return
	}
	windows, errCode, errMsg := parsePriceWindows(req.Windows)
	if errCode != "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: schedule put get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.get_failed", "failed to get ticket tier", r))
		return
	}
	if tier.PricingMode != "fixed" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"tier.schedule_requires_fixed_mode",
			"only fixed-price tiers carry a price schedule", r,
			map[string]any{"pricing_mode": tier.PricingMode},
		))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := h.tierQueries.WithTx(tx)

	before, err := txq.ListTierPriceWindows(ctx, []uuid.UUID{tier.ID})
	if err != nil {
		h.logger.Error("tier: schedule snapshot failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.schedule_failed", "failed to load price schedule", r))
		return
	}
	rows, errCode, errMsg := replaceTierScheduleTx(ctx, txq, tier.ID, windows)
	if errCode != "" {
		status := http.StatusUnprocessableEntity
		if errCode == "tier.schedule_failed" {
			status = http.StatusInternalServerError
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.tier.price_schedule.replace",
			ResourceType: "ticket_tier",
			ResourceID:   tier.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"session_id": sessionID.String(),
				"before":     windowsForAudit(before),
				"after":      windowsForAudit(rows),
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			h.logger.Error("tier: schedule audit failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.audit_failed", "failed to write audit event", r))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("tier.commit_failed", "failed to commit price schedule", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"price_schedule": scheduleResponse(ctx, h.tierQueries, tier, rows),
	})
}

// replaceTierScheduleTx wipes and re-inserts a tier's windows inside tx.
// Maps the exclusion violation to tier.price_windows_overlap.
func replaceTierScheduleTx(ctx context.Context, txq *gen.Queries, tierID uuid.UUID, windows []parsedWindow) ([]gen.TicketTierPriceRow, string, string) {
	if _, err := txq.DeleteTierPriceWindowsByTier(ctx, tierID); err != nil {
		return nil, "tier.schedule_failed", "failed to replace price schedule"
	}
	rows := make([]gen.TicketTierPriceRow, 0, len(windows))
	for _, wnd := range windows {
		row, err := txq.InsertTierPriceWindow(ctx, tierID, wnd.from, wnd.to, wnd.amount)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgExclusionViolation {
				return nil, "tier.price_windows_overlap",
					"price windows overlap — each moment may carry exactly one scheduled price"
			}
			return nil, "tier.schedule_failed", "failed to replace price schedule"
		}
		rows = append(rows, row)
	}
	return rows, "", ""
}

func windowsForAudit(rows []gen.TicketTierPriceRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		m := map[string]any{
			"valid_from":   r.ValidFrom.UTC().Format(time.RFC3339),
			"price_amount": r.PriceAmount,
		}
		if r.ValidTo != nil {
			m["valid_to"] = r.ValidTo.UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}

// tierPathParams parses org_id / event_id / session_id / id.
func (h *Handler) tierPathParams(w http.ResponseWriter, r *http.Request) (orgID, sessionID, tierID uuid.UUID, ok bool) {
	orgID, ok = httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if _, ok = httputil.UUIDPathParam(w, r, "event_id"); !ok {
		return
	}
	sessionID, ok = httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok = httputil.UUIDPathParam(w, r, "id")
	return
}

// ─────────────────────────────────────────────────────────────────────────────
// POST .../events/{event_id}/sessions/pricing-bulk
// ─────────────────────────────────────────────────────────────────────────────

// bulkPriceItem is one grid row: a category (tier) NAME — tiers are
// minted per plan category, so the same name identifies the same
// category across sessions — with a base price and optional schedule.
type bulkPriceItem struct {
	TierName    string             `json:"tier_name"`
	PriceAmount int64              `json:"price_amount"`
	Windows     []priceWindowInput `json:"windows"`
}

type bulkPricingRequest struct {
	SessionIDs []string        `json:"session_ids"`
	Prices     []bulkPriceItem `json:"prices"`
}

type bulkPricingSessionResult struct {
	SessionID    string   `json:"session_id"`
	Applied      []string `json:"applied"`
	MissingTiers []string `json:"missing_tiers"`
	Error        *string  `json:"error"`
}

// maxBulkSessions bounds a grid application.
const maxBulkSessions = 100

// HandleBulkSessionPricing applies one price grid to several sessions of
// an event in one pass (AB-48 step 5, the reference's "set →" multi-
// select). Each session is applied in its own transaction; the response
// reports per-session outcomes so a partial failure is visible, never
// silent. Requires JWT + tier.update.
func (h *Handler) HandleBulkSessionPricing(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.sessionQueries == nil || h.pool == nil {
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
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.tierQueries, orgID) {
		return
	}

	var req bulkPricingRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"tier.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return
	}
	if len(req.SessionIDs) == 0 || len(req.SessionIDs) > maxBulkSessions {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_session_ids", "session_ids must carry 1..100 entries", r,
			map[string]any{"field": "session_ids"},
		))
		return
	}
	if len(req.Prices) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_prices", "prices must be a non-empty array", r,
			map[string]any{"field": "prices"},
		))
		return
	}
	sessionIDs := make([]uuid.UUID, 0, len(req.SessionIDs))
	for _, raw := range req.SessionIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_session_ids", "session_ids must be UUIDs", r,
				map[string]any{"field": "session_ids", "value": raw},
			))
			return
		}
		sessionIDs = append(sessionIDs, id)
	}
	type gridItem struct {
		amount  int64
		windows []parsedWindow
	}
	grid := make(map[string]gridItem, len(req.Prices))
	for _, p := range req.Prices {
		name := strings.ToLower(strings.TrimSpace(p.TierName))
		if name == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_prices", "prices[].tier_name is required", r,
				map[string]any{"field": "prices"},
			))
			return
		}
		if p.PriceAmount < 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_prices", "prices[].price_amount must be >= 0", r,
				map[string]any{"field": "prices", "tier_name": p.TierName},
			))
			return
		}
		windows, errCode, errMsg := parsePriceWindows(p.Windows)
		if errCode != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, errMsg+" (tier "+p.TierName+")", r))
			return
		}
		grid[name] = gridItem{amount: p.PriceAmount, windows: windows}
	}

	actor, _ := auth.ActorFromContext(ctx)
	results := make([]bulkPricingSessionResult, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		res := bulkPricingSessionResult{SessionID: sid.String(), Applied: []string{}, MissingTiers: []string{}}
		if _, err := h.sessionQueries.GetSessionByID(ctx, sid, eventID); err != nil {
			msg := "session not found in this event"
			if !errors.Is(err, pgx.ErrNoRows) {
				msg = "failed to load session"
			}
			res.Error = &msg
			results = append(results, res)
			continue
		}
		tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sid)
		if err != nil {
			msg := "failed to list tiers"
			res.Error = &msg
			results = append(results, res)
			continue
		}
		byName := make(map[string]gen.TicketTierRow, len(tiers))
		for _, t := range tiers {
			byName[strings.ToLower(strings.TrimSpace(t.Name))] = t
		}
		for name := range grid {
			if _, ok := byName[name]; !ok {
				res.MissingTiers = append(res.MissingTiers, name)
			}
		}
		sort.Strings(res.MissingTiers)

		if errMsg := h.applyGridToSessionTx(ctx, r, actor.ID, sid, byName, func(name string) (int64, []parsedWindow, bool) {
			g, ok := grid[name]
			return g.amount, g.windows, ok
		}, &res.Applied); errMsg != "" {
			res.Error = &errMsg
		}
		sort.Strings(res.Applied)
		results = append(results, res)
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"results": results,
	})
}

// applyGridToSessionTx writes the grid for one session in one transaction:
// base price + schedule per matching tier, with one audit event per tier
// (old → new). Returns "" on success or an error message.
func (h *Handler) applyGridToSessionTx(
	ctx context.Context,
	r *http.Request,
	actorID string,
	sessionID uuid.UUID,
	byName map[string]gen.TicketTierRow,
	lookup func(name string) (int64, []parsedWindow, bool),
	applied *[]string,
) string {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "failed to begin transaction"
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := h.tierQueries.WithTx(tx)

	for name, tier := range byName {
		amount, windows, ok := lookup(name)
		if !ok {
			continue
		}
		if tier.PricingMode != "fixed" {
			// A grid only makes sense for fixed-price categories.
			continue
		}
		before, err := txq.ListTierPriceWindows(ctx, []uuid.UUID{tier.ID})
		if err != nil {
			return "failed to load price schedule for " + tier.Name
		}
		updated, err := txq.UpdateTicketTier(ctx, tier.ID, sessionID, "", "", &amount, "", nil, nil, nil, nil, nil, nil)
		if err != nil {
			return "failed to update price for " + tier.Name
		}
		rows, errCode, errMsg := replaceTierScheduleTx(ctx, txq, tier.ID, windows)
		if errCode != "" {
			return errMsg + " (" + tier.Name + ")"
		}
		if h.audit != nil {
			ev := audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    "user",
				ActorID:      actorID,
				Action:       "v1.tier.price.bulk",
				ResourceType: "ticket_tier",
				ResourceID:   tier.ID.String(),
				RequestID:    logging.RequestID(ctx),
				TraceID:      logging.TraceID(ctx),
				IP:           httputil.ExtractClientIP(r),
				Metadata: map[string]any{
					"session_id":        sessionID.String(),
					"tier_name":         tier.Name,
					"price_amount_from": tier.PriceAmount,
					"price_amount_to":   updated.PriceAmount,
					"schedule_before":   windowsForAudit(before),
					"schedule_after":    windowsForAudit(rows),
				},
			}
			if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
				return "failed to write audit event for " + tier.Name
			}
		}
		*applied = append(*applied, tier.Name)
	}
	if err := tx.Commit(ctx); err != nil {
		return "failed to commit"
	}
	return ""
}
