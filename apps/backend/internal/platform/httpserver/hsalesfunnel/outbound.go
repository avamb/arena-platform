package hsalesfunnel

// outbound.go — spec 63, L-10: the organizer's "Clicks to box offices" block,
//
//	GET /v1/organizations/{org_id}/events/{event_id}/outbound-clicks?from=&to=   (report.read)
//
// The clicks come from the static site's track route (outbound_clicks_daily);
// the visits are the site's shown-event counter, so the share of visitors who
// went on to a box office can be read. A bot never reaches either table.

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// maxSellers caps the list of box offices; the biggest come first.
const maxSellers = 50

// DayClicks is the clicks of one day.
type DayClicks struct {
	Day    string `json:"day"`
	Clicks int64  `json:"clicks"`
}

// SellerClicks is the clicks on one box office.
type SellerClicks struct {
	Seller string `json:"seller"`
	Clicks int64  `json:"clicks"`
}

// SourceClicks is the clicks that came from one utm source.
type SourceClicks struct {
	UTMSource string `json:"utm_source"`
	Clicks    int64  `json:"clicks"`
}

// OutboundResponse is the answer of GET .../outbound-clicks.
type OutboundResponse struct {
	EventID         string         `json:"event_id"`
	From            string         `json:"from"`
	To              string         `json:"to"`
	Timezone        *string        `json:"timezone"`
	Total           int64          `json:"total"`
	Visits          int64          `json:"visits"`
	ShareOfVisitors *float64       `json:"share_of_visitors"`
	ByDay           []DayClicks    `json:"by_day"`
	BySeller        []SellerClicks `json:"by_seller"`
	BySource        []SourceClicks `json:"by_source"`
}

// BuildOutbound folds the query rows into the answer. Pure.
func BuildOutbound(eventID, from, to string, tz *string, visits int64, rows []gen.OutboundClickRow, sources []gen.OutboundClickSource) OutboundResponse {
	out := OutboundResponse{
		EventID: eventID, From: from, To: to, Timezone: tz, Visits: visits,
		ByDay: []DayClicks{}, BySeller: []SellerClicks{}, BySource: []SourceClicks{},
	}
	bySeller := map[string]int64{}
	byDay := map[string]int64{}
	for _, r := range rows {
		out.Total += r.Hits
		byDay[hpagestats.DateString(r.Day)] += r.Hits
		bySeller[r.Seller] += r.Hits
	}
	for d, n := range byDay {
		out.ByDay = append(out.ByDay, DayClicks{Day: d, Clicks: n})
	}
	sort.Slice(out.ByDay, func(a, b int) bool { return out.ByDay[a].Day < out.ByDay[b].Day })
	for s, n := range bySeller {
		out.BySeller = append(out.BySeller, SellerClicks{Seller: s, Clicks: n})
	}
	sort.Slice(out.BySeller, func(a, b int) bool {
		if out.BySeller[a].Clicks != out.BySeller[b].Clicks {
			return out.BySeller[a].Clicks > out.BySeller[b].Clicks
		}
		return out.BySeller[a].Seller < out.BySeller[b].Seller
	})
	if len(out.BySeller) > maxSellers {
		out.BySeller = out.BySeller[:maxSellers]
	}
	for _, s := range sources {
		if len(out.BySource) == maxSources {
			break
		}
		out.BySource = append(out.BySource, SourceClicks{UTMSource: s.UTMSource, Clicks: s.Hits})
	}
	out.ShareOfVisitors = share(out.Total, visits)
	return out
}

// HandleOutboundClicks serves GET /v1/organizations/{org_id}/events/{event_id}/outbound-clicks.
func (h *Handler) HandleOutboundClicks(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
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
	header, err := h.queries.GetEventPageStatsHeader(ctx, eventID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"event.not_found", "We cannot find this event.", r,
		))
		return
	}
	if err != nil {
		h.fail(w, r, eventID, "outbound_header", err)
		return
	}
	from, to, rerr := hpagestats.ParseRange(r, header.Timezone)
	if rerr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			strings.Replace(rerr.Code, "page_stats.", "outbound_clicks.", 1), rerr.Message, r,
		))
		return
	}
	rows, err := h.queries.ListOutboundClickRows(ctx, eventID, orgID, from, to)
	if err != nil {
		h.fail(w, r, eventID, "outbound_rows", err)
		return
	}
	sources, err := h.queries.ListOutboundClickSources(ctx, eventID, orgID, from, to)
	if err != nil {
		h.fail(w, r, eventID, "outbound_sources", err)
		return
	}
	visits, err := h.queries.CountExternalSiteVisits(ctx, eventID, orgID, from, to)
	if err != nil {
		h.fail(w, r, eventID, "outbound_visits", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, BuildOutbound(
		eventID.String(), hpagestats.DateString(from), hpagestats.DateString(to), header.Timezone, visits, rows, sources,
	))
}
