package horders

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The session summary is the one-screen answer to "how is this session
// doing": places by status, categories with what was paid for them, orders,
// tickets, refunds and the money per currency. It is read-only, scoped to one
// organization, and carries no buyer data — counts, amounts and ids only.
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/summary   (order.read)

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

type summarySession struct {
	ID             string  `json:"id"`
	EventID        string  `json:"event_id"`
	OrgID          string  `json:"org_id"`
	EventName      string  `json:"event_name"`
	StartAt        string  `json:"start_at"`
	Status         string  `json:"status"`
	CapacityTotal  int32   `json:"capacity_total"`
	HasSeatingPlan bool    `json:"has_seating_plan"`
	VenueName      *string `json:"venue_name"`
	VenueTimezone  *string `json:"venue_timezone"`
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

type summaryRefunds struct {
	Settlement string `json:"settlement"`
	State      string `json:"state"`
	Currency   string `json:"currency"`
	Refunds    int64  `json:"refunds"`
	Amount     int64  `json:"amount"`
}

// summaryPromo is one promo code's share of the session's paid orders.
type summaryPromo struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Currency string `json:"currency"`
	Orders   int64  `json:"orders"`
	Discount int64  `json:"discount"`
}

type sessionSummary struct {
	Session summarySession               `json:"session"`
	Places  summaryPlaces                `json:"places"`
	Tiers   []summaryTier                `json:"tiers"`
	Money   []summaryMoney               `json:"money"`
	Orders  []summaryOrders              `json:"orders"`
	Tickets gen.SessionSummaryTicketsRow `json:"tickets"`
	Refunds []summaryRefunds             `json:"refunds"`
	Promos  []summaryPromo               `json:"promos"`
}

// orderWasPaid reports whether an order of this status took the buyer's money
// at some point — a refunded order did, and its refund is accounted separately.
func orderWasPaid(status string) bool {
	return status == "paid" || status == "partially_refunded" || status == "refunded"
}

// buildSessionSummary folds the raw aggregates into the response. Pure, so
// the arithmetic is unit-tested without a database.
func buildSessionSummary(
	header gen.SessionSummaryHeaderRow,
	places []gen.SessionSummaryPlacesRow,
	tiers []gen.SessionSummaryTierRow,
	orders []gen.SessionSummaryOrdersRow,
	tickets gen.SessionSummaryTicketsRow,
	refunds []gen.SessionSummaryRefundsRow,
	promos []gen.SessionSummaryPromoRow,
) sessionSummary {
	out := sessionSummary{
		Session: summarySession{
			ID:             header.ID.String(),
			EventID:        header.EventID.String(),
			OrgID:          header.OrgID.String(),
			EventName:      header.EventName,
			StartAt:        header.StartAt.UTC().Format(time.RFC3339),
			Status:         header.Status,
			CapacityTotal:  header.CapacityTotal,
			HasSeatingPlan: header.SeatingPlanVersionID != nil,
			VenueName:      header.VenueName,
			VenueTimezone:  header.VenueTimezone,
		},
		Tiers:   make([]summaryTier, 0, len(tiers)),
		Money:   []summaryMoney{},
		Orders:  make([]summaryOrders, 0, len(orders)),
		Tickets: tickets,
		Refunds: make([]summaryRefunds, 0, len(refunds)),
		Promos:  make([]summaryPromo, 0, len(promos)),
	}
	for _, p := range promos {
		out.Promos = append(out.Promos, summaryPromo{
			ID: p.PromoCodeID.String(), Code: p.Code, Currency: p.Currency,
			Orders: p.Orders, Discount: p.Discount,
		})
	}
	sort.Slice(out.Promos, func(i, j int) bool {
		a, b := out.Promos[i], out.Promos[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Currency < b.Currency
	})

	type tierPlaces struct {
		counts   placeCounts
		hasSeats bool
	}
	byTier := map[string]*tierPlaces{}
	for _, p := range places {
		if p.Kind == "ga_unit" {
			out.Places.GA.add(p)
		} else {
			out.Places.Seats.add(p)
		}
		if p.TierID == nil {
			continue
		}
		tp := byTier[p.TierID.String()]
		if tp == nil {
			tp = &tierPlaces{}
			byTier[p.TierID.String()] = tp
		}
		tp.counts.add(p)
		if p.Kind != "ga_unit" {
			tp.hasSeats = true
		}
	}

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
		if tp := byTier[st.ID]; tp != nil {
			st.Places = tp.counts
			if tp.hasSeats {
				st.Kind = "seated"
			}
		}
		out.Tiers = append(out.Tiers, st)
	}

	money := map[string]*summaryMoney{}
	moneyFor := func(currency string) *summaryMoney {
		m := money[currency]
		if m == nil {
			m = &summaryMoney{Currency: currency}
			money[currency] = m
		}
		return m
	}
	for _, o := range orders {
		out.Orders = append(out.Orders, summaryOrders{
			Status: o.Status, Source: o.Source, Currency: o.Currency,
			Orders: o.Orders, Total: o.Total,
		})
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
	for _, r := range refunds {
		out.Refunds = append(out.Refunds, summaryRefunds{
			Settlement: r.Settlement, State: r.State, Currency: r.Currency,
			Refunds: r.Refunds, Amount: r.Amount,
		})
		if r.State == "succeeded" {
			moneyFor(r.Currency).Refunded += r.Amount
		}
	}
	for _, m := range money {
		m.Net = m.Paid - m.Refunded
		out.Money = append(out.Money, *m)
	}
	sort.Slice(out.Money, func(i, j int) bool { return out.Money[i].Currency < out.Money[j].Currency })
	sort.Slice(out.Orders, func(i, j int) bool {
		a, b := out.Orders[i], out.Orders[j]
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Currency < b.Currency
	})
	sort.Slice(out.Refunds, func(i, j int) bool {
		a, b := out.Refunds[i], out.Refunds[j]
		if a.Settlement != b.Settlement {
			return a.Settlement < b.Settlement
		}
		if a.State != b.State {
			return a.State < b.State
		}
		return a.Currency < b.Currency
	})
	return out
}

// HandleSessionSummary serves
// GET /v1/organizations/{org_id}/sessions/{session_id}/summary. A session of
// another organization is indistinguishable from a missing one (404).
func (h *Handler) HandleSessionSummary(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()

	header, err := h.queries.GetSessionSummaryHeader(ctx, sessionID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	fail := func(what string, err error) {
		h.logger.Error("horders: session summary failed",
			slog.String("part", what), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to build the session summary", r))
	}
	if err != nil {
		fail("header", err)
		return
	}
	places, err := h.queries.ListSessionSummaryPlaces(ctx, sessionID)
	if err != nil {
		fail("places", err)
		return
	}
	tiers, err := h.queries.ListSessionSummaryTiers(ctx, sessionID)
	if err != nil {
		fail("tiers", err)
		return
	}
	orders, err := h.queries.ListSessionSummaryOrders(ctx, sessionID)
	if err != nil {
		fail("orders", err)
		return
	}
	tickets, err := h.queries.GetSessionSummaryTickets(ctx, sessionID)
	if err != nil {
		fail("tickets", err)
		return
	}
	refunds, err := h.queries.ListSessionSummaryRefunds(ctx, sessionID)
	if err != nil {
		fail("refunds", err)
		return
	}
	promos, err := h.queries.ListSessionSummaryPromos(ctx, sessionID)
	if err != nil {
		fail("promos", err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK,
		buildSessionSummary(header, places, tiers, orders, tickets, refunds, promos))
}
