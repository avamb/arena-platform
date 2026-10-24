package hsalelinks

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultStatsDays = 30
	maxStatsDays     = 366
	dayLayout        = "2006-01-02" // allow:timeformat
)

type statsDayJSON struct {
	Day         string `json:"day"`
	Hits        int64  `json:"hits"`
	SuspectHits int64  `json:"suspect_hits"`
}

type statsCountryJSON struct {
	Country string `json:"country"`
	Hits    int64  `json:"hits"`
}

type statsRevenueJSON struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

type statsOrdersJSON struct {
	Count   int64              `json:"count"`
	Tickets int64              `json:"tickets"`
	Revenue []statsRevenueJSON `json:"revenue"`
}

type statsJSON struct {
	ScansTotal   int64              `json:"scans_total"`
	SuspectTotal int64              `json:"suspect_total"`
	ByDay        []statsDayJSON     `json:"by_day"`
	ByCountry    []statsCountryJSON `json:"by_country"`
	Orders       statsOrdersJSON    `json:"orders"`
}

func invalidRange() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_range",
		"from and to must be dates as YYYY-MM-DD, from not after to, at most 366 days apart"}
}

// statsRange reads ?from=&to= (both inclusive calendar days, UTC). The default
// is the last 30 days ending today.
func statsRange(r *http.Request, now time.Time) (from, to time.Time, err *apiError) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	to = today
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		t, perr := time.Parse(dayLayout, v)
		if perr != nil {
			return from, to, invalidRange()
		}
		to = t
	}
	from = to.AddDate(0, 0, -(defaultStatsDays - 1))
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		t, perr := time.Parse(dayLayout, v)
		if perr != nil {
			return from, to, invalidRange()
		}
		from = t
	}
	if from.After(to) || int(to.Sub(from).Hours()/24)+1 > maxStatsDays {
		return from, to, invalidRange()
	}
	return from, to, nil
}

// HandleStats serves GET /v1/organizations/{org_id}/sales-links/{id}/stats:
// the scans (the redirect) and the orders that came through the code. Scan
// totals are all-time counters of the link, the day and country series cover
// the asked range, and the orders block is everything ever paid through the
// link, revenue per currency in minor units after succeeded refunds.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	id, ok := linkID(w, r)
	if !ok {
		return
	}
	from, to, rerr := statsRange(r, time.Now())
	if rerr != nil {
		rerr.write(w, r)
		return
	}
	ctx := r.Context()
	link, err := h.queries.GetSalesLinkByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "stats load", err)
		return
	}
	days, err := h.queries.SalesLinkHitsByDay(ctx, id, orgID, from, to)
	if err != nil {
		h.failed(w, r, "stats by day", err)
		return
	}
	countries, err := h.queries.SalesLinkHitsByCountry(ctx, id, orgID, from, to)
	if err != nil {
		h.failed(w, r, "stats by country", err)
		return
	}
	totals, err := h.queries.SalesLinkOrdersAggregate(ctx, id, orgID)
	if err != nil {
		h.failed(w, r, "stats orders", err)
		return
	}

	out := statsJSON{
		ScansTotal: link.ScanCount, SuspectTotal: link.SuspectScanCount,
		ByDay: []statsDayJSON{}, ByCountry: []statsCountryJSON{},
		Orders: statsOrdersJSON{Revenue: []statsRevenueJSON{}},
	}
	for _, d := range days {
		out.ByDay = append(out.ByDay, statsDayJSON{Day: d.Day.UTC().Format(dayLayout), Hits: d.Hits, SuspectHits: d.SuspectHits})
	}
	for _, c := range countries {
		out.ByCountry = append(out.ByCountry, statsCountryJSON{Country: c.Country, Hits: c.Hits})
	}
	for _, t := range totals {
		out.Orders.Count += t.Orders
		out.Orders.Tickets += t.Tickets
		out.Orders.Revenue = append(out.Orders.Revenue, statsRevenueJSON{Currency: t.Currency, AmountMinor: t.RevenueMinor})
	}
	writeJSON(w, http.StatusOK, out)
}
