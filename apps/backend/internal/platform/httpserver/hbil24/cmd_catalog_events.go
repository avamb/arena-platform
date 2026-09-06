// cmd_catalog_events.go — the nested actionEventList / categoryLimitList
// subtree of GET_ALL_ACTIONS (feature #497 W1-B3a, spec §7.1).
//
// Split out of cmd_catalog.go because the session-level projection is a whole
// contract of its own: local-calendar dates, two inventory shapes, the GA /
// seated category split, and the price aggregates. cmd_catalog.go keeps the
// event-level (actionList) body; this file owns everything under it.
//
// Cost discipline (the spec's "no N+1" requirement): the whole subtree — every
// action, every session, every category, across the entire org catalog — is
// built from exactly THREE round-trips:
//
//	ListActionEventsByOrg     — sessions + venue geo + availability
//	ListActionEventTiersByOrg — every live tier of those sessions
//	priceresolve.ForTiers     — one batched price-window resolve
//
// Nothing in here queries per event or per session.
package hbil24

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// catalogAction is the per-event slice of the actionEventList subtree: the
// projected sessions plus the price envelope spanning them.
//
// minPrice / maxPrice are in DB minor units, matching every other money field
// this gateway emits (GET_SEAT_LIST price, GET_CART sum / chargeAmount). The
// WordPress plugin scales once, at the display edge, and it must see ONE
// convention across all commands — so GET_ALL_ACTIONS deliberately does not
// convert to major units here either.
//
// hasPrice distinguishes "no live tier at all" (both bounds stay 0 and the
// action advertises no price) from "a genuinely free tier priced at 0".
type catalogAction struct {
	events   []map[string]any
	minPrice int64
	maxPrice int64
	hasPrice bool
	// posterMediaID is the poster override of the action's FIRST projected
	// (earliest, venue-local) session, spec §7.1 feature #498: "Постеры —
	// публичный URL media_objects постера сеанса с fallback на
	// events.image_url". Sessions arrive ordered by start_at ASC, so the
	// first entry appended to `events` fixes this value; later sessions of
	// the same action never override it — one action, one cover.
	posterMediaID *uuid.UUID
}

// loadActionEvents builds the whole actionEventList subtree for orgID, keyed
// by event id. Callers pass the result into buildActionEntry.
//
// An error here is fatal to the command: an actionList whose entries silently
// lost their sessions looks to the WP plugin like "this action is over", which
// would unpublish live events on the site. handleBil24GetAllActions therefore
// turns it into a -99 envelope rather than degrading.
func (h *Handler) loadActionEvents(
	ctx context.Context, orgID uuid.UUID, feePercent float64,
) (map[uuid.UUID]catalogAction, error) {
	sessions, err := h.eventQueries.ListActionEventsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	tiers, err := h.eventQueries.ListActionEventTiersByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}

	// Feature #498 (W1-B3b): batch-resolve every action_event / venue / city /
	// category_price id up front — one EnsureMany round-trip pair per kind for
	// the WHOLE catalog — instead of the per-session / per-tier compatEnsure
	// calls inside projectActionEvents / projectCategories each doing their
	// own single-row Ensure. See compat_ids.go's cache doc comment for the
	// N+1 story this fixes (100 events x 3 sessions took 1.3s before this).
	sessionIDs := make([]uuid.UUID, 0, len(sessions))
	venueIDs := make([]uuid.UUID, 0, len(sessions))
	cityIDs := make([]uuid.UUID, 0, len(sessions))
	for _, s := range sessions {
		sessionIDs = append(sessionIDs, s.SessionID)
		venueIDs = append(venueIDs, s.VenueID)
		if s.CityID != nil {
			cityIDs = append(cityIDs, *s.CityID)
		}
	}
	tierIDs := make([]uuid.UUID, 0, len(tiers))
	for _, t := range tiers {
		tierIDs = append(tierIDs, t.Tier.ID)
	}
	h.prewarmCompatIDs(ctx, compatids.KindActionEvent, sessionIDs)
	h.prewarmCompatIDs(ctx, compatids.KindVenue, venueIDs)
	h.prewarmCompatIDs(ctx, compatids.KindCity, cityIDs)
	h.prewarmCompatIDs(ctx, compatids.KindCategoryPrice, tierIDs)

	// One batched resolve for every tier in the catalog. priceresolve is the
	// single authority on scheduled price windows (AB-48); reading
	// price_amount directly here would make the catalog disagree with
	// GET_SEAT_LIST the moment a window opens.
	flat := make([]gen.TicketTierRow, 0, len(tiers))
	for _, t := range tiers {
		flat = append(flat, t.Tier)
	}
	prices := make(map[uuid.UUID]int64, len(flat))
	if len(flat) > 0 {
		eff, perr := priceresolve.ForTiers(ctx, h.tierQueries, flat, time.Now().UTC())
		if perr != nil {
			// A window-table hiccup must not blank the catalog: fall back to
			// the tiers' base prices, which is exactly what priceresolve
			// itself returns when a tier has no windows.
			h.logger.Warn("bil24_compat: GET_ALL_ACTIONS: price window resolve failed; using base prices",
				slog.String("org_id", orgID.String()),
				slog.String("error", perr.Error()),
			)
		} else {
			for id, e := range eff {
				prices[id] = e.Amount
			}
		}
		for _, t := range flat {
			if _, ok := prices[t.ID]; !ok {
				prices[t.ID] = t.PriceAmount
			}
		}
	}

	return h.projectActionEvents(ctx, sessions, tiers, prices, feePercent), nil
}

// projectActionEvents is the pure projection half of loadActionEvents: rows in,
// wire maps out, no DB access of its own (compat-id lookups aside, which are
// cached and fall back to UUID strings without a pool). Kept separate so the
// spec §7.1 semantics below are unit-testable from hand-built rows.
func (h *Handler) projectActionEvents(
	ctx context.Context,
	sessions []gen.ActionEventRow,
	tiers []gen.ActionEventTierRow,
	prices map[uuid.UUID]int64,
	feePercent float64,
) map[uuid.UUID]catalogAction {
	bySession := make(map[uuid.UUID][]gen.ActionEventTierRow, len(sessions))
	for _, t := range tiers {
		bySession[t.Tier.SessionID] = append(bySession[t.Tier.SessionID], t)
	}

	out := make(map[uuid.UUID]catalogAction, len(sessions))
	for _, s := range sessions {
		loc := h.venueLocation(s)
		if loc == nil {
			// Spec §7.1: a session whose venue has no timezone cannot be given
			// a local calendar day, and a WRONG day is worse than a missing
			// session — the buyer would show up on the wrong evening. Drop it
			// and warn so operations can fix the venue.
			continue
		}
		local := s.StartAt.In(loc)

		entry := map[string]any{
			"actionEventId": h.compatActionEventID(ctx, s.SessionID),
			"venueId":       h.compatVenueID(ctx, s.VenueID),
			// Spec §7.1: day is the LOCAL calendar day at the venue.
			"day": local.Format("02.01.2006"), // allow:timeformat: spec §7.1 DD.MM.YYYY
			// allow:timeformat: spec §7.1 HH:MM, local wall clock
			"time":     local.Format("15:04"),
			"currency": s.Currency,
			// eTicket is unconditionally true: arena has no paper-only
			// fulfilment, every ticket is a barcode the buyer can print.
			"eTicket": true,
			// chargePercent is an int on the legacy wire, so the channel fee
			// is truncated here exactly as GET_CART truncates it.
			"chargePercent":  int64(feePercent),
			"tariffPlanList": []any{},
		}

		// cityId is 0 when the venue has no city reference — the plugin treats
		// 0 as "ungrouped" and the key must still be present, because absent
		// keys break its array access.
		if s.CityID != nil {
			entry["cityId"] = h.compatCityID(ctx, *s.CityID)
		} else {
			entry["cityId"] = int64(0)
		}

		// sellEndTime — spec §7.1: the earliest tier sale-window end, falling
		// back to the session start when no tier bounds its window. Rendered
		// RFC3339 *in the venue's zone* so the offset the site parses is the
		// one a local buyer experiences.
		sellEnd := s.StartAt
		if s.SellEndAt != nil {
			sellEnd = *s.SellEndAt
		}
		entry["sellEndTime"] = sellEnd.In(loc).Format(time.RFC3339)

		// seatingPlanId — spec §7.1: the plan is addressed by the SESSION, not
		// by the plan row, because GET_SCHEMA takes an actionEventId. Pure GA
		// sessions have no plan and report 0, which is how the plugin decides
		// whether to render a seat map at all.
		if s.AdmissionMode == "assigned_seats" || s.AdmissionMode == "hybrid" {
			entry["seatingPlanId"] = entry["actionEventId"]
			if s.SeatingPlanName != nil && *s.SeatingPlanName != "" {
				entry["seatingPlanName"] = *s.SeatingPlanName
			}
		} else {
			entry["seatingPlanId"] = int64(0)
		}

		sessionAvail := sessionAvailability(s)
		entry["availability"] = sessionAvail

		catList, minPrice, maxPrice, hasPrice := h.projectCategories(
			ctx, bySession[s.SessionID], prices, sessionAvail,
		)
		// categoryLimitList is [] — not [{categoryList: []}] — when the
		// session sells no GA places. That emptiness is load-bearing: it is
		// how bil24-acf-sync.php tells a pure-seating event from a combined
		// one (bil24-acf-sync.php:434-446).
		if len(catList) > 0 {
			entry["categoryLimitList"] = []map[string]any{{"categoryList": catList}}
		} else {
			entry["categoryLimitList"] = []map[string]any{}
		}
		entry["minPrice"] = minPrice

		acc := out[s.EventID]
		if len(acc.events) == 0 {
			// First (earliest) session of this action: fixes the action's
			// cover per spec §7.1. s.PosterMediaID is the session's OWN
			// override; buildActionEntry falls further back to the event's
			// poster / image_url when it is nil.
			acc.posterMediaID = s.PosterMediaID
		}
		acc.events = append(acc.events, entry)
		if hasPrice {
			if !acc.hasPrice || minPrice < acc.minPrice {
				acc.minPrice = minPrice
			}
			if !acc.hasPrice || maxPrice > acc.maxPrice {
				acc.maxPrice = maxPrice
			}
			acc.hasPrice = true
		}
		out[s.EventID] = acc
	}
	return out
}

// projectCategories builds the GA-only categoryList of one session and, as a
// side product, the price envelope over ALL of its live tiers.
//
// The two differ on purpose (spec §7.1): categories describe what can be bought
// without picking a seat, while minPrice/maxPrice describe the whole action —
// a pure-seating event exposes no categories yet must still advertise a "from"
// price, otherwise the site renders it as free.
func (h *Handler) projectCategories(
	ctx context.Context,
	tiers []gen.ActionEventTierRow,
	prices map[uuid.UUID]int64,
	sessionAvail int,
) (catList []map[string]any, minPrice, maxPrice int64, hasPrice bool) {
	catList = make([]map[string]any, 0, len(tiers))
	for _, t := range tiers {
		price, ok := prices[t.Tier.ID]
		if !ok {
			price = t.Tier.PriceAmount
		}
		if !hasPrice || price < minPrice {
			minPrice = price
		}
		if !hasPrice || price > maxPrice {
			maxPrice = price
		}
		hasPrice = true

		if !t.IsGA {
			continue
		}
		// A GA tier with its OWN materialised units reports their free count.
		// A tier with none is not "sold out" — the session's GA pool is
		// commonly materialised with tier_id NULL (or tracked purely in the
		// ledger), so the session-level remaining count is the honest answer.
		avail := sessionAvail
		if t.GAUnitsTotal > 0 {
			avail = int(t.GAUnitsAvailable)
		}
		catList = append(catList, map[string]any{
			"categoryPriceId":   h.compatCategoryPriceID(ctx, t.Tier.ID),
			"categoryPriceName": t.Tier.Name,
			// placement=false marks the row as "no seat to choose". Seated
			// tiers never reach here, so the key is a constant.
			"placement":    false,
			"price":        price,
			"availability": avail,
			// arena has no tariff plans in wave 1; the site synthesises a
			// default variation when the map is empty. The KEY must exist.
			"tariffIdMap": map[string]any{},
		})
	}
	return catList, minPrice, maxPrice, hasPrice
}

// sessionAvailability picks the right inventory shape (spec §7.1). A session
// with a materialised session_seats pool — assigned seats and/or ga_units —
// counts free rows; one without falls back to the ledger's
// capacity_total − sold − held. Negative results (an oversold ledger) clamp to
// 0: the wire has no way to express "less than nothing" and the site would
// render a negative count verbatim.
func sessionAvailability(s gen.ActionEventRow) int {
	var n int32
	if s.SeatsTotal > 0 {
		n = s.SeatsAvailable
	} else {
		n = s.LedgerAvailable
	}
	if n < 0 {
		return 0
	}
	return int(n)
}

// venueLocation resolves the venue's IANA zone, returning nil (and warning)
// when it is missing or unloadable. Both cases are spec §7.1's "skip the
// session" path — an unknown zone is indistinguishable from no zone as far as
// producing a correct local day goes.
func (h *Handler) venueLocation(s gen.ActionEventRow) *time.Location {
	if s.Timezone == nil || *s.Timezone == "" {
		h.logger.Warn("bil24.venue_timezone_missing",
			slog.String("venue_id", s.VenueID.String()),
			slog.String("venue_name", s.VenueName),
			slog.String("session_id", s.SessionID.String()),
		)
		return nil
	}
	loc, err := time.LoadLocation(*s.Timezone)
	if err != nil {
		h.logger.Warn("bil24.venue_timezone_missing",
			slog.String("venue_id", s.VenueID.String()),
			slog.String("venue_name", s.VenueName),
			slog.String("session_id", s.SessionID.String()),
			slog.String("timezone", *s.Timezone),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return loc
}
