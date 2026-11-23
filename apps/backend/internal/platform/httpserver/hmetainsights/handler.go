// Package hmetainsights serves the organizer's Meta ads reports (spec
// 08_architecture/66 section 5, stage M2):
//
//	GET  /v1/organizations/{org_id}/events/{event_id}/ads/meta?from=&to=   (report.read)
//	GET  /v1/organizations/{org_id}/ads/meta/summary?from=&to=             (report.read)
//	POST /v1/organizations/{org_id}/ads/meta/refresh                       (report.read)
//
// The routes run after the report.read gate and the membership check of
// {org_id} (an organization API key with report.read reads its own
// organization, like the sales funnel). An event of another organization, or
// a deleted one, is the route's own 404 event.not_found. When the Meta
// connection is not switched on for the platform every route answers 503
// meta.disabled. The numbers come from internal/platform/metainsights.
package hmetainsights

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metainsights"
)

// Handler serves the three routes.
type Handler struct {
	pool    *pgxpool.Pool
	enabled bool
	logger  *slog.Logger
	now     func() time.Time
}

// New builds a Handler. enabled is config.MetaEnabled(); a nil pool answers
// 503.
func New(pool *pgxpool.Pool, enabled bool, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, enabled: enabled, logger: logger, now: time.Now}
}

// WithClock replaces the clock (tests).
func (h *Handler) WithClock(now func() time.Time) *Handler { h.now = now; return h }

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

// guard answers 503 when Meta is off or there is no database, then reads
// {org_id}.
func (h *Handler) guard(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if !h.enabled {
		writeErr(w, r, http.StatusServiceUnavailable, "meta.disabled", "the Meta connection is not switched on for this platform")
		return uuid.Nil, false
	}
	if h.pool == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.")
		return uuid.Nil, false
	}
	return httputil.UUIDPathParam(w, r, "org_id")
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, step string, err error) {
	h.logger.ErrorContext(r.Context(), "hmetainsights: read failed", "step", step, "error", err.Error())
	writeErr(w, r, http.StatusInternalServerError, "meta_ads.read_failed", "We could not read the ad report. Try again in a minute.")
}

// ---------------------------------------------------------------------------
// Wire shapes (openapi.yaml MetaAds*). MER is a json.Number so no float is
// ever made of it.
// ---------------------------------------------------------------------------

// Sync is MetaAdsSync.
type Sync struct {
	Status        string     `json:"status"`
	AdAccountID   *string    `json:"ad_account_id"`
	AdAccountName *string    `json:"ad_account_name"`
	Currency      *string    `json:"currency"`
	Timezone      *string    `json:"timezone"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *string    `json:"last_error"`
	BackfillDone  bool       `json:"backfill_done"`
}

// Period is MetaAdsPeriod.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// MetaTotals is MetaAdsMetaTotals.
type MetaTotals struct {
	SpendMinor         int64   `json:"spend_minor"`
	Currency           *string `json:"currency"`
	Impressions        int64   `json:"impressions"`
	Clicks             int64   `json:"clicks"`
	LinkClicks         int64   `json:"link_clicks"`
	Purchases          int64   `json:"purchases"`
	PurchaseValueMinor int64   `json:"purchase_value_minor"`
}

// OurTotals is MetaAdsOurTotals.
type OurTotals struct {
	CheckoutsStarted int64   `json:"checkouts_started"`
	PaidOrders       int64   `json:"paid_orders"`
	RevenueNetMinor  int64   `json:"revenue_net_minor"`
	Currency         *string `json:"currency"`
}

// Untagged is MetaAdsUntagged.
type Untagged struct {
	PaidOrders     int64 `json:"paid_orders"`
	UntaggedOrders int64 `json:"untagged_orders"`
}

// EventRef is MetaAdsEventRef.
type EventRef struct {
	EventID string `json:"event_id"`
	Name    string `json:"name"`
}

// Campaign is MetaAdsCampaign.
type Campaign struct {
	CampaignID             string     `json:"campaign_id"`
	CampaignName           string     `json:"campaign_name"`
	EffectiveStatus        *string    `json:"effective_status"`
	SpendMinor             int64      `json:"spend_minor"`
	Currency               *string    `json:"currency"`
	Impressions            int64      `json:"impressions"`
	Clicks                 int64      `json:"clicks"`
	LinkClicks             int64      `json:"link_clicks"`
	MetaPurchases          int64      `json:"meta_purchases"`
	MetaPurchaseValueMinor int64      `json:"meta_purchase_value_minor"`
	PaidOrders             int64      `json:"paid_orders"`
	RevenueNetMinor        int64      `json:"revenue_net_minor"`
	RevenueCurrency        *string    `json:"revenue_currency"`
	Shared                 bool       `json:"shared"`
	Events                 []EventRef `json:"events"`
}

// EventReport is MetaAdsEventReport.
type EventReport struct {
	Sync                  Sync         `json:"sync"`
	Period                Period       `json:"period"`
	Meta                  MetaTotals   `json:"meta"`
	Ours                  OurTotals    `json:"ours"`
	MER                   *json.Number `json:"mer"`
	Untagged              Untagged     `json:"untagged"`
	Campaigns             []Campaign   `json:"campaigns"`
	URLParametersTemplate string       `json:"url_parameters_template"`
	SharedSpend           bool         `json:"shared_spend"`
}

// EventRow is MetaAdsEventRow.
type EventRow struct {
	EventID         string       `json:"event_id"`
	Name            string       `json:"name"`
	SpendMinor      int64        `json:"spend_minor"`
	Currency        *string      `json:"currency"`
	PaidOrders      int64        `json:"paid_orders"`
	RevenueNetMinor int64        `json:"revenue_net_minor"`
	RevenueCurrency *string      `json:"revenue_currency"`
	MER             *json.Number `json:"mer"`
}

// APIVolume is MetaAdsApiVolume.
type APIVolume struct {
	Calls15d  int64 `json:"calls_15d"`
	Errors15d int64 `json:"errors_15d"`
}

// OrgSummary is MetaAdsOrgSummary.
type OrgSummary struct {
	Sync                  Sync       `json:"sync"`
	Period                Period     `json:"period"`
	Meta                  MetaTotals `json:"meta"`
	Campaigns             []Campaign `json:"campaigns"`
	Events                []EventRow `json:"events"`
	APIVolume             APIVolume  `json:"api_volume"`
	URLParametersTemplate string     `json:"url_parameters_template"`
}

// RefreshResult is MetaAdsRefreshResult.
type RefreshResult struct {
	Queued    bool       `json:"queued"`
	NotBefore *time.Time `json:"not_before"`
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optMER(s string) *json.Number {
	if s == "" {
		return nil
	}
	n := json.Number(s)
	return &n
}

func syncWire(s metainsights.SyncInfo) Sync {
	var last *time.Time
	if s.LastSuccessAt != nil {
		t := s.LastSuccessAt.UTC()
		last = &t
	}
	return Sync{
		Status:        s.Status,
		AdAccountID:   optStr(s.AdAccountID),
		AdAccountName: optStr(s.AdAccountName),
		Currency:      optStr(s.Currency),
		Timezone:      optStr(s.Timezone),
		LastSuccessAt: last,
		LastError:     optStr(s.LastError),
		BackfillDone:  s.BackfillDone,
	}
}

func metaWire(m metainsights.MetaTotals) MetaTotals {
	return MetaTotals{
		SpendMinor: m.SpendMinor, Currency: optStr(m.Currency), Impressions: m.Impressions, Clicks: m.Clicks,
		LinkClicks: m.LinkClicks, Purchases: m.Purchases, PurchaseValueMinor: m.PurchaseValueMinor,
	}
}

func campaignsWire(cs []metainsights.CampaignReport) []Campaign {
	out := make([]Campaign, 0, len(cs))
	for _, c := range cs {
		evs := make([]EventRef, 0, len(c.Events))
		for _, e := range c.Events {
			evs = append(evs, EventRef{EventID: e.EventID.String(), Name: e.Name})
		}
		out = append(out, Campaign{
			CampaignID: c.CampaignID, CampaignName: c.Name, EffectiveStatus: optStr(c.EffectiveStatus),
			SpendMinor: c.Meta.SpendMinor, Currency: optStr(c.Meta.Currency), Impressions: c.Meta.Impressions,
			Clicks: c.Meta.Clicks, LinkClicks: c.Meta.LinkClicks, MetaPurchases: c.Meta.Purchases,
			MetaPurchaseValueMinor: c.Meta.PurchaseValueMinor, PaidOrders: c.Ours.PaidOrders,
			RevenueNetMinor: c.Ours.NetMinor, RevenueCurrency: optStr(c.Ours.Currency), Shared: c.Shared, Events: evs,
		})
	}
	return out
}

// period loads the sync state and reads from/to in the account timezone.
func (h *Handler) period(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (metainsights.SyncInfo, time.Time, time.Time, bool) {
	info, err := metainsights.LoadSyncInfo(r.Context(), h.pool, orgID)
	if err != nil {
		h.fail(w, r, "sync", err)
		return info, time.Time{}, time.Time{}, false
	}
	q := r.URL.Query()
	from, to, perr := metainsights.ParsePeriod(q.Get("from"), q.Get("to"), info.Location(), h.now())
	if perr != nil {
		writeErr(w, r, http.StatusBadRequest, perr.Code, perr.Message)
		return info, from, to, false
	}
	return info, from, to, true
}

const dateLayout = "2006-01-02"

// HandleEvent serves GET .../events/{event_id}/ads/meta.
func (h *Handler) HandleEvent(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	ctx := r.Context()
	if _, found, err := metainsights.EventName(ctx, h.pool, orgID, eventID); err != nil {
		h.fail(w, r, "event", err)
		return
	} else if !found {
		writeErr(w, r, http.StatusNotFound, "event.not_found", "We cannot find this event.")
		return
	}
	info, from, to, ok := h.period(w, r, orgID)
	if !ok {
		return
	}
	rep, err := metainsights.BuildEventReport(ctx, h.pool, orgID, eventID, info, from, to)
	if err != nil {
		h.fail(w, r, "report", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, EventReport{
		Sync:   syncWire(info),
		Period: Period{From: from.Format(dateLayout), To: to.Format(dateLayout)},
		Meta:   metaWire(rep.Meta),
		Ours: OurTotals{
			CheckoutsStarted: rep.Ours.CheckoutsStarted, PaidOrders: rep.Ours.PaidOrders,
			RevenueNetMinor: rep.Ours.NetMinor, Currency: optStr(rep.Ours.Currency),
		},
		MER:                   optMER(rep.MER),
		Untagged:              Untagged{PaidOrders: rep.Untagged.PaidOrders, UntaggedOrders: rep.Untagged.UntaggedOrders},
		Campaigns:             campaignsWire(rep.Campaigns),
		URLParametersTemplate: metainsights.URLParametersTemplate,
		SharedSpend:           rep.SharedSpend,
	})
}

// HandleSummary serves GET .../ads/meta/summary.
func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	info, from, to, ok := h.period(w, r, orgID)
	if !ok {
		return
	}
	sum, err := metainsights.BuildOrgSummary(r.Context(), h.pool, orgID, info, from, to, h.now())
	if err != nil {
		h.fail(w, r, "summary", err)
		return
	}
	events := make([]EventRow, 0, len(sum.Events))
	for _, e := range sum.Events {
		events = append(events, EventRow{
			EventID: e.EventID.String(), Name: e.Name, SpendMinor: e.SpendMinor, Currency: optStr(e.Currency),
			PaidOrders: e.Ours.PaidOrders, RevenueNetMinor: e.Ours.NetMinor, RevenueCurrency: optStr(e.Ours.Currency),
			MER: optMER(e.MER),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, OrgSummary{
		Sync:                  syncWire(info),
		Period:                Period{From: from.Format(dateLayout), To: to.Format(dateLayout)},
		Meta:                  metaWire(sum.Meta),
		Campaigns:             campaignsWire(sum.Campaigns),
		Events:                events,
		APIVolume:             APIVolume{Calls15d: sum.CallsTotal, Errors15d: sum.CallErrors},
		URLParametersTemplate: metainsights.URLParametersTemplate,
	})
}

// HandleRefresh serves POST .../ads/meta/refresh: 202 with queued and the
// time of the next accepted refresh.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	queued, notBefore, err := metainsights.EnqueueRefresh(r.Context(), h.pool, orgID, h.now())
	if err != nil {
		h.fail(w, r, "refresh", err)
		return
	}
	res := RefreshResult{Queued: queued}
	if !notBefore.IsZero() {
		nb := notBefore.UTC()
		res.NotBefore = &nb
	}
	httputil.WriteJSON(w, http.StatusAccepted, res)
}
