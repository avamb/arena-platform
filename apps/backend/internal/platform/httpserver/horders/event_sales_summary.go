package horders

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The batch sales summary (WEB-LIST, spec 40 task 0-1) gives the list of
// events its sales figures in ONE request: for every id it answers the same
// numbers the event summary shows at its top level (the money per currency,
// orders, tickets, door count, refunds, promo codes, invitations, places and
// the session count) and leaves out the per-category table and the
// per-session list, which only the event's own screen needs.
//
//	GET /v1/organizations/{org_id}/events/sales-summary?ids=<uuid>,<uuid>   (order.read)
//
// It runs the batch form of the event summary queries once over the sessions
// of every requested event (never one round trip per event) and folds them
// with buildSummaryCore, the assembly behind the event and session summaries,
// so the three cannot drift. An event of another organization, a deleted one
// and an unknown id are simply absent from the answer.

// maxSalesSummaryIDs is the largest batch one request may name.
const maxSalesSummaryIDs = 50

// eventSalesSummary is one event's entry: summaryEvent plus the top level of
// eventSummary without its tiers and sessions.
type eventSalesSummary struct {
	Event         summaryEvent         `json:"event"`
	Places        summaryPlaces        `json:"places"`
	Money         []summaryMoney       `json:"money"`
	Orders        []summaryOrders      `json:"orders"`
	Tickets       summaryTickets       `json:"tickets"`
	Entered       summaryEntered       `json:"entered"`
	Refunds       []summaryRefunds     `json:"refunds"`
	Promos        []summaryPromo       `json:"promos"`
	Complimentary summaryComplimentary `json:"complimentary"`
}

type eventSalesSummaryList struct {
	Items []eventSalesSummary `json:"items"`
}

// parseSalesSummaryIDs reads the `ids` query parameter: comma separated
// UUIDs, at most maxSalesSummaryIDs of them. It returns the error code and
// message for a refusal.
func parseSalesSummaryIDs(raw string) ([]uuid.UUID, string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "events.ids_required", "query parameter ids is required: one or more event ids separated by commas"
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxSalesSummaryIDs {
		return nil, "events.too_many_ids", "at most 50 event ids can be requested at once"
	}
	ids := make([]uuid.UUID, 0, len(parts))
	seen := make(map[uuid.UUID]struct{}, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			return nil, "events.invalid_ids", "every id in ids must be a UUID"
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, "", ""
}

// buildEventSalesSummaries folds the raw aggregates of a batch of events into
// one entry per event found, in the order the caller asked for them. Pure.
func buildEventSalesSummaries(requested []uuid.UUID, headers []gen.EventSummaryHeaderRow, sessionIDs []gen.EventSummarySessionIDRow, rows summaryRows) []eventSalesSummary {
	byID := make(map[uuid.UUID]gen.EventSummaryHeaderRow, len(headers))
	for _, h := range headers {
		byID[h.ID] = h
	}
	sessionsOf := make(map[uuid.UUID][]uuid.UUID, len(headers))
	for _, s := range sessionIDs {
		sessionsOf[s.EventID] = append(sessionsOf[s.EventID], s.SessionID)
	}
	bySession := rows.groupBySession()

	out := make([]eventSalesSummary, 0, len(headers))
	for _, id := range requested {
		h, ok := byID[id]
		if !ok {
			continue
		}
		var eventRows summaryRows
		for _, sid := range sessionsOf[id] {
			eventRows.append(bySession[sid])
		}
		total := buildSummaryCore(eventRows)
		out = append(out, eventSalesSummary{
			Event: summaryEvent{
				ID:             h.ID.String(),
				OrgID:          h.OrgID.String(),
				Name:           h.Name,
				Status:         h.Status,
				FirstSessionAt: rfc3339Ptr(h.FirstSessionAt),
				LastSessionAt:  rfc3339Ptr(h.LastSessionAt),
				SessionCount:   len(sessionsOf[id]),
			},
			Places:        total.Places,
			Money:         total.Money,
			Orders:        total.Orders,
			Tickets:       total.Tickets,
			Entered:       total.Entered,
			Refunds:       total.Refunds,
			Promos:        total.Promos,
			Complimentary: total.Complimentary,
		})
	}
	return out
}

// HandleEventSalesSummaries serves
// GET /v1/organizations/{org_id}/events/sales-summary?ids=...
func (h *Handler) HandleEventSalesSummaries(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	requested, code, msg := parseSalesSummaryIDs(r.URL.Query().Get("ids"))
	if code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	ctx := r.Context()

	headers, err := h.queries.ListEventSummaryHeaders(ctx, requested, orgID)
	if err != nil {
		h.summaryFailure(w, r, "event_batch", "headers", err)
		return
	}
	found := make([]uuid.UUID, 0, len(headers))
	for _, hd := range headers {
		found = append(found, hd.ID)
	}
	var (
		sessionIDs []gen.EventSummarySessionIDRow
		rows       summaryRows
	)
	if len(found) > 0 {
		sessionIDs, err = h.queries.ListEventSummarySessionIDs(ctx, found, orgID)
		if err != nil {
			h.summaryFailure(w, r, "event_batch", "sessions", err)
			return
		}
		all := make([]uuid.UUID, 0, len(sessionIDs))
		for _, s := range sessionIDs {
			all = append(all, s.SessionID)
		}
		var part string
		rows, part, err = h.loadSummaryRowsNoTiers(ctx, all)
		if err != nil {
			h.summaryFailure(w, r, "event_batch", part, err)
			return
		}
	}
	httputil.WriteJSON(w, http.StatusOK,
		eventSalesSummaryList{Items: buildEventSalesSummaries(requested, headers, sessionIDs, rows)})
}
