package hexport

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/horders"
)

// summaryColumns are the per-category columns of a session summary.
var summaryColumns = []csvexport.Column{
	csvexport.ColCategory, csvexport.ColKind, csvexport.ColPrice, csvexport.ColCurrency,
	csvexport.ColPlacesTotal, csvexport.ColPlacesAvailable, csvexport.ColPlacesHeld,
	csvexport.ColPlacesSold, csvexport.ColPlacesSoldUpstream, csvexport.ColPlacesUnavailable,
	csvexport.ColPaidTickets, csvexport.ColRevenue, csvexport.ColOnSale,
}

// HandleSessionSummary serves
// GET /v1/organizations/{org_id}/sessions/{session_id}/summary.csv — the
// per-category lines of the JSON summary (horders.LoadSessionSummary, the
// same numbers the one-screen overview shows), one row per category.
func (h *Handler) HandleSessionSummary(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetExportSessionHeader(ctx, sessionID, orgID)
	if isNoRows(err) {
		notFound(w, r, "session.not_found", "session not found")
		return
	}
	if err != nil {
		h.internal(w, r, "session", err)
		return
	}
	summary, part, err := horders.LoadSessionSummary(ctx, h.queries, sessionID, orgID)
	if isNoRows(err) {
		notFound(w, r, "session.not_found", "session not found")
		return
	}
	if err != nil {
		h.internal(w, r, "summary "+part, err)
		return
	}
	loc := venueLocation(header.VenueTimezone)
	locale := csvexport.LocaleFromRequest(r)

	s := begin(w, filename("summary", slugOr(header.EventSlug, header.EventID.String()), time.Now(), loc))
	if err := s.csv.WriteHeader(csvexport.Header(locale, summaryColumns...)); err != nil {
		h.abort(ctx, "header", err)
	}
	for _, t := range summary.Tiers {
		if err := s.csv.WriteRow(summaryCells(t)...); err != nil {
			h.abort(ctx, "write", err)
		}
	}
	if err := s.flush(); err != nil {
		h.abort(ctx, "flush", err)
	}
}

// summaryCells renders one category of the summary.
func summaryCells(t horders.SummaryTier) []csvexport.Cell {
	return []csvexport.Cell{
		csvexport.Text(t.Name),
		csvexport.Text(t.Kind),
		csvexport.Money(t.PriceAmount),
		csvexport.Text(t.Currency),
		csvexport.Number(t.Places.Total),
		csvexport.Number(t.Places.Available),
		csvexport.Number(t.Places.Held),
		csvexport.Number(t.Places.Sold),
		csvexport.Number(t.Places.SoldUpstream),
		csvexport.Number(t.Places.Unavailable),
		csvexport.Number(t.PaidItems),
		csvexport.Money(t.PaidRevenue),
		csvexport.YesNo(t.IsOpen),
	}
}
