package horders

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// summary_core.go is the ONE assembly behind the session summary
// (session_summary.go) and the event summary (event_summary.go, EC-06). Both
// screens load the same rows for a set of sessions and fold them with
// buildSummaryCore; the event summary runs it once over every session for
// the totals and once per session for the breakdown. Add a new aggregate
// HERE, never to one of the two handlers alone.

// placeCounts are the places of a session (or of one category) by status.
// SoldUpstream is the part of Sold that was sold in the system the session was
// imported from: no ticket, order or money stands behind it in arena.
type placeCounts struct {
	Total        int64 `json:"total"`
	Available    int64 `json:"available"`
	Held         int64 `json:"held"`
	Sold         int64 `json:"sold"`
	SoldUpstream int64 `json:"sold_upstream"`
	Unavailable  int64 `json:"unavailable"`
}

func (p *placeCounts) add(r gen.SessionSummaryPlacesRow) {
	p.Available += r.Available
	p.Held += r.Held
	p.Sold += r.Sold
	p.SoldUpstream += r.SoldUpstream
	p.Unavailable += r.Unavailable
	p.Total += r.Available + r.Held + r.Sold + r.Unavailable
}

func (p *placeCounts) addCounts(o placeCounts) {
	p.Total += o.Total
	p.Available += o.Available
	p.Held += o.Held
	p.Sold += o.Sold
	p.SoldUpstream += o.SoldUpstream
	p.Unavailable += o.Unavailable
}

type summaryPlaces struct {
	Seats placeCounts `json:"seats"`
	GA    placeCounts `json:"ga"`
}

type summaryTier struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	PriceAmount int64       `json:"price_amount"`
	Currency    string      `json:"currency"`
	IsOpen      bool        `json:"is_open"`
	Places      placeCounts `json:"places"`
	PaidItems   int64       `json:"paid_items"`
	PaidRevenue int64       `json:"paid_revenue"`
}

// summaryMoney is the bottom line of one currency, in minor units. Paid covers
// every order that was paid at some point (paid, partially_refunded,
// refunded); Refunded counts succeeded refunds only; Net = Paid - Refunded.
type summaryMoney struct {
	Currency      string `json:"currency"`
	PaidOrders    int64  `json:"paid_orders"`
	Paid          int64  `json:"paid"`
	ServiceCharge int64  `json:"service_charge"`
	Discount      int64  `json:"discount"`
	Refunded      int64  `json:"refunded"`
	Net           int64  `json:"net"`
	PendingOrders int64  `json:"pending_orders"`
	Pending       int64  `json:"pending"`
}

type summaryOrders struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Currency string `json:"currency"`
	Orders   int64  `json:"orders"`
	Total    int64  `json:"total"`
}

// summaryTickets counts tickets by status. Used and Complimentary are
// subsets of Active; Complimentary here is the admin complimentary flow
// only — see summaryComplimentary for every invitation path.
type summaryTickets struct {
	Active        int64 `json:"active"`
	Cancelled     int64 `json:"cancelled"`
	Transferred   int64 `json:"transferred"`
	Used          int64 `json:"used"`
	Complimentary int64 `json:"complimentary"`
}

// summaryEntered is the door count: Used valid tickets were scanned, Total
// is every valid ticket ("entered N of M").
type summaryEntered struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
}

type summaryRefunds struct {
	Settlement string `json:"settlement"`
	State      string `json:"state"`
	Currency   string `json:"currency"`
	Refunds    int64  `json:"refunds"`
	Amount     int64  `json:"amount"`
}

// summaryPromo is one promo code's share of the paid orders. Orders counts
// the paid orders carrying the code; Redemptions the recorded
// promo_code_redemptions rows among them (at most Orders).
type summaryPromo struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Currency    string `json:"currency"`
	Orders      int64  `json:"orders"`
	Redemptions int64  `json:"redemptions"`
	Discount    int64  `json:"discount"`
}

// summaryComplimentary counts invitations whichever way they were issued:
// paid orders with source='complimentary' and the active tickets of either
// invitation path.
type summaryComplimentary struct {
	Orders  int64 `json:"orders"`
	Tickets int64 `json:"tickets"`
}

// summaryCore is the part of the overview both screens share.
type summaryCore struct {
	Places        summaryPlaces        `json:"places"`
	Tiers         []summaryTier        `json:"tiers"`
	Money         []summaryMoney       `json:"money"`
	Orders        []summaryOrders      `json:"orders"`
	Tickets       summaryTickets       `json:"tickets"`
	Entered       summaryEntered       `json:"entered"`
	Refunds       []summaryRefunds     `json:"refunds"`
	Promos        []summaryPromo       `json:"promos"`
	Complimentary summaryComplimentary `json:"complimentary"`
}

// orderWasPaid reports whether an order of this status took the buyer's money
// at some point — a refunded order did, and its refund is accounted separately.
func orderWasPaid(status string) bool {
	return status == "paid" || status == "partially_refunded" || status == "refunded"
}

// summaryRows is everything the summary queries return for a set of
// sessions, every row tagged with its session.
type summaryRows struct {
	places        []gen.SessionSummaryPlacesRow
	tiers         []gen.SessionSummaryTierRow
	orders        []gen.SessionSummaryOrdersRow
	tickets       []gen.SessionSummaryTicketsRow
	refunds       []gen.SessionSummaryRefundsRow
	promos        []gen.SessionSummaryPromoRow
	complimentary []gen.SessionSummaryComplimentaryRow
}

// loadSummaryRows runs every summary query for the given sessions. On a
// failure it names the part that failed, for the log line.
func (h *Handler) loadSummaryRows(ctx context.Context, sessionIDs []uuid.UUID) (summaryRows, string, error) {
	var (
		r   summaryRows
		err error
	)
	if r.places, err = h.queries.ListSessionSummaryPlaces(ctx, sessionIDs); err != nil {
		return r, "places", err
	}
	if r.tiers, err = h.queries.ListSessionSummaryTiers(ctx, sessionIDs); err != nil {
		return r, "tiers", err
	}
	if r.orders, err = h.queries.ListSessionSummaryOrders(ctx, sessionIDs); err != nil {
		return r, "orders", err
	}
	if r.tickets, err = h.queries.ListSessionSummaryTickets(ctx, sessionIDs); err != nil {
		return r, "tickets", err
	}
	if r.refunds, err = h.queries.ListSessionSummaryRefunds(ctx, sessionIDs); err != nil {
		return r, "refunds", err
	}
	if r.promos, err = h.queries.ListSessionSummaryPromos(ctx, sessionIDs); err != nil {
		return r, "promos", err
	}
	if r.complimentary, err = h.queries.ListSessionSummaryComplimentary(ctx, sessionIDs); err != nil {
		return r, "complimentary", err
	}
	return r, "", nil
}

// forSession keeps only the rows of one session.
func (r summaryRows) forSession(id uuid.UUID) summaryRows {
	var out summaryRows
	for _, x := range r.places {
		if x.SessionID == id {
			out.places = append(out.places, x)
		}
	}
	for _, x := range r.tiers {
		if x.SessionID == id {
			out.tiers = append(out.tiers, x)
		}
	}
	for _, x := range r.orders {
		if x.SessionID == id {
			out.orders = append(out.orders, x)
		}
	}
	for _, x := range r.tickets {
		if x.SessionID == id {
			out.tickets = append(out.tickets, x)
		}
	}
	for _, x := range r.refunds {
		if x.SessionID == id {
			out.refunds = append(out.refunds, x)
		}
	}
	for _, x := range r.promos {
		if x.SessionID == id {
			out.promos = append(out.promos, x)
		}
	}
	for _, x := range r.complimentary {
		if x.SessionID == id {
			out.complimentary = append(out.complimentary, x)
		}
	}
	return out
}

// buildSummaryCore folds the raw aggregates of any number of sessions into
// the shared part of the overview. Pure, so the arithmetic is unit-tested
// without a database. Rows of the same group (an order status, a refund
// state, a promo code) coming from different sessions are merged.
func buildSummaryCore(r summaryRows) summaryCore {
	out := summaryCore{
		Tiers:   make([]summaryTier, 0, len(r.tiers)),
		Money:   []summaryMoney{},
		Orders:  []summaryOrders{},
		Refunds: []summaryRefunds{},
		Promos:  []summaryPromo{},
	}
	out.Tiers = foldTiers(r.places, r.tiers, &out.Places)
	out.Orders, out.Refunds, out.Money = foldMoney(r.orders, r.refunds)
	out.Promos = foldPromos(r.promos)
	for _, t := range r.tickets {
		out.Tickets.Active += t.Active
		out.Tickets.Cancelled += t.Cancelled
		out.Tickets.Transferred += t.Transferred
		out.Tickets.Used += t.Used
		out.Tickets.Complimentary += t.Complimentary
	}
	out.Entered = summaryEntered{Used: out.Tickets.Used, Total: out.Tickets.Active}
	for _, c := range r.complimentary {
		out.Complimentary.Orders += c.Orders
		out.Complimentary.Tickets += c.Tickets
	}
	return out
}

// foldTiers sums the places of the hall into whole and attaches each
// category its own places, keyed by tier id. A place no category owns still
// counts in the hall total.
func foldTiers(places []gen.SessionSummaryPlacesRow, tiers []gen.SessionSummaryTierRow, whole *summaryPlaces) []summaryTier {
	type tierPlaces struct {
		counts   placeCounts
		hasSeats bool
	}
	byTier := map[uuid.UUID]*tierPlaces{}
	for _, p := range places {
		if p.Kind == "ga_unit" {
			whole.GA.add(p)
		} else {
			whole.Seats.add(p)
		}
		if p.TierID == nil {
			continue
		}
		tp := byTier[*p.TierID]
		if tp == nil {
			tp = &tierPlaces{}
			byTier[*p.TierID] = tp
		}
		tp.counts.add(p)
		if p.Kind != "ga_unit" {
			tp.hasSeats = true
		}
	}
	out := make([]summaryTier, 0, len(tiers))
	for _, t := range tiers {
		st := summaryTier{
			ID:          t.ID.String(),
			Name:        t.Name,
			Kind:        "ga",
			PriceAmount: t.PriceAmount,
			Currency:    t.Currency,
			IsOpen:      t.IsOpen,
			PaidItems:   t.PaidItems,
			PaidRevenue: t.PaidRevenue,
		}
		if tp := byTier[t.ID]; tp != nil {
			st.Places = tp.counts
			if tp.hasSeats {
				st.Kind = "seated"
			}
		}
		out = append(out, st)
	}
	return out
}

// foldMoney merges the order and refund groups across sessions and derives
// the per-currency bottom line from them.
func foldMoney(orders []gen.SessionSummaryOrdersRow, refunds []gen.SessionSummaryRefundsRow) ([]summaryOrders, []summaryRefunds, []summaryMoney) {
	money := map[string]*summaryMoney{}
	moneyFor := func(currency string) *summaryMoney {
		m := money[currency]
		if m == nil {
			m = &summaryMoney{Currency: currency}
			money[currency] = m
		}
		return m
	}

	type orderKey struct{ status, source, currency string }
	orderGroups := map[orderKey]*summaryOrders{}
	for _, o := range orders {
		k := orderKey{o.Status, o.Source, o.Currency}
		g := orderGroups[k]
		if g == nil {
			g = &summaryOrders{Status: o.Status, Source: o.Source, Currency: o.Currency}
			orderGroups[k] = g
		}
		g.Orders += o.Orders
		g.Total += o.Total
		m := moneyFor(o.Currency)
		switch {
		case orderWasPaid(o.Status):
			m.PaidOrders += o.Orders
			m.Paid += o.Total
			m.ServiceCharge += o.Charge
			m.Discount += o.Discount
		case o.Status == "pending_payment":
			m.PendingOrders += o.Orders
			m.Pending += o.Total
		}
	}
	type refundKey struct{ settlement, state, currency string }
	refundGroups := map[refundKey]*summaryRefunds{}
	for _, r := range refunds {
		k := refundKey{r.Settlement, r.State, r.Currency}
		g := refundGroups[k]
		if g == nil {
			g = &summaryRefunds{Settlement: r.Settlement, State: r.State, Currency: r.Currency}
			refundGroups[k] = g
		}
		g.Refunds += r.Refunds
		g.Amount += r.Amount
		if r.State == "succeeded" {
			moneyFor(r.Currency).Refunded += r.Amount
		}
	}

	outOrders := make([]summaryOrders, 0, len(orderGroups))
	for _, g := range orderGroups {
		outOrders = append(outOrders, *g)
	}
	sort.Slice(outOrders, func(i, j int) bool {
		a, b := outOrders[i], outOrders[j]
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Currency < b.Currency
	})
	outRefunds := make([]summaryRefunds, 0, len(refundGroups))
	for _, g := range refundGroups {
		outRefunds = append(outRefunds, *g)
	}
	sort.Slice(outRefunds, func(i, j int) bool {
		a, b := outRefunds[i], outRefunds[j]
		if a.Settlement != b.Settlement {
			return a.Settlement < b.Settlement
		}
		if a.State != b.State {
			return a.State < b.State
		}
		return a.Currency < b.Currency
	})
	outMoney := make([]summaryMoney, 0, len(money))
	for _, m := range money {
		m.Net = m.Paid - m.Refunded
		outMoney = append(outMoney, *m)
	}
	sort.Slice(outMoney, func(i, j int) bool { return outMoney[i].Currency < outMoney[j].Currency })
	return outOrders, outRefunds, outMoney
}

// foldPromos merges one promo code's rows across sessions and currencies.
func foldPromos(promos []gen.SessionSummaryPromoRow) []summaryPromo {
	type promoKey struct {
		id       uuid.UUID
		currency string
	}
	groups := map[promoKey]*summaryPromo{}
	for _, p := range promos {
		k := promoKey{p.PromoCodeID, p.Currency}
		g := groups[k]
		if g == nil {
			g = &summaryPromo{ID: p.PromoCodeID.String(), Code: p.Code, Currency: p.Currency}
			groups[k] = g
		}
		g.Orders += p.Orders
		g.Redemptions += p.Redemptions
		g.Discount += p.Discount
	}
	out := make([]summaryPromo, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Currency < b.Currency
	})
	return out
}
