// cmd_seat_list.go — Bil24-compatible GET_SEAT_LIST handler and its two
// per-mode branches (GA tier facade + per-unit assigned-seat / hybrid),
// plus the small pure helpers they share (seatListCurrency,
// bssStatusCode).
//
// Extracted from cmd_catalog.go by feature #476 W1-A2b slice 22 so the
// spec-mandated split (feature description: "no file over 700 lines")
// stays honored — GET_ALL_ACTIONS and its projection helpers grew past
// the ceiling as the actionList body caught up to spec §7.1, so
// GET_SEAT_LIST moves into its own file to make room. The dispatcher
// (HandleBil24Command in bil24_compat.go) stays the single central
// case-list — this file only owns the seat-list projection.
package hbil24

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET_SEAT_LIST — list ticket tiers for a session
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetSeatList maps GET_SEAT_LIST to either ticket-tier listing
// (general_admission) or the real assigned-seat inventory
// (assigned_seats / hybrid) for a specific event session. Feature #312
// Wave SEAT-D1 introduced the admission_mode branch on top of the
// pre-existing tier-facade behavior.
//
// Bil24 request fields used:
//   - actionEventId: platform session UUID (Bil24 event instance)
//
// Response shapes:
//
//   - general_admission (or admissionQ nil / session not resolvable to a
//     seating binding) — one entry per ticket_tier. Feature #476 slice 22
//     (spec §7.2) renamed the per-entry name key from the legacy
//     `categoryName` to spec-canonical `categoryPriceName` — the Bil24
//     wire uses the "categoryPrice" naming everywhere (categoryPriceId
//     is already the id key) and the WP plugin reads
//     `categoryPriceName` off the response envelope:
//
//     {
//     "categoryPriceId":   "<uuid>", "categoryPriceName": "...",
//     "price": <cents>, "currency": "USD",
//     "pricingMode": "fixed"|"free"|"pwyw",
//     "availableCount": <int or null>
//     }
//
//   - assigned_seats / hybrid — one entry per session_seat, per ADR-005
//     the seat identifier is the platform session_seats.id serialised
//     as a plain UUID string:
//
//     {
//     "seatId":          "<uuid>",       // session_seats.id as string
//     "categoryPriceId": "<uuid>",       // tier UUID (nullable)
//     "sector":          "...",
//     "row":             "...",
//     "number":          "...",
//     "price":           <cents>,        // 0 if no tier bound yet
//     "currency":        "USD",
//     "status":          <BSS int>       // 0 unavailable, 1 available, 3 held, 4 sold
//     }
//
// BSS status codes are the Bil24 seat-status wire values (§6 of the
// Bil24 gateway spec): 0 = unavailable (admin), 1 = available, 3 = held
// (reservation active), 4 = sold. The mapping never surfaces the internal
// row status string.
//
// Operator note: stadium-scale seat maps can push the seatList payload
// past 1 MiB. Enable gzip on the reverse proxy fronting POST
// /compat/bil24/json (nginx: gzip_types application/json; Cloudflare:
// Auto-Minify JSON + Brotli; Caddy: encode zstd gzip) so callers with
// Accept-Encoding: gzip receive a compressed response and the wire foot-
// print stays predictable.
func (h *Handler) handleBil24GetSeatList(w http.ResponseWriter, r *http.Request, req bil24Request) {
	// tier and seat services can be independently unwired; the outer
	// guard fails fast only if BOTH are missing (no data source at all
	// for either branch).
	if h.tierQueries == nil && h.seatQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "seat service unavailable",
		))
		return
	}

	ctx := r.Context()

	// Spec §4 / §7.2 (feature #476, W1-A2b): actionEventId is int64 on the
	// wire; resolveActionEventID rejects UUID input with -2 when compatDB is
	// wired and falls back to TranslateLegacyID for unit tests that omit the
	// pool.
	sessionID, err := h.resolveActionEventID(ctx, req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	// Feature #471 (spec §5, §7.2): validate fid+token and enforce that the
	// requested session belongs to the channel's org. Cross-tenant reads
	// through the compat surface are rejected as "not found in this
	// channel's organization" (-3).
	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return
	}
	if authed {
		if !h.enforceSessionOrg(ctx, w, req, sessionID, channel.OrgID) {
			return
		}
	}

	// Resolve admission_mode when the seating dependencies are wired.
	// Missing dependencies / lookup failures silently fall back to the
	// tier-facade behavior — legacy GA clients keep working during the
	// SEAT-D rollout even when the seating tables are empty.
	admissionMode := "general_admission"
	if h.admissionQ != nil {
		row, aerr := h.admissionQ.GetSessionAdmissionModeByID(ctx, sessionID)
		if aerr == nil && row.AdmissionMode != "" {
			admissionMode = row.AdmissionMode
		}
	}

	// Route: sessions with materialized seat/GA-unit rows emit per-unit
	// entries (AB-51 restored compat parity — every ticketable place has
	// a seatId); the tier facade remains the fallback for unwired seat
	// queries and legacy GA sessions without unit rows.
	if h.seatQ != nil {
		seats, serr := h.seatQ.ListSessionSeats(ctx, sessionID)
		if serr != nil && admissionMode != "general_admission" {
			h.logger.Error("bil24_compat: GET_SEAT_LIST: list session seats failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", serr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to retrieve seat list",
			))
			return
		}
		if serr == nil && (admissionMode != "general_admission" || len(seats) > 0) {
			h.getSeatListUnits(w, ctx, req, sessionID, admissionMode, seats)
			return
		}
	}
	if h.tierQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "tier service unavailable",
		))
		return
	}
	h.getSeatListGA(w, ctx, req, sessionID)
}

// getSeatListGA is the pre-#312 tier-facade GET_SEAT_LIST response for
// general_admission sessions (and the fallback whenever the SEAT-D
// dependencies are not wired). Kept factored out so the assigned-seat
// branch can remain a self-contained addition.
func (h *Handler) getSeatListGA(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID) {
	tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: list tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat list",
		))
		return
	}

	// AB-48: scheduled prices via the ONE resolver (base on lookup failure).
	effPrices, effErr := priceresolve.ForTiers(ctx, h.tierQueries, tiers, time.Now().UTC())
	if effErr != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: price window lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", effErr.Error()),
		)
		effPrices = nil
	}
	effectiveOf := func(t gen.TicketTierRow) int64 {
		if eff, ok := effPrices[t.ID]; ok {
			return eff.Amount
		}
		return t.PriceAmount
	}

	seatList := make([]map[string]any, 0, len(tiers))
	for _, t := range tiers {
		// Spec §4 / §7.2 (feature #476): int64 wire form via compat map.
		// Fallback (nil compatDB) returns the legacy UUID string so the
		// pre-W1 unit-test Handlers stay green.
		seatList = append(seatList, buildGASeatEntry(
			h.compatCategoryPriceID(ctx, t.ID), t, effectiveOf(t),
		))
	}

	// Spec §7.2 (feature #476 slice 21): the response envelope carries the
	// session-level currency at the top level. Bil24 goldens under
	// testdata/wp/golden/GET_SEAT_LIST/basic.json expect this key; every
	// tier of a session shares one currency (a mixed-currency session is
	// rejected at ticket_tier admission time), so the first non-empty tier
	// currency is the correct source. Omitted entirely when there is no
	// tier at all — the pre-slice callers see no wire regression because
	// the empty-tier path never emitted a currency to begin with.
	body := map[string]any{
		"seatList": seatList,
	}
	if cur := seatListCurrency(tiers); cur != "" {
		body["currency"] = cur
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, body))
}

// buildGASeatEntry projects one ticket-tier row into a single GA-branch
// seatList entry per spec §7.2. Extracted in feature #476 W1-A2b slice
// 22 so the entry's wire-shape contract (in particular the
// `categoryPriceName` rename from the legacy `categoryName`) can be
// unit-tested without spinning up a live pool or Handler.
//
// The categoryPriceID argument is passed in already-resolved (int64 via
// compatCategoryPriceID on the production path, UUID string on the
// nil-compatDB unit-test path) so this helper stays pure over the row —
// it does not touch the DB and it does not depend on the Handler.
//
// availableCount is emitted ONLY when the tier row has a non-nil
// Capacity; the pre-slice behavior omitted the key for uncapped tiers
// and this slice keeps that contract so uncapped-tier callers see no
// wire regression from the rename.
func buildGASeatEntry(categoryPriceID any, t gen.TicketTierRow, effectivePrice int64) map[string]any {
	entry := map[string]any{
		"categoryPriceId":   categoryPriceID,
		"categoryPriceName": t.Name,
		"price":             effectivePrice,
		"currency":          t.Currency,
		"pricingMode":       t.PricingMode,
	}
	if t.Capacity != nil {
		entry["availableCount"] = *t.Capacity
	}
	return entry
}

// getSeatListUnits is the per-unit GET_SEAT_LIST branch (SEAT-D1,
// extended by AB-51 to GA sessions). It emits one entry per
// session_seats row — assigned seats carry sector/row/number, GA units
// carry empty coordinates exactly like the Bil24 seat-management table —
// joining tier metadata (price/currency) from the session's
// ticket_tiers snapshot.
func (h *Handler) getSeatListUnits(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string, seats []gen.SessionSeatRow) {
	// Load tier snapshot for price / currency projection. When the tier
	// dependency is unwired (nil) or fails, we degrade gracefully with
	// price=0 / currency omitted rather than failing the whole
	// response — seat inventory is still meaningful without prices.
	var tiers []gen.TicketTierRow
	if h.tierQueries != nil {
		var terr error
		tiers, terr = h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
		if terr != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: tier snapshot failed; emitting seats with zero price",
				slog.String("session_id", sessionID.String()),
				slog.String("error", terr.Error()),
			)
			tiers = nil
		}
	}
	tierByID := make(map[uuid.UUID]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}
	// AB-48: scheduled prices via the ONE resolver (base on failure).
	var effPrices map[uuid.UUID]priceresolve.Effective
	if h.tierQueries != nil && len(tiers) > 0 {
		if m, effErr := priceresolve.ForTiers(ctx, h.tierQueries, tiers, time.Now().UTC()); effErr != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: price window lookup failed; using base prices",
				slog.String("error", effErr.Error()))
		} else {
			effPrices = m
		}
	}
	effectiveOf := func(t gen.TicketTierRow) int64 {
		if eff, ok := effPrices[t.ID]; ok {
			return eff.Amount
		}
		return t.PriceAmount
	}

	seatList := make([]map[string]any, 0, len(seats))
	for _, s := range seats {
		entry := map[string]any{
			// Spec §4 / §7.2 (W1-A2b feature #476): seatId on the wire is
			// session_seats.system_seat_id (bigint, migration 0088 /
			// AB-50a). Legacy ADR-005 UUID projection has been retired —
			// callers that need the platform UUID resolve it via
			// compatids on the way back in.
			"seatId": s.SystemSeatID,
			"sector": s.SectorName,
			"row":    s.RowName,
			"number": s.SeatNumber,
			"status": bssStatusCode(s.Status),
		}
		if s.TierID != nil {
			// Spec §4 / §7.2 (feature #476): int64 wire form via compat map.
			entry["categoryPriceId"] = h.compatCategoryPriceID(ctx, *s.TierID)
			if t, ok := tierByID[*s.TierID]; ok {
				entry["price"] = effectiveOf(t)
				entry["currency"] = t.Currency
			} else {
				entry["price"] = int64(0)
			}
		} else {
			entry["price"] = int64(0)
		}
		seatList = append(seatList, entry)
	}

	// Spec §7.2 (feature #476 slice 21): top-level currency mirrors the GA
	// branch. The tier snapshot is best-effort here (a stale/failed load
	// leaves `tiers` empty and we simply omit the key rather than emit an
	// empty string), so pre-slice callers on the unit branch that had no
	// tier snapshot at all still see the same admissionMode+seatList shape.
	body := map[string]any{
		"seatList":      seatList,
		"admissionMode": admissionMode,
	}
	if cur := seatListCurrency(tiers); cur != "" {
		body["currency"] = cur
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, body))
}

// seatListCurrency projects a session's ticket-tier snapshot onto the
// spec §7.2 top-level `currency` key. Every tier of one session shares a
// currency (mixed-currency inserts are rejected at ticket_tier admission)
// so the first non-empty tier currency is the correct value; empty input
// returns "" so callers can OMIT the key rather than emit an empty
// string. Pure over the tier slice — no DB round-trip — so the wire-shape
// contract can be unit-tested without spinning up a live pool.
//
// Feature #476 W1-A2b slice 21 (spec §7.2).
func seatListCurrency(tiers []gen.TicketTierRow) string {
	for _, t := range tiers {
		if t.Currency != "" {
			return t.Currency
		}
	}
	return ""
}

// bssStatusCode maps an internal session_seats.status string to the Bil24
// BSS wire code documented in §6 of the gateway spec:
//
//	unavailable → 0  (admin-withheld)
//	available   → 1
//	held        → 3  (a reservation currently owns the seat)
//	sold        → 4
//
// Any unknown status maps to 0 so legacy clients never see a hole in
// the enum surface.
func bssStatusCode(status string) int {
	switch status {
	case "available":
		return 1
	case "held":
		return 3
	case "sold":
		return 4
	case "unavailable":
		return 0
	default:
		return 0
	}
}
