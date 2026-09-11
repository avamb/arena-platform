// cmd_seat_list.go — Bil24-compatible GET_SEAT_LIST handler (spec §7.2).
//
// Feature #499 (W1-B4) replaced the two divergent legacy branches (a GA
// "tier facade" that emitted tiers *as* seats, and a per-unit branch that
// emitted BSS status codes) with ONE projection: the spec §7.2
// GetSeatListResponse — a `categoryList` describing every ticket tier and
// a `seatList` describing every ticketable place. The seat-status enum,
// the `admissionMode` echo and the `pricingMode`/`availableCount` tier
// keys are gone from the wire; nothing in the WordPress plugin read them.
//
// The dispatcher (HandleBil24Command in bil24_compat.go) stays the single
// central case-list — this file only owns the seat-list projection.
package hbil24

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// admissionGA is the sessions.admission_mode value for a session with no
// seating plan at all. Mirrored here rather than imported so the
// projection stays readable at its branch points; the other two values
// ("assigned_seats", "hybrid") are only ever tested as "not this one".
const admissionGA = "general_admission"

// seatKindGAUnit is the session_seats.kind value for a general-admission
// unit — a ticketable place with no coordinates. The other value is
// "seat".
const seatKindGAUnit = "ga_unit"

// tierUnitStats accumulates what the session_seats snapshot says about one
// ticket tier: how many rows of each kind it owns and how many of them are
// currently sellable. Feature #499 (spec §7.2) derives both the category
// `availability` count and the tri-state `placement` flag from it.
type tierUnitStats struct {
	seats     int
	gaUnits   int
	available int
}

// handleBil24GetSeatList answers GET_SEAT_LIST with the spec §7.2
// response: the session currency, the full category (ticket-tier) list and
// the seat list.
//
//	{
//	  "resultCode": 0, "description": "OK", "command": "GET_SEAT_LIST",
//	  "currency": "CZK",
//	  "categoryList": [
//	    {"categoryPriceId": 1000000020, "categoryPriceName": "Parter",
//	     "price": 900, "availability": 84, "placement": true,
//	     "tariffIdMap": {}}
//	  ],
//	  "seatList": [
//	    {"seatId": 1731, "categoryPriceId": 1000000020, "tariffPlanId": null,
//	     "price": 900, "available": true,
//	     "location": {"sector": "Parter", "row": "3", "number": "12"}}
//	  ]
//	}
//
// Shape rules (all from spec §7.2):
//
//   - `placement` is TRI-STATE. A seated tier is `true`; a
//     general-admission tier that lives inside a seating plan (hybrid
//     session) is `false`; on a pure-GA session the key is ABSENT
//     entirely, because "placement" is only meaningful where a plan
//     exists. Modelled as *bool with `omitempty`.
//
//   - `seatList` carries every kind='seat' row on an assigned_seats
//     session; on a hybrid session it ALSO carries the kind='ga_unit'
//     rows as pseudo-seats, whose location is {sector: <tier name>,
//     row: "", number: ""} so the WordPress renderer can group them; on a
//     pure-GA session it is the empty array (there is nothing to place).
//
//   - `availableOnly:true` filters `seatList` ONLY. `categoryList` always
//     describes the complete category set so a sold-out category still
//     renders.
//
//   - `tariffPlanId` is always null and `tariffIdMap` always {} — arena
//     has no tariff-plan concept, but the keys are part of the contract
//     the plugin destructures.
//
// A session outside the calling channel's organization answers -3.
//
// Operator note: stadium-scale seat maps can push the seatList payload
// past 1 MiB. Enable gzip on the reverse proxy fronting POST
// /compat/bil24/json (nginx: gzip_types application/json; Cloudflare:
// Auto-Minify JSON + Brotli; Caddy: encode zstd gzip) so callers with
// Accept-Encoding: gzip receive a compressed response and the wire foot-
// print stays predictable.
func (h *Handler) handleBil24GetSeatList(w http.ResponseWriter, r *http.Request, req bil24Request) {
	// tier and seat services can be independently unwired; the outer
	// guard fails fast only if BOTH are missing (no data source at all).
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

	// Resolve admission_mode when the seating dependency is wired. A
	// missing dependency or a lookup failure degrades to general_admission
	// — the most conservative shape (empty seatList, no placement key).
	admissionMode := admissionGA
	if h.admissionQ != nil {
		if row, aerr := h.admissionQ.GetSessionAdmissionModeByID(ctx, sessionID); aerr == nil && row.AdmissionMode != "" {
			admissionMode = row.AdmissionMode
		}
	}

	// The unit snapshot drives BOTH halves of the response: seatList rows
	// come straight from it, and each category's `availability` is a count
	// over it. ListSessionSeatsAdmin (rather than ListSessionSeats) is the
	// read because only it carries session_seats.kind, and the hybrid
	// branch must tell a seat from a GA unit.
	var units []gen.SessionSeatAdminRow
	if h.seatQ != nil {
		rows, serr := h.seatQ.ListSessionSeatsAdmin(ctx, sessionID)
		if serr != nil {
			// On a placed session the seat map IS the response; failing
			// loudly beats emitting a plausible-looking empty hall.
			if admissionMode != admissionGA {
				h.logger.Error("bil24_compat: GET_SEAT_LIST: list session seats failed",
					slog.String("session_id", sessionID.String()),
					slog.String("error", serr.Error()),
				)
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeInternalError, "failed to retrieve seat list",
				))
				return
			}
		} else {
			units = rows
		}
	}

	// Without tier data there is no categoryList to build. That is fatal
	// unless the unit snapshot alone can still carry the response (a
	// placed session with an unwired tier facade degrades to priced-at-zero
	// seats rather than a hard failure).
	if h.tierQueries == nil && len(units) == 0 {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "tier service unavailable",
		))
		return
	}

	tiers, ok := h.seatListTiers(w, ctx, req, sessionID, len(units))
	if !ok {
		return
	}

	priceOf := h.seatListPricer(ctx, sessionID, tiers)
	stats := seatListUnitStats(units)

	resp := bil24compat.GetSeatListResponse{
		Currency:     seatListCurrency(tiers),
		CategoryList: h.buildSeatListCategories(ctx, sessionID, admissionMode, tiers, stats, priceOf),
		SeatList:     h.buildSeatList(ctx, admissionMode, units, tiers, priceOf, req.AvailableOnly),
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"currency":     resp.Currency,
		"categoryList": resp.CategoryList,
		"seatList":     resp.SeatList,
	}))
}

// seatListTiers loads the session's ticket-tier snapshot. A load failure is
// fatal only when the unit snapshot is also empty — otherwise the response
// degrades to seats priced at zero with an empty categoryList, which is
// strictly more useful to the caller than -99. It writes the error envelope
// itself and reports ok=false when it does.
func (h *Handler) seatListTiers(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, unitCount int) ([]gen.TicketTierRow, bool) {
	if h.tierQueries == nil {
		return nil, true
	}
	tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err == nil {
		return tiers, true
	}
	if unitCount == 0 {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: list tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat list",
		))
		return nil, false
	}
	h.logger.Warn("bil24_compat: GET_SEAT_LIST: tier snapshot failed; emitting seats with zero price",
		slog.String("session_id", sessionID.String()),
		slog.String("error", err.Error()),
	)
	return nil, true
}

// seatListPricer resolves the AB-48 scheduled price for every tier once and
// returns a lookup closure over the result. A resolver failure degrades to
// the tiers' base price_amount rather than failing the command — a stale
// price is recoverable, a -99 on the seat map is not.
//
// The returned amount is the raw minor-currency unit (cents), which is the
// money convention the Bil24 wire uses everywhere in this gateway.
func (h *Handler) seatListPricer(ctx context.Context, sessionID uuid.UUID, tiers []gen.TicketTierRow) func(gen.TicketTierRow) int64 {
	var eff map[uuid.UUID]priceresolve.Effective
	if h.tierQueries != nil && len(tiers) > 0 {
		m, err := priceresolve.ForTiers(ctx, h.tierQueries, tiers, time.Now().UTC())
		if err != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: price window lookup failed; using base prices",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
		} else {
			eff = m
		}
	}
	return func(t gen.TicketTierRow) int64 {
		if e, ok := eff[t.ID]; ok {
			return e.Amount
		}
		return t.PriceAmount
	}
}

// seatListUnitStats folds the session_seats snapshot into per-tier counts.
// Units with no tier binding are bucketed under uuid.Nil: a GA pool is very
// commonly materialised with tier_id NULL, and that pool is the session-level
// fallback every tier without units of its own reports (see
// seatListAvailability). A real tier id is never the nil UUID, so the two
// namespaces cannot collide.
func seatListUnitStats(units []gen.SessionSeatAdminRow) map[uuid.UUID]tierUnitStats {
	out := make(map[uuid.UUID]tierUnitStats, len(units))
	for _, u := range units {
		key := uuid.Nil
		if u.TierID != nil {
			key = *u.TierID
		}
		st := out[key]
		if u.Kind == seatKindGAUnit {
			st.gaUnits++
		} else {
			st.seats++
		}
		if u.Status == "available" {
			st.available++
		}
		out[key] = st
	}
	return out
}

// buildSeatListCategories projects the tier snapshot onto spec §7.2's
// `categoryList`. Every tier appears, sold out or not — `availableOnly`
// deliberately does not reach this list (see handleBil24GetSeatList).
func (h *Handler) buildSeatListCategories(
	ctx context.Context,
	sessionID uuid.UUID,
	admissionMode string,
	tiers []gen.TicketTierRow,
	stats map[uuid.UUID]tierUnitStats,
	priceOf func(gen.TicketTierRow) int64,
) []bil24compat.GetSeatListCategory {
	ledgers := h.seatListLedgers(ctx, sessionID, tiers, stats)

	out := make([]bil24compat.GetSeatListCategory, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, bil24compat.GetSeatListCategory{
			CategoryPriceID:   h.compatCategoryPriceIDInt(ctx, t.ID),
			CategoryPriceName: t.Name,
			// Spec 20 §3: major units on the wire (priceOf is minor).
			Price:        money.Major(priceOf(t)),
			Availability: seatListAvailability(t, stats, ledgers),
			Placement:    seatListPlacement(admissionMode, stats[t.ID]),
			TariffIDMap:  map[string]any{},
		})
	}
	return out
}

// seatListLedgers loads the inventory ledger only when it is actually
// needed: a session whose every tier has materialised unit rows counts its
// availability off those rows and never touches the ledger. Returns nil on
// any failure — seatListAvailability then falls back to the tier capacity.
//
// The session-level row (tier_id NULL) is KEPT, bucketed under uuid.Nil: it
// is the fallback a tier without inventory of its own reports, exactly as
// GET_ALL_ACTIONS does (cmd_catalog_events.go sessionAvailability).
func (h *Handler) seatListLedgers(ctx context.Context, sessionID uuid.UUID, tiers []gen.TicketTierRow, stats map[uuid.UUID]tierUnitStats) map[uuid.UUID]gen.InventoryLedgerRow {
	if h.tierQueries == nil {
		return nil
	}
	needed := false
	for _, t := range tiers {
		if _, ok := stats[t.ID]; !ok {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	rows, err := h.tierQueries.ListInventoryLedgersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Warn("bil24_compat: GET_SEAT_LIST: inventory ledger lookup failed; using tier capacity",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}
	out := make(map[uuid.UUID]gen.InventoryLedgerRow, len(rows))
	for _, r := range rows {
		key := uuid.Nil
		if r.TierID != nil {
			key = *r.TierID
		}
		out[key] = r
	}
	return out
}

// seatListAvailability computes spec §7.2's per-category `availability`
// (how many tickets remain sellable in this category), in precedence order:
//
//  1. the count of available session_seats rows bound to the tier, when the
//     tier has materialised units — the seat map is the truth for placed
//     inventory;
//  2. otherwise the tier's own inventory ledger row: capacity_total − sold
//     − held, which is what a general-admission tier without unit rows
//     actually has left;
//  3. otherwise the session-level GA unit pool (session_seats rows with
//     tier_id NULL). A GA pool is very commonly materialised unbound, and
//     every tier of that session sells out of it;
//  4. otherwise the session-level ledger row (tier_id NULL) — spec §7.2's
//     "для безлимитного GA без юнитов — capacity − sold − held";
//  5. otherwise the tier's own declared capacity;
//  6. otherwise 0 (an uncapped tier with no ledger row has nothing
//     countable to report).
//
// Steps 3–4 are the same rule GET_ALL_ACTIONS applies (cmd_catalog_events.go
// projectCategories): a tier with no inventory of its own is NOT "sold out",
// the session-level remaining count is the honest answer. Emitting 0 here
// while the sibling command reports 50 for the same fixture would be an
// outright contradiction on the wire.
//
// The result is clamped at zero: an oversold ledger must not surface as a
// negative count, which legacy clients render as garbage.
func seatListAvailability(t gen.TicketTierRow, stats map[uuid.UUID]tierUnitStats, ledgers map[uuid.UUID]gen.InventoryLedgerRow) int {
	if st, ok := stats[t.ID]; ok {
		return clampNonNegative(st.available)
	}
	if l, ok := ledgers[t.ID]; ok && l.CapacityTotal != nil {
		return ledgerRemaining(l)
	}
	if st, ok := stats[uuid.Nil]; ok {
		return clampNonNegative(st.available)
	}
	if l, ok := ledgers[uuid.Nil]; ok && l.CapacityTotal != nil {
		return ledgerRemaining(l)
	}
	if t.Capacity != nil {
		return clampNonNegative(int(*t.Capacity))
	}
	return 0
}

// ledgerRemaining is capacity_total − sold − held, floored at zero.
func ledgerRemaining(l gen.InventoryLedgerRow) int {
	return clampNonNegative(int(*l.CapacityTotal) - int(l.CapacitySold) - int(l.CapacityHeld))
}

// clampNonNegative floors n at zero.
func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// seatListPlacement computes spec §7.2's TRI-STATE `placement` flag:
//
//   - nil (key omitted) on a pure general-admission session — there is no
//     seating plan, so "is this category placed?" has no answer;
//   - false for a category whose units are all GA units inside a plan
//     (the standing-room tier of a hybrid session);
//   - true otherwise — a seated category, and by default any category of a
//     placed session that has not materialised units yet.
func seatListPlacement(admissionMode string, st tierUnitStats) *bool {
	if admissionMode == admissionGA {
		return nil
	}
	placed := st.gaUnits == 0 || st.seats > 0
	return &placed
}

// buildSeatList projects the session_seats snapshot onto spec §7.2's
// `seatList`.
//
// A pure general-admission session returns the empty array regardless of
// what the snapshot holds: GA units are counted in categoryList
// availability, not placed on a map. On a placed session (assigned_seats or
// hybrid) every unit is emitted, GA units as pseudo-seats sectored by their
// tier name.
//
// availableOnly drops non-available rows HERE and nowhere else.
func (h *Handler) buildSeatList(
	ctx context.Context,
	admissionMode string,
	units []gen.SessionSeatAdminRow,
	tiers []gen.TicketTierRow,
	priceOf func(gen.TicketTierRow) int64,
	availableOnly bool,
) []bil24compat.GetSeatListSeat {
	if admissionMode == admissionGA {
		return []bil24compat.GetSeatListSeat{}
	}
	tierByID := make(map[uuid.UUID]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}

	out := make([]bil24compat.GetSeatListSeat, 0, len(units))
	for _, u := range units {
		available := u.Status == "available"
		if availableOnly && !available {
			continue
		}
		seat := bil24compat.GetSeatListSeat{
			// Spec §4: seatId on the wire is
			// session_seats.system_seat_id (bigint, migration 0088 /
			// AB-50a), never the platform UUID.
			SeatID:    u.SystemSeatID,
			Available: available,
			// arena has no tariff-plan concept; the key is part of the
			// contract and is always null.
			TariffPlanID: nil,
			Location: bil24compat.GetSeatListLocation{
				Sector: u.SectorName,
				Row:    u.RowName,
				Number: u.SeatNumber,
			},
		}
		if u.TierID != nil {
			seat.CategoryPriceID = h.compatCategoryPriceIDInt(ctx, *u.TierID)
			if t, ok := tierByID[*u.TierID]; ok {
				seat.Price = money.Major(priceOf(t))
				if u.Kind == seatKindGAUnit {
					// A GA unit has no coordinates of its own; spec §7.2
					// sectors it by its category so the plugin can group
					// the standing-room block next to the seated ones.
					seat.Location.Sector = t.Name
				}
			}
		}
		out = append(out, seat)
	}
	return out
}

// seatListCurrency projects a session's ticket-tier snapshot onto the
// spec §7.2 top-level `currency` key. Every tier of one session shares a
// currency (mixed-currency inserts are rejected at ticket_tier admission)
// so the first non-empty tier currency is the correct value; empty input
// returns "". Pure over the tier slice — no DB round-trip — so the
// wire-shape contract can be unit-tested without spinning up a live pool.
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
