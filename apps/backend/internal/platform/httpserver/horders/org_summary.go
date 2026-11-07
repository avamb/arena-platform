package horders

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The organization summary (EC-V1, spec 62 §12.2) is the organization-wide
// counterpart of the session summary: one card per session and currency with
// what was sold, invited, refunded and earned, and under each card the
// "price x category" breakdown with the promo codes used. It is read-only,
// scoped to one organization and carries no buyer data.
//
//	GET /v1/organizations/{org_id}/summary?from=&to=&event_id=   (order.read)
//
// The money comes from the very queries and fold (foldMoney) every other
// summary uses, so a card can never disagree with the session overview: revenue
// is orders.total of the orders paid at some point, refunded the SUCCEEDED
// refunds of both settlements (provider and external). Test orders are left
// out of every figure and counted apart in test_orders. The same assembly feeds
// the accounting workbook (hexport, EC-V2).

// OrgSummaryMaxSessions caps the sessions one summary reads. The accounting
// workbook asks for more (OrgAccountingMaxSessions): it is a file, not a phone
// screen.
const (
	OrgSummaryMaxSessions    = 500
	OrgAccountingMaxSessions = 5000
)

// OrgSummaryFilter is the optional narrowing of the summary: sessions that
// start in [From, To) and, when set, belong to one event.
type OrgSummaryFilter struct {
	From    *time.Time
	To      *time.Time
	EventID *uuid.UUID
}

type orgSummarySession struct {
	ID            string  `json:"id"`
	EventID       string  `json:"event_id"`
	EventName     string  `json:"event_name"`
	StartAt       string  `json:"start_at"`
	Status        string  `json:"status"`
	VenueName     *string `json:"venue_name"`
	VenueTimezone *string `json:"venue_timezone"`
}

// OrgSummaryBreakdown is the units sold at one price in one category with one
// promo code. PromoCode is nil for units sold without one.
type OrgSummaryBreakdown struct {
	TierID    string  `json:"tier_id"`
	TierName  string  `json:"tier_name"`
	UnitPrice int64   `json:"unit_price"`
	PromoCode *string `json:"promo_code"`
	Tickets   int64   `json:"tickets"`
	Discount  int64   `json:"discount"`
	Revenue   int64   `json:"revenue"`
}

// OrgSummaryCard is one session in one currency, in minor units. Sold counts
// the tickets that were not invitations, whatever became of them since;
// RefundedTickets those among them cancelled with money returned; Active the
// valid tickets of every kind. Revenue, Refunded and Net follow summaryMoney.
type OrgSummaryCard struct {
	Currency        string                `json:"currency"`
	Orders          int64                 `json:"orders"`
	Sold            int64                 `json:"sold"`
	Complimentary   int64                 `json:"complimentary"`
	RefundedTickets int64                 `json:"refunded_tickets"`
	ActiveTickets   int64                 `json:"active_tickets"`
	Revenue         int64                 `json:"revenue"`
	Refunded        int64                 `json:"refunded"`
	Cashback        int64                 `json:"cashback"`
	Net             int64                 `json:"net"`
	Breakdown       []OrgSummaryBreakdown `json:"breakdown"`
	Promos          []summaryPromo        `json:"promos"`
}

// OrgSummarySession is a session with its cards.
type OrgSummarySession struct {
	orgSummarySession
	Start      time.Time        `json:"-"`
	TestOrders int64            `json:"test_orders"`
	Cards      []OrgSummaryCard `json:"cards"`
}

// OrgSummaryTotal is the sum of the cards of one currency.
type OrgSummaryTotal struct {
	Currency        string `json:"currency"`
	Orders          int64  `json:"orders"`
	Sold            int64  `json:"sold"`
	Complimentary   int64  `json:"complimentary"`
	RefundedTickets int64  `json:"refunded_tickets"`
	ActiveTickets   int64  `json:"active_tickets"`
	Revenue         int64  `json:"revenue"`
	Refunded        int64  `json:"refunded"`
	Cashback        int64  `json:"cashback"`
	Net             int64  `json:"net"`
}

// OrgSummary is the JSON body of the route.
type OrgSummary struct {
	Sessions   []OrgSummarySession `json:"sessions"`
	Totals     []OrgSummaryTotal   `json:"totals"`
	TestOrders int64               `json:"test_orders"`
	// Truncated is true when more sessions matched than were read; narrow the
	// range to see them.
	Truncated bool `json:"truncated"`
}

// orgSummaryRows is everything the queries return for the sessions found.
type orgSummaryRows struct {
	sessions  []gen.OrgSummarySessionRow
	money     summaryRows // orders, refunds, promos, test orders
	tickets   []gen.SessionSummaryTicketCardRow
	breakdown []gen.SessionSummaryBreakdownRow
	truncated bool
}

// buildOrgSummary folds the raw rows into the response. Pure, so the
// arithmetic is unit-tested without a database. Sessions without a single
// card (nothing sold, invited or refunded in them) are left out of the list;
// their test orders still count in TestOrders.
func buildOrgSummary(rows orgSummaryRows) OrgSummary {
	out := OrgSummary{
		Sessions:  []OrgSummarySession{},
		Totals:    []OrgSummaryTotal{},
		Truncated: rows.truncated,
	}
	moneyBy := rows.money.groupBySession()
	ticketsBy := map[uuid.UUID][]gen.SessionSummaryTicketCardRow{}
	for _, t := range rows.tickets {
		ticketsBy[t.SessionID] = append(ticketsBy[t.SessionID], t)
	}
	breakdownBy := map[uuid.UUID][]gen.SessionSummaryBreakdownRow{}
	for _, b := range rows.breakdown {
		breakdownBy[b.SessionID] = append(breakdownBy[b.SessionID], b)
	}
	testBy := map[uuid.UUID]int64{}
	for _, t := range rows.money.testOrders {
		testBy[t.SessionID] += t.Orders
		out.TestOrders += t.Orders
	}

	totals := map[string]*OrgSummaryTotal{}
	for _, s := range rows.sessions {
		sess := OrgSummarySession{
			orgSummarySession: orgSummarySession{
				ID:            s.ID.String(),
				EventID:       s.EventID.String(),
				EventName:     s.EventName,
				StartAt:       s.StartAt.UTC().Format(time.RFC3339),
				Status:        s.Status,
				VenueName:     s.VenueName,
				VenueTimezone: s.VenueTimezone,
			},
			Start:      s.StartAt,
			TestOrders: testBy[s.ID],
			Cards:      buildOrgCards(moneyBy[s.ID], ticketsBy[s.ID], breakdownBy[s.ID]),
		}
		if len(sess.Cards) == 0 {
			continue
		}
		for _, c := range sess.Cards {
			t := totals[c.Currency]
			if t == nil {
				t = &OrgSummaryTotal{Currency: c.Currency}
				totals[c.Currency] = t
			}
			t.Orders += c.Orders
			t.Sold += c.Sold
			t.Complimentary += c.Complimentary
			t.RefundedTickets += c.RefundedTickets
			t.ActiveTickets += c.ActiveTickets
			t.Revenue += c.Revenue
			t.Refunded += c.Refunded
			t.Cashback += c.Cashback
			t.Net += c.Net
		}
		out.Sessions = append(out.Sessions, sess)
	}
	for _, t := range totals {
		out.Totals = append(out.Totals, *t)
	}
	sort.Slice(out.Totals, func(i, j int) bool { return out.Totals[i].Currency < out.Totals[j].Currency })
	return out
}

// buildOrgCards makes the cards of one session: one per currency that has any
// paid order, refund, ticket or invitation.
func buildOrgCards(money summaryRows, tickets []gen.SessionSummaryTicketCardRow, breakdown []gen.SessionSummaryBreakdownRow) []OrgSummaryCard {
	_, _, moneyByCurrency := foldMoney(money.orders, money.refunds)
	promos := foldPromos(money.promos)
	cards := map[string]*OrgSummaryCard{}
	card := func(currency string) *OrgSummaryCard {
		c := cards[currency]
		if c == nil {
			c = &OrgSummaryCard{Currency: currency, Breakdown: []OrgSummaryBreakdown{}, Promos: []summaryPromo{}}
			cards[currency] = c
		}
		return c
	}
	for _, m := range moneyByCurrency {
		if m.PaidOrders == 0 && m.Refunded == 0 && m.Cashback == 0 {
			continue // only pending payments: not a sale yet
		}
		c := card(m.Currency)
		c.Orders, c.Revenue, c.Refunded, c.Cashback, c.Net = m.PaidOrders, m.Paid, m.Refunded, m.Cashback, m.Net
	}
	for _, t := range tickets {
		c := card(t.Currency)
		c.Sold, c.Complimentary, c.RefundedTickets, c.ActiveTickets = t.Sold, t.Complimentary, t.Refunded, t.Active
	}
	for _, b := range breakdown {
		var promo *string
		if b.PromoCode != "" {
			p := b.PromoCode
			promo = &p
		}
		c := card(b.Currency)
		c.Breakdown = append(c.Breakdown, OrgSummaryBreakdown{
			TierID: b.TierID.String(), TierName: b.TierName, UnitPrice: b.UnitPrice,
			PromoCode: promo, Tickets: b.Tickets, Discount: b.Discount, Revenue: b.Revenue,
		})
	}
	for _, p := range promos {
		card(p.Currency).Promos = append(card(p.Currency).Promos, p)
	}
	out := make([]OrgSummaryCard, 0, len(cards))
	for _, c := range cards {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// OrgSummarySessionIDs lists the session ids of a summary, in its order.
func OrgSummarySessionIDs(s OrgSummary) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(s.Sessions))
	for _, x := range s.Sessions {
		if id, err := uuid.Parse(x.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// LoadOrgSummary reads every aggregate of the organization's sessions that
// match the filter and folds them like HandleOrgSummary does. limit caps the
// sessions read (OrgSummaryMaxSessions / OrgAccountingMaxSessions). On a
// failure the string names the part, for the log line.
func LoadOrgSummary(ctx context.Context, q *gen.Queries, orgID uuid.UUID, f OrgSummaryFilter, limit int32) (OrgSummary, string, error) {
	sessions, err := q.ListOrgSummarySessions(ctx, orgID, f.From, f.To, f.EventID, limit+1)
	if err != nil {
		return OrgSummary{}, "sessions", err
	}
	rows := orgSummaryRows{}
	if len(sessions) > int(limit) {
		sessions = sessions[:limit]
		rows.truncated = true
	}
	rows.sessions = sessions
	if len(sessions) == 0 {
		return buildOrgSummary(rows), "", nil
	}
	ids := make([]uuid.UUID, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	if rows.money.orders, err = q.ListSessionSummaryOrders(ctx, ids); err != nil {
		return OrgSummary{}, "orders", err
	}
	if rows.money.refunds, err = q.ListSessionSummaryRefunds(ctx, ids); err != nil {
		return OrgSummary{}, "refunds", err
	}
	if rows.money.promos, err = q.ListSessionSummaryPromos(ctx, ids); err != nil {
		return OrgSummary{}, "promos", err
	}
	if rows.money.testOrders, err = q.ListSessionSummaryTestOrders(ctx, ids); err != nil {
		return OrgSummary{}, "test_orders", err
	}
	if rows.tickets, err = q.ListSessionSummaryTicketCards(ctx, ids); err != nil {
		return OrgSummary{}, "tickets", err
	}
	if rows.breakdown, err = q.ListSessionSummaryBreakdown(ctx, ids); err != nil {
		return OrgSummary{}, "breakdown", err
	}
	return buildOrgSummary(rows), "", nil
}

// ParseOrgSummaryFilter reads ?from=&to=&event_id=. A bound is a calendar
// date (YYYY-MM-DD, UTC; `to` is the last day INCLUDED) or an RFC 3339 instant
// (`to` then exclusive). It returns the error code and message for a refusal.
func ParseOrgSummaryFilter(r *http.Request) (OrgSummaryFilter, string, string) {
	var f OrgSummaryFilter
	q := r.URL.Query()
	parse := func(raw string, endOfDay bool) (*time.Time, bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, true
		}
		if t, err := time.Parse("2006-01-02", raw); err == nil { // allow:timeformat: a calendar date from the query
			if endOfDay {
				t = t.AddDate(0, 0, 1)
			}
			return &t, true
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			t = t.UTC()
			return &t, true
		}
		return nil, false
	}
	var ok bool
	if f.From, ok = parse(q.Get("from"), false); !ok {
		return f, "summary.invalid_range", "from must be a date (YYYY-MM-DD) or an RFC 3339 time"
	}
	if f.To, ok = parse(q.Get("to"), true); !ok {
		return f, "summary.invalid_range", "to must be a date (YYYY-MM-DD) or an RFC 3339 time"
	}
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		return f, "summary.invalid_range", "to must not be before from"
	}
	if raw := strings.TrimSpace(q.Get("event_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return f, "summary.invalid_event_id", "event_id must be a UUID"
		}
		f.EventID = &id
	}
	return f, "", ""
}

// HandleOrgSummary serves GET /v1/organizations/{org_id}/summary.
func (h *Handler) HandleOrgSummary(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	f, code, msg := ParseOrgSummaryFilter(r)
	if code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	out, part, err := LoadOrgSummary(r.Context(), h.queries, orgID, f, OrgSummaryMaxSessions)
	if err != nil {
		h.summaryFailure(w, r, "organization", part, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
