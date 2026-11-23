// public_series.go implements the public side of tours (event_series,
// migration 0222, spec 63 section 15.1):
//
//	GET /v1/public/feeds/{feed_token}/series
//	GET /v1/public/feeds/{feed_token}/series/{series_id}
//	GET /v1/public/pages/{org_slug}/series/{series_slug}
//
// A tour is shown to a feed only through the events of that feed: it appears
// when at least one of its events is visible there by the same rule as
// GET .../events (published, active token, not deleted). An archived tour is
// hidden everywhere. The hosted page lists the tour's events by the rule of
// the organization page (hosted-page channel, published, with a slug).
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// feedSeriesNextSession is the nearest upcoming session of a tour.
type feedSeriesNextSession struct {
	EventID   string  `json:"event_id"`
	SessionID string  `json:"session_id"`
	StartsAt  string  `json:"starts_at"`
	City      *string `json:"city"`
}

// feedSeriesPrice is the lowest open price of a tour in one currency, in
// MAJOR units (19.5 for 1950 minor units of EUR).
type feedSeriesPrice struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

// feedSeriesSummary is the "at a glance" block of a tour.
type feedSeriesSummary struct {
	DateFrom          *string                `json:"date_from"`
	DateTo            *string                `json:"date_to"`
	CitiesCount       int                    `json:"cities_count"`
	CitiesOnSaleCount int                    `json:"cities_on_sale_count"`
	NextSession       *feedSeriesNextSession `json:"next_session"`
	PriceFrom         []feedSeriesPrice      `json:"price_from"`
}

// feedSeriesResponse is one tour as the feed and the hosted page show it.
type feedSeriesResponse struct {
	ID                  string            `json:"id"`
	Slug                string            `json:"slug"`
	Name                string            `json:"name"`
	DescriptionHTML     *string           `json:"description_html"`
	PartnersTextHTML    *string           `json:"partners_text_html"`
	PosterURL           *string           `json:"poster_url"`
	HeroURL             *string           `json:"hero_url"`
	HeroVideoURL        *string           `json:"hero_video_url"`
	HideChildrenInLists bool              `json:"hide_children_in_lists"`
	Position            int32             `json:"position"`
	EventIDs            []string          `json:"event_ids"`
	Summary             feedSeriesSummary `json:"summary"`
	// Translations is reserved for per-language texts; always {} for now.
	Translations map[string]any `json:"translations"`
}

// hostedSeriesPageResponse is the envelope of GET
// /v1/public/pages/{org_slug}/series/{series_slug}.
type hostedSeriesPageResponse struct {
	Org           hostedPageOrgResponse     `json:"org"`
	DefaultLocale string                    `json:"default_locale"`
	Series        feedSeriesResponse        `json:"series"`
	Events        []hostedPageEventResponse `json:"events"`
	Seller        *hostedPageSellerResponse `json:"seller,omitempty"`
	ContactForm   *hostedPageContactForm    `json:"contact_form,omitempty"`
}

// hydrateFeedSeriesIDs fills SeriesID of the events in one batched read.
// Non-fatal: a failure leaves series_id null and is logged.
func (h *Handler) hydrateFeedSeriesIDs(ctx context.Context, responses []publicFeedEventResponse) {
	if h.publicFeedQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, resp := range responses {
		if id, err := uuid.Parse(resp.ID); err == nil {
			ids = append(ids, id)
		}
	}
	series, err := h.publicFeedQueries.ListActiveSeriesIDsForEvents(ctx, ids)
	if err != nil {
		h.logger.Warn("public_feed: series id hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		id, err := uuid.Parse(responses[i].ID)
		if err != nil {
			continue
		}
		if sid, ok := series[id]; ok {
			s := sid.String()
			responses[i].SeriesID = &s
		}
	}
}

func majorAmount(minor int64, currency string) float64 {
	div := 1.0
	for i := 0; i < layoutgen.MinorExponent(currency); i++ {
		div *= 10
	}
	return float64(minor) / div
}

// seriesSummaries computes the summary of each group of events (keyed by
// tour id) from the events' scheduled sessions, in a fixed number of batched
// reads. Dates and cities cover every scheduled session; prices and the
// "on sale" count only sessions whose sales state is open now.
func (h *Handler) seriesSummaries(ctx context.Context, groups map[uuid.UUID][]uuid.UUID, now time.Time) (map[uuid.UUID]feedSeriesSummary, error) {
	out := make(map[uuid.UUID]feedSeriesSummary, len(groups))
	var allEvents []uuid.UUID
	seriesOfEvent := map[uuid.UUID]uuid.UUID{}
	for sid, evs := range groups {
		for _, e := range evs {
			allEvents = append(allEvents, e)
			seriesOfEvent[e] = sid
		}
		out[sid] = feedSeriesSummary{PriceFrom: []feedSeriesPrice{}}
	}
	sessions, err := h.publicFeedQueries.ListSeriesSessions(ctx, allEvents)
	if err != nil {
		return nil, err
	}
	sessionIDs := make([]uuid.UUID, len(sessions))
	for i, s := range sessions {
		sessionIDs[i] = s.SessionID
	}
	facts, err := h.publicFeedQueries.ListSessionSalesFacts(ctx, sessionIDs, now)
	if err != nil {
		return nil, err
	}
	open := map[uuid.UUID]bool{}
	for _, s := range sessions {
		if f, ok := facts[s.SessionID]; ok &&
			salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now) == salesstate.Open {
			open[s.SessionID] = true
		}
	}
	var openIDs []uuid.UUID
	for id := range open {
		openIDs = append(openIDs, id)
	}
	tiers, err := h.publicFeedQueries.ListSellableTierPrices(ctx, openIDs, now)
	if err != nil {
		return nil, err
	}
	placeless, err := h.publicFeedQueries.ListPlacelessPlanTierIDsBySessions(ctx, openIDs)
	if err != nil {
		return nil, err
	}

	type agg struct {
		from, to  time.Time
		hasDates  bool
		cities    map[uuid.UUID]bool
		onSale    map[uuid.UUID]bool
		next      *feedSeriesNextSession
		nextStart time.Time
		prices    map[string]int64
	}
	aggs := map[uuid.UUID]*agg{}
	for sid := range groups {
		aggs[sid] = &agg{cities: map[uuid.UUID]bool{}, onSale: map[uuid.UUID]bool{}, prices: map[string]int64{}}
	}
	sessionSeries := map[uuid.UUID]uuid.UUID{}
	for _, s := range sessions { // sorted by start
		sid := seriesOfEvent[s.EventID]
		sessionSeries[s.SessionID] = sid
		a := aggs[sid]
		if !a.hasDates || s.StartAt.Before(a.from) {
			a.from = s.StartAt
		}
		if !a.hasDates || s.StartAt.After(a.to) {
			a.to = s.StartAt
		}
		a.hasDates = true
		if s.CityID != nil {
			a.cities[*s.CityID] = true
			if open[s.SessionID] {
				a.onSale[*s.CityID] = true
			}
		}
		if !s.StartAt.Before(now) && (a.next == nil || s.StartAt.Before(a.nextStart)) {
			a.nextStart = s.StartAt
			a.next = &feedSeriesNextSession{
				EventID:   s.EventID.String(),
				SessionID: s.SessionID.String(),
				StartsAt:  s.StartAt.UTC().Format(time.RFC3339),
				City:      s.CityName,
			}
		}
	}
	// The price of a category is the one in effect now (priceresolve, the rule
	// of the feed's event detail), and a step of a price chain that does not
	// sell now is skipped (chainView.hide), one session at a time.
	bySession := map[uuid.UUID][]gen.SeriesTierPriceRow{}
	var sessionOrder []uuid.UUID
	for _, t := range tiers {
		if placeless[t.TierID] {
			continue
		}
		if _, ok := bySession[t.SessionID]; !ok {
			sessionOrder = append(sessionOrder, t.SessionID)
		}
		bySession[t.SessionID] = append(bySession[t.SessionID], t)
	}
	for _, sessID := range sessionOrder {
		a := aggs[sessionSeries[sessID]]
		if a == nil {
			continue
		}
		var hide map[uuid.UUID]bool
		var eff map[uuid.UUID]priceresolve.Effective
		if h.tierQueries != nil {
			all, err := h.tierQueries.ListTicketTiersBySession(ctx, sessID)
			if err != nil {
				return nil, err
			}
			all = withoutPlacelessTiers(all, placeless)
			if eff, err = priceresolve.ForTiers(ctx, h.tierQueries, all, now); err != nil {
				return nil, err
			}
			hide = h.loadChainView(ctx, sessID, all).hide
		}
		for _, t := range bySession[sessID] {
			if hide[t.TierID] {
				continue
			}
			amount := t.PriceAmount
			if e, ok := eff[t.TierID]; ok {
				amount = e.Amount
			}
			if cur, ok := a.prices[t.Currency]; !ok || amount < cur {
				a.prices[t.Currency] = amount
			}
		}
	}
	for sid, a := range aggs {
		sum := feedSeriesSummary{
			CitiesCount:       len(a.cities),
			CitiesOnSaleCount: len(a.onSale),
			NextSession:       a.next,
			PriceFrom:         []feedSeriesPrice{},
		}
		if a.hasDates {
			f, t := a.from.UTC().Format(time.RFC3339), a.to.UTC().Format(time.RFC3339)
			sum.DateFrom, sum.DateTo = &f, &t
		}
		cur := make([]string, 0, len(a.prices))
		for c := range a.prices {
			cur = append(cur, c)
		}
		sort.Strings(cur)
		for _, c := range cur {
			sum.PriceFrom = append(sum.PriceFrom, feedSeriesPrice{Currency: c, Amount: majorAmount(a.prices[c], c)})
		}
		out[sid] = sum
	}
	return out, nil
}

// seriesResponse builds the public shape of a tour.
func (h *Handler) seriesResponse(ctx context.Context, s gen.EventSeriesRow, eventIDs []uuid.UUID, sum feedSeriesSummary) feedSeriesResponse {
	r := feedSeriesResponse{
		ID:                  s.ID.String(),
		Slug:                s.Slug,
		Name:                s.Name,
		DescriptionHTML:     s.DescriptionHTML,
		PartnersTextHTML:    s.PartnersTextHTML,
		HeroVideoURL:        s.HeroVideoURL,
		HideChildrenInLists: s.HideChildrenInLists,
		Position:            s.Position,
		EventIDs:            make([]string, len(eventIDs)),
		Summary:             sum,
		Translations:        map[string]any{},
	}
	for i, id := range eventIDs {
		r.EventIDs[i] = id.String()
	}
	if s.PosterMediaID != nil {
		u := h.mediaFileURL(ctx, *s.PosterMediaID)
		r.PosterURL = &u
	}
	if s.HeroMediaID != nil {
		u := h.mediaFileURL(ctx, *s.HeroMediaID)
		r.HeroURL = &u
	}
	return r
}

// checkFeedToken applies the feed rate limits and the token check shared by
// the /series routes; false means the answer is already written.
func (h *Handler) checkFeedToken(w http.ResponseWriter, r *http.Request, feedToken string) bool {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return false
	}
	if !h.enforceRateLimit(w, r, "feed.rate_limited", h.rl.CheckFeedToken, feedToken) {
		return false
	}
	if h.feedTokenQueries != nil {
		ft, err := h.feedTokenQueries.GetFeedTokenByToken(r.Context(), feedToken)
		if err != nil || !ft.IsActive {
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				h.logger.Error("public_series: token lookup failed", slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"feed.token_lookup_failed", "We could not check this ticket link. Try again in a minute.", r,
				))
				return false
			}
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"feed.token_not_found", "This ticket link does not work any more. Ask the organizer for a new link.", r,
			))
			return false
		}
	}
	return true
}

// feedSeries loads the tours visible in the feed (one tour when onlyID is
// set), in tour order.
func (h *Handler) feedSeries(ctx context.Context, feedToken string, onlyID *uuid.UUID) ([]feedSeriesResponse, error) {
	rows, err := h.publicFeedQueries.ListFeedSeriesEvents(ctx, feedToken, onlyID)
	if err != nil {
		return nil, err
	}
	groups := map[uuid.UUID][]uuid.UUID{}
	ids := make([]uuid.UUID, 0)
	for _, r := range rows { // by first session
		if _, ok := groups[r.SeriesID]; !ok {
			ids = append(ids, r.SeriesID)
		}
		groups[r.SeriesID] = append(groups[r.SeriesID], r.EventID)
	}
	if len(ids) == 0 {
		return []feedSeriesResponse{}, nil
	}
	series, err := h.publicFeedQueries.ListEventSeriesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	sums, err := h.seriesSummaries(ctx, groups, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	out := make([]feedSeriesResponse, 0, len(series))
	for _, s := range series {
		out = append(out, h.seriesResponse(ctx, s, groups[s.ID], sums[s.ID]))
	}
	return out, nil
}

// HandlePublicFeedSeries answers GET /v1/public/feeds/{feed_token}/series:
// the non-archived tours with at least one event visible in this feed.
// The locale query parameter is accepted and ignored for now.
func (h *Handler) HandlePublicFeedSeries(w http.ResponseWriter, r *http.Request) {
	feedToken := chi.URLParam(r, "feed_token")
	if !h.checkFeedToken(w, r, feedToken) {
		return
	}
	items, err := h.feedSeries(r.Context(), feedToken, nil)
	if err != nil {
		h.logger.Error("public_series: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.list_failed", "We could not load the tours. Try again in a minute.", r,
		))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=30")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// HandlePublicFeedSeriesItem answers GET
// /v1/public/feeds/{feed_token}/series/{series_id}: 404 for an archived tour,
// another organization's tour or one with no event visible in this feed.
func (h *Handler) HandlePublicFeedSeriesItem(w http.ResponseWriter, r *http.Request) {
	feedToken := chi.URLParam(r, "feed_token")
	if !h.checkFeedToken(w, r, feedToken) {
		return
	}
	notFound := func() {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"series.not_found", "We cannot find this tour.", r,
		))
	}
	id, err := uuid.Parse(chi.URLParam(r, "series_id"))
	if err != nil {
		notFound()
		return
	}
	items, err := h.feedSeries(r.Context(), feedToken, &id)
	if err != nil {
		h.logger.Error("public_series: detail failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.fetch_failed", "We could not load the tour. Try again in a minute.", r,
		))
		return
	}
	if len(items) == 0 {
		notFound()
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=15")
	httputil.WriteJSON(w, http.StatusOK, items[0])
}

// HandlePublicSeriesPage answers GET
// /v1/public/pages/{org_slug}/series/{series_slug}: the tour page. 404
// (page.not_found, indistinguishable) when the organization or the tour is
// unknown or archived, the organization has no hosted-page channel, or none
// of the tour's events is listed on the organization's page.
func (h *Handler) HandlePublicSeriesPage(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	if !h.enforceRateLimit(w, r, "page.rate_limited", func(string) (bool, int) { return true, 0 }, "") {
		return
	}
	notFound := func() {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"page.not_found", "We cannot find this page. Check the address.", r,
		))
	}
	fail := func(err error) {
		h.logger.Error("public_series: page failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"page.resolve_failed", "We could not load this page. Try again in a minute.", r,
		))
	}
	orgSlug, seriesSlug := chi.URLParam(r, "org_slug"), chi.URLParam(r, "series_slug")
	if orgSlug == "" || seriesSlug == "" {
		notFound()
		return
	}
	ctx := r.Context()
	org, err := h.publicFeedQueries.GetHostedPromoterPageOrg(ctx, orgSlug)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !org.HasHostedChannel) {
		notFound()
		return
	}
	if err != nil {
		fail(err)
		return
	}
	series, err := h.publicFeedQueries.GetActiveEventSeriesBySlug(ctx, org.OrgID, seriesSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound()
		return
	}
	if err != nil {
		fail(err)
		return
	}
	all, err := h.publicFeedQueries.ListHostedPromoterPageEvents(ctx, org.OrgID)
	if err != nil {
		fail(err)
		return
	}
	allIDs := make([]uuid.UUID, len(all))
	for i, e := range all {
		allIDs[i] = e.EventID
	}
	seriesOf, err := h.publicFeedQueries.ListActiveSeriesIDsForEvents(ctx, allIDs)
	if err != nil {
		fail(err)
		return
	}
	var events []gen.HostedPromoterPageEventRow
	var eventIDs []uuid.UUID
	for _, e := range all {
		if seriesOf[e.EventID] == series.ID {
			events = append(events, e)
			eventIDs = append(eventIDs, e.EventID)
		}
	}
	if len(events) == 0 {
		notFound()
		return
	}
	sums, err := h.seriesSummaries(ctx, map[uuid.UUID][]uuid.UUID{series.ID: eventIDs}, time.Now().UTC())
	if err != nil {
		fail(err)
		return
	}
	resp := hostedSeriesPageResponse{
		Org:           hostedPageOrgResponse{Slug: org.OrgSlug, Name: org.OrgName},
		DefaultLocale: org.OrgDefaultLocale,
		Series:        h.seriesResponse(ctx, series, eventIDs, sums[series.ID]),
		Events:        h.hostedPageEventItems(ctx, orgSlug, events),
	}
	if org.OrgLogoMediaID != nil {
		u := h.mediaFileURL(ctx, *org.OrgLogoMediaID)
		resp.Org.LogoURL = &u
	}
	resp.Seller, _, resp.ContactForm = h.sellerBlocks(ctx, org.OrgID, "", nil, false)

	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=15")
	httputil.WriteJSON(w, http.StatusOK, resp)
}
