// Package hsalesfunnel serves the organizer's sales funnel of an event (spec 50,
// stage A7):
//
//	GET /v1/organizations/{org_id}/events/{event_id}/funnel?from=&to=&session_id=   (report.read)
//
// The answer follows a buyer through six steps: the event was shown (view), a
// session was opened (session_open), seats were chosen (seats_chosen), a
// checkout took its hold (checkout_start), a payment page was opened
// (payment_open) and the order was paid (paid). The first three are counters
// the widget and the pages send without knowing the visitor
// (hfeed POST /v1/public/funnel, table sales_funnel_daily); the last three are
// counted live from orders, checkout_sessions and payment_intents, so a figure
// that touches money never depends on a browser. Every step has its count and
// its share of the previous one; the paid orders add the revenue per currency.
//
// The same steps are split by source: the utm pair of the visit and, for the
// server steps, the sales link the order came through (checkout_sessions.attribution
// and orders.sales_link_id, spec 43).
//
// Sites that sell through the Bil24 gateway protocol (WordPress plugins, a
// channel with a gateway credential) have no client steps. Their carts and
// orders are reported apart in "gateway", whose funnel starts at seats_chosen
// instead of showing zeros for the steps it cannot have.
//
// An event of another organization (or a deleted one) is the route's own 404
// event.not_found. The view counters are an organizer's guide, not proof for an
// ad account: a bot may look like a person and a person may block the counter.
package hsalesfunnel

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesfunnel"
)

// maxSources caps the list of sources; the biggest come first.
const maxSources = 50

// Handler serves the funnel read.
type Handler struct {
	queries *gen.Queries
	logger  *slog.Logger
}

// New builds the handler. A nil queries is allowed: the route answers 503.
func New(queries *gen.Queries, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{queries: queries, logger: logger}
}

// Step is one step of a funnel. Count is the people counted (nil when the step
// cannot be read for this request: the view of one session). SuspectCount is
// what looked like a bot and was counted apart; it is 0 for the server steps.
// ShareOfPrevious is Count divided by the previous step's Count, nil when
// either is unknown or the previous is 0.
type Step struct {
	Step            string   `json:"step"`
	Count           *int64   `json:"count"`
	SuspectCount    int64    `json:"suspect_count"`
	ShareOfPrevious *float64 `json:"share_of_previous"`
}

// Money is an amount in the minor units of a currency.
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// Source is the funnel of one utm pair.
type Source struct {
	UTMSource   string  `json:"utm_source"`
	UTMCampaign string  `json:"utm_campaign"`
	Steps       []Step  `json:"steps"`
	Revenue     []Money `json:"revenue"`
}

// SalesLink is what the orders of one tracked sales link did.
type SalesLink struct {
	SalesLinkID   string  `json:"sales_link_id"`
	Code          *string `json:"code"`
	Label         *string `json:"label"`
	CheckoutStart int64   `json:"checkout_start"`
	Paid          int64   `json:"paid"`
	Revenue       []Money `json:"revenue"`
}

// SessionRef is a live session of the event for the screen's picker. Timezone
// is the IANA zone of ITS venue, so a date is read on the clock of the room it
// takes place in, not of the first session's venue or of the browser.
type SessionRef struct {
	ID       string  `json:"id"`
	StartAt  string  `json:"start_at"`
	Status   string  `json:"status"`
	Timezone *string `json:"timezone"`
}

// Gateway is the funnel of the sites on the Bil24 gateway protocol.
type Gateway struct {
	Steps   []Step  `json:"steps"`
	Revenue []Money `json:"revenue"`
}

// Response is the answer of GET .../funnel.
type Response struct {
	EventID   string       `json:"event_id"`
	SessionID *string      `json:"session_id"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	Timezone  *string      `json:"timezone"`
	Sessions  []SessionRef `json:"sessions"`
	Steps     []Step       `json:"steps"`
	Revenue   []Money      `json:"revenue"`
	Sources   []Source     `json:"sources"`
	SalesLink []SalesLink  `json:"sales_links"`
	Gateway   *Gateway     `json:"gateway"`
}

// HandleGet serves GET /v1/organizations/{org_id}/events/{event_id}/funnel.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
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
	var sessionID *uuid.UUID
	if v := strings.TrimSpace(r.URL.Query().Get("session_id")); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"funnel.invalid_session", "session_id must be a session id.", r,
			))
			return
		}
		sessionID = &id
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
		h.fail(w, r, eventID, "header", err)
		return
	}
	from, to, rerr := hpagestats.ParseRange(r, header.Timezone)
	if rerr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			strings.Replace(rerr.Code, "page_stats.", "funnel.", 1), rerr.Message, r,
		))
		return
	}
	win := gen.FunnelWindow{EventID: eventID, OrgID: orgID, From: from, To: to, Zone: zoneName(header.Timezone), SessionID: sessionID}

	client, err := h.queries.ListSalesFunnelClientRows(ctx, eventID, from, to, sessionID)
	if err != nil {
		h.fail(w, r, eventID, "client", err)
		return
	}
	server, err := h.queries.ListFunnelServerRows(ctx, win)
	if err != nil {
		h.fail(w, r, eventID, "server", err)
		return
	}
	totals, err := h.queries.GetFunnelServerTotals(ctx, win)
	if err != nil {
		h.fail(w, r, eventID, "server_totals", err)
		return
	}
	sessions, err := h.queries.ListEventFunnelSessions(ctx, eventID)
	if err != nil {
		h.fail(w, r, eventID, "sessions", err)
		return
	}
	carts, err := h.queries.CountFunnelGatewayCarts(ctx, win)
	if err != nil {
		h.fail(w, r, eventID, "gateway_carts", err)
		return
	}
	gwOrders, err := h.queries.ListFunnelGatewayOrders(ctx, win)
	if err != nil {
		h.fail(w, r, eventID, "gateway_orders", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, Build(Input{
		EventID: eventID, SessionID: sessionID, From: from, To: to, Timezone: header.Timezone,
		Client: client, Server: server, ServerTotals: totals, Sessions: sessions, GatewayCarts: carts, GatewayOrders: gwOrders,
	}))
}

// zoneName is the IANA zone the SQL clock uses: the venue's when Go knows it,
// else UTC (the same fallback as pagestats.Day).
func zoneName(tz *string) string {
	if tz == nil || *tz == "" {
		return "UTC"
	}
	if _, err := time.LoadLocation(*tz); err != nil {
		return "UTC"
	}
	return *tz
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, eventID uuid.UUID, part string, err error) {
	h.logger.Error("hsalesfunnel: read failed",
		slog.String("event_id", eventID.String()), slog.String("part", part), slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"funnel.failed", "We could not load the sales funnel. Try again in a minute.", r,
	))
}

// Input is what the queries returned. Build is pure over it.
type Input struct {
	EventID       uuid.UUID
	SessionID     *uuid.UUID
	From, To      time.Time
	Timezone      *string
	Client        []gen.SalesFunnelClientRow
	Server        []gen.FunnelServerRow
	ServerTotals  gen.FunnelServerTotals
	Sessions      []gen.EventFunnelSession
	GatewayCarts  int64
	GatewayOrders []gen.FunnelGatewayOrderRow
}

// counts is the six steps of one source, in salesfunnel.Steps order.
type counts struct {
	n       [6]int64
	suspect [6]int64
	revenue map[string]int64
}

func stepIndex(step string) int {
	for i, s := range salesfunnel.Steps {
		if s == step {
			return i
		}
	}
	return -1
}

// Build folds the query rows into the answer. Pure.
func Build(in Input) Response {
	sessionFiltered := in.SessionID != nil
	total := counts{revenue: map[string]int64{}}
	bySource := map[[2]string]*counts{}
	get := func(src, camp string) *counts {
		k := [2]string{src, camp}
		c := bySource[k]
		if c == nil {
			c = &counts{revenue: map[string]int64{}}
			bySource[k] = c
		}
		return c
	}
	for _, r := range in.Client {
		i := stepIndex(r.Step)
		if i < 0 {
			continue
		}
		total.n[i] += r.Hits
		total.suspect[i] += r.SuspectHits
		c := get(r.UTMSource, r.UTMCampaign)
		c.n[i] += r.Hits
		c.suspect[i] += r.SuspectHits
	}
	total.n[3], total.n[4], total.n[5] = in.ServerTotals.Started, in.ServerTotals.PaymentOpen, in.ServerTotals.Paid
	links := map[uuid.UUID]*SalesLink{}
	linkRevenue := map[uuid.UUID]map[string]int64{}
	var linkOrder []uuid.UUID
	for _, r := range in.Server {
		// The window's own steps come from ServerTotals (a buyer counted once
		// however many sources, links and currencies they appear under); only
		// the money is a sum of the rows.
		total.revenue[r.Currency] += r.Revenue
		c := get(r.UTMSource, r.UTMCampaign)
		c.n[3] += r.Started
		c.n[4] += r.PaymentOpen
		c.n[5] += r.Paid
		c.revenue[r.Currency] += r.Revenue
		if r.SalesLinkID != nil {
			l := links[*r.SalesLinkID]
			if l == nil {
				l = &SalesLink{SalesLinkID: r.SalesLinkID.String(), Code: r.SalesLinkCode, Label: r.SalesLinkLabel}
				links[*r.SalesLinkID] = l
				linkRevenue[*r.SalesLinkID] = map[string]int64{}
				linkOrder = append(linkOrder, *r.SalesLinkID)
			}
			l.CheckoutStart += r.Started
			l.Paid += r.Paid
			linkRevenue[*r.SalesLinkID][r.Currency] += r.Revenue
		}
	}

	out := Response{
		EventID:   in.EventID.String(),
		From:      hpagestats.DateString(in.From),
		To:        hpagestats.DateString(in.To),
		Timezone:  in.Timezone,
		Steps:     buildSteps(total, sessionFiltered),
		Revenue:   moneyList(total.revenue),
		Sources:   []Source{},
		SalesLink: []SalesLink{},
		Sessions:  []SessionRef{},
	}
	for _, s := range in.Sessions {
		out.Sessions = append(out.Sessions, SessionRef{
			ID: s.ID.String(), StartAt: s.StartAt.UTC().Format(time.RFC3339), Status: s.Status, Timezone: s.Timezone,
		})
	}
	if in.SessionID != nil {
		s := in.SessionID.String()
		out.SessionID = &s
	}

	keys := make([][2]string, 0, len(bySource))
	for k := range bySource {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		ca, cb := bySource[keys[a]], bySource[keys[b]]
		for _, i := range []int{0, 3, 5} {
			if ca.n[i] != cb.n[i] {
				return ca.n[i] > cb.n[i]
			}
		}
		if keys[a][0] != keys[b][0] {
			return keys[a][0] < keys[b][0]
		}
		return keys[a][1] < keys[b][1]
	})
	for _, k := range keys {
		if len(out.Sources) == maxSources {
			break
		}
		c := bySource[k]
		out.Sources = append(out.Sources, Source{
			UTMSource: k[0], UTMCampaign: k[1],
			Steps: buildSteps(*c, sessionFiltered), Revenue: moneyList(c.revenue),
		})
	}

	sort.SliceStable(linkOrder, func(a, b int) bool {
		la, lb := links[linkOrder[a]], links[linkOrder[b]]
		if la.Paid != lb.Paid {
			return la.Paid > lb.Paid
		}
		return la.CheckoutStart > lb.CheckoutStart
	})
	for _, id := range linkOrder {
		l := links[id]
		l.Revenue = moneyList(linkRevenue[id])
		out.SalesLink = append(out.SalesLink, *l)
	}

	if in.GatewayCarts > 0 || len(in.GatewayOrders) > 0 {
		gw := counts{revenue: map[string]int64{}}
		gw.n[2] = in.GatewayCarts
		for _, r := range in.GatewayOrders {
			gw.n[3] += r.Started
			gw.n[5] += r.Paid
			gw.revenue[r.Currency] += r.Revenue
		}
		all := buildSteps(gw, false)
		// A gateway site has no client steps before the cart and no payment
		// page of its own: its funnel is cart -> order -> paid.
		out.Gateway = &Gateway{
			Steps:   []Step{all[2], all[3], all[5]},
			Revenue: moneyList(gw.revenue),
		}
		// The order's share is taken against the cart, the payment against the order.
		out.Gateway.Steps[0].ShareOfPrevious = nil
		out.Gateway.Steps[1].ShareOfPrevious = share(gw.n[3], gw.n[2])
		out.Gateway.Steps[2].ShareOfPrevious = share(gw.n[5], gw.n[3])
	}
	return out
}

// buildSteps turns the counts into the six steps. With a session filter the
// view cannot be read (it carries no session), so it is nil and the second
// step has no share.
func buildSteps(c counts, sessionFiltered bool) []Step {
	steps := make([]Step, len(salesfunnel.Steps))
	for i, name := range salesfunnel.Steps {
		n := c.n[i]
		st := Step{Step: name, SuspectCount: c.suspect[i]}
		if i == 0 && sessionFiltered {
			steps[i] = st
			continue
		}
		st.Count = &n
		if i > 0 && steps[i-1].Count != nil {
			st.ShareOfPrevious = share(n, *steps[i-1].Count)
		}
		steps[i] = st
	}
	return steps
}

// share is n/of, nil when there is nothing to divide by.
func share(n, of int64) *float64 {
	if of <= 0 {
		return nil
	}
	v := float64(n) / float64(of)
	return &v
}

func moneyList(m map[string]int64) []Money {
	out := make([]Money, 0, len(m))
	for cur, amount := range m {
		if amount == 0 {
			continue // a free order earned nothing; it has no currency line
		}
		out = append(out, Money{Currency: cur, AmountMinor: amount})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Currency < out[b].Currency })
	return out
}
