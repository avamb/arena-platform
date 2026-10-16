package hexport

import (
	"math"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// pageStatsColumns are the columns of the page statistics file: one row per
// counter row of the table (day, kind, utm pair).
var pageStatsColumns = []csvexport.Column{
	csvexport.ColDate, csvexport.ColKind, csvexport.ColUTMSource, csvexport.ColUTMCampaign,
	csvexport.ColCount, csvexport.ColSuspectCount,
}

// HandlePageStats serves
// GET /v1/organizations/{org_id}/events/{event_id}/page-stats.csv?from=&to=
// (event.read): the counters of the hosted page's beacon (HP-2a) as the
// organizer's guide, one row per day, kind and utm pair, the same window rules
// as the JSON route (hpagestats.ParseRange). It is an EVENT-level file: a page
// view belongs to the event, not to a session or a category, which is why it is
// not a set of columns on summary.csv.
func (h *Handler) HandlePageStats(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidParam(w, r, "event_id")
	if !ok {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetEventPageStatsHeader(ctx, eventID, orgID)
	if isNoRows(err) {
		notFound(w, r, "event.not_found", "event not found")
		return
	}
	if err != nil {
		h.internal(w, r, "header", err)
		return
	}
	from, to, rerr := hpagestats.ParseRange(r, header.Timezone)
	if rerr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(rerr.Code, rerr.Message, r))
		return
	}
	// One row over the cap tells a file that fits from one that does not,
	// before any byte is streamed.
	limit := h.maxRows + 1
	if limit > math.MaxInt32 {
		limit = math.MaxInt32
	}
	rows, err := h.queries.ListEventPageStatsRows(ctx, eventID, from, to, int32(limit)) //nolint:gosec // clamped above
	if err != nil {
		h.internal(w, r, "rows", err)
		return
	}
	if h.tooManyRows(w, r, int64(len(rows))) {
		return
	}
	locale := csvexport.LocaleFromRequest(r)
	s := begin(w, filename("page-stats", slugOr(header.Slug, eventID.String()), time.Now(), venueLocation(header.Timezone)))
	if err := s.csv.WriteHeader(csvexport.Header(locale, pageStatsColumns...)); err != nil {
		h.abort(ctx, "header", err)
	}
	for _, row := range rows {
		if err := s.csv.WriteRow(
			csvexport.Text(hpagestats.DateString(row.Day)),
			csvexport.Text(row.Kind),
			csvexport.Text(row.UTMSource),
			csvexport.Text(row.UTMCampaign),
			csvexport.Number(row.Hits),
			csvexport.Number(row.SuspectHits),
		); err != nil {
			h.abort(ctx, "write", err)
		}
	}
	if err := s.flush(); err != nil {
		h.abort(ctx, "flush", err)
	}
}
