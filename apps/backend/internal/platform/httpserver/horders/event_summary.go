package horders

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The event summary (EC-06, spec 35 §5.7) is the session summary over ALL
// sessions of an event: the same places, categories, money, orders, tickets,
// refunds, promo codes and invitations as totals, plus one entry per session
// with its own figures, so a multi-session event reads as totals + a list.
// Both screens are built by summary_core.go and cannot drift.
//
//	GET /v1/organizations/{org_id}/events/{event_id}/summary   (order.read)

type summaryEvent struct {
	ID             string  `json:"id"`
	OrgID          string  `json:"org_id"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	FirstSessionAt *string `json:"first_session_at"`
	LastSessionAt  *string `json:"last_session_at"`
	SessionCount   int     `json:"session_count"`
}

// eventSummaryTier is one category of the event table: the categories of
// every session that share a name, a currency and a list price are merged,
// with their places and revenue summed. Sessions says how many sessions the
// row stands for; the per-session entries keep each category's own id.
type eventSummaryTier struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	PriceAmount int64       `json:"price_amount"`
	Currency    string      `json:"currency"`
	IsOpen      bool        `json:"is_open"`
	Sessions    int         `json:"sessions"`
	Places      placeCounts `json:"places"`
	PaidItems   int64       `json:"paid_items"`
	PaidRevenue int64       `json:"paid_revenue"`
}

type eventSummarySessionHeader struct {
	ID             string  `json:"id"`
	StartAt        string  `json:"start_at"`
	EndAt          string  `json:"end_at"`
	Status         string  `json:"status"`
	CapacityTotal  int32   `json:"capacity_total"`
	HasSeatingPlan bool    `json:"has_seating_plan"`
	VenueName      *string `json:"venue_name"`
	VenueTimezone  *string `json:"venue_timezone"`
}

// eventSummarySession is one session of the event with the SAME figures the
// session summary shows for it.
type eventSummarySession struct {
	eventSummarySessionHeader
	summaryCore
}

type eventSummary struct {
	Event         summaryEvent          `json:"event"`
	Places        summaryPlaces         `json:"places"`
	Tiers         []eventSummaryTier    `json:"tiers"`
	Money         []summaryMoney        `json:"money"`
	Orders        []summaryOrders       `json:"orders"`
	Tickets       summaryTickets        `json:"tickets"`
	Entered       summaryEntered        `json:"entered"`
	Refunds       []summaryRefunds      `json:"refunds"`
	Promos        []summaryPromo        `json:"promos"`
	Complimentary summaryComplimentary  `json:"complimentary"`
	Sessions      []eventSummarySession `json:"sessions"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// buildEventSummary folds the raw aggregates of every session of an event:
// once over all of them for the totals, once per session for the list. Pure.
func buildEventSummary(header gen.EventSummaryHeaderRow, sessions []gen.EventSummarySessionRow, rows summaryRows) eventSummary {
	total := buildSummaryCore(rows)
	out := eventSummary{
		Event: summaryEvent{
			ID:             header.ID.String(),
			OrgID:          header.OrgID.String(),
			Name:           header.Name,
			Status:         header.Status,
			FirstSessionAt: rfc3339Ptr(header.FirstSessionAt),
			LastSessionAt:  rfc3339Ptr(header.LastSessionAt),
			SessionCount:   len(sessions),
		},
		Places:        total.Places,
		Tiers:         mergeEventTiers(total.Tiers),
		Money:         total.Money,
		Orders:        total.Orders,
		Tickets:       total.Tickets,
		Entered:       total.Entered,
		Refunds:       total.Refunds,
		Promos:        total.Promos,
		Complimentary: total.Complimentary,
		Sessions:      make([]eventSummarySession, 0, len(sessions)),
	}
	for _, s := range sessions {
		out.Sessions = append(out.Sessions, eventSummarySession{
			eventSummarySessionHeader: eventSummarySessionHeader{
				ID:             s.ID.String(),
				StartAt:        s.StartAt.UTC().Format(time.RFC3339),
				EndAt:          s.EndAt.UTC().Format(time.RFC3339),
				Status:         s.Status,
				CapacityTotal:  s.CapacityTotal,
				HasSeatingPlan: s.SeatingPlanVersionID != nil,
				VenueName:      s.VenueName,
				VenueTimezone:  s.VenueTimezone,
			},
			summaryCore: buildSummaryCore(rows.forSession(s.ID)),
		})
	}
	return out
}

// mergeEventTiers folds the per-session categories into the event table,
// keyed by name, currency, list price and kind. A category whose price
// differs between two sessions is two rows — the table never averages.
func mergeEventTiers(tiers []summaryTier) []eventSummaryTier {
	type key struct {
		name, currency, kind string
		price                int64
	}
	type nameKey struct{ name, currency string }
	groups := map[key]*eventSummaryTier{}
	order := []key{}
	// The first session's display order names the rows; a second price of
	// the same category sits right after the first one.
	firstSeen := map[nameKey]int{}
	for _, t := range tiers {
		k := key{t.Name, t.Currency, t.Kind, t.PriceAmount}
		g := groups[k]
		if g == nil {
			g = &eventSummaryTier{Name: t.Name, Kind: t.Kind, PriceAmount: t.PriceAmount, Currency: t.Currency}
			groups[k] = g
			order = append(order, k)
			if _, seen := firstSeen[nameKey{t.Name, t.Currency}]; !seen {
				firstSeen[nameKey{t.Name, t.Currency}] = len(firstSeen)
			}
		}
		g.Sessions++
		g.IsOpen = g.IsOpen || t.IsOpen
		g.Places.addCounts(t.Places)
		g.PaidItems += t.PaidItems
		g.PaidRevenue += t.PaidRevenue
	}
	out := make([]eventSummaryTier, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := firstSeen[nameKey{out[i].Name, out[i].Currency}], firstSeen[nameKey{out[j].Name, out[j].Currency}]
		if a != b {
			return a < b
		}
		return out[i].PriceAmount < out[j].PriceAmount
	})
	return out
}

// HandleEventSummary serves
// GET /v1/organizations/{org_id}/events/{event_id}/summary. An event of
// another organization is indistinguishable from a missing one (404).
func (h *Handler) HandleEventSummary(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	ctx := r.Context()

	header, err := h.queries.GetEventSummaryHeader(ctx, eventID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("event.not_found", "event not found", r))
		return
	}
	if err != nil {
		h.summaryFailure(w, r, "event", "header", err)
		return
	}
	sessions, err := h.queries.ListEventSummarySessions(ctx, eventID)
	if err != nil {
		h.summaryFailure(w, r, "event", "sessions", err)
		return
	}
	ids := make([]uuid.UUID, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	rows, part, err := h.loadSummaryRows(ctx, ids)
	if err != nil {
		h.summaryFailure(w, r, "event", part, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, buildEventSummary(header, sessions, rows))
}
