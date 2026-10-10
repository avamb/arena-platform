package horders

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
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

// PlaceCounts are the places of a session (or of one category) by status.
// SoldUpstream is the part of Sold that was sold in the system the session was
// imported from: no ticket, order or money stands behind it in arena.
type PlaceCounts struct {
	Total        int64 `json:"total"`
	Available    int64 `json:"available"`
	Held         int64 `json:"held"`
	Sold         int64 `json:"sold"`
	SoldUpstream int64 `json:"sold_upstream"`
	Unavailable  int64 `json:"unavailable"`
}

func (p *PlaceCounts) add(r gen.SessionSummaryPlacesRow) {
	p.Available += r.Available
	p.Held += r.Held
	p.Sold += r.Sold
	p.SoldUpstream += r.SoldUpstream
	p.Unavailable += r.Unavailable
	p.Total += r.Available + r.Held + r.Sold + r.Unavailable
}

type SummarySession struct {
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

type SummaryPlaces struct {
	Seats PlaceCounts `json:"seats"`
	GA    PlaceCounts `json:"ga"`
}

type SummaryTier struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	PriceAmount int64       `json:"price_amount"`
	Currency    string      `json:"currency"`
	IsOpen      bool        `json:"is_open"`
	Places      PlaceCounts `json:"places"`
	PaidItems   int64       `json:"paid_items"`
	PaidRevenue int64       `json:"paid_revenue"`
}

// SummaryMoney is the bottom line of one currency, in minor units. Paid covers
// every order that was paid at some point (paid, partially_refunded,
// refunded); Refunded counts succeeded refunds only; Net = Paid - Refunded.
type SummaryMoney struct {
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

type SummaryOrders struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Currency string `json:"currency"`
	Orders   int64  `json:"orders"`
	Total    int64  `json:"total"`
}

type SummaryRefunds struct {
	Settlement string `json:"settlement"`
	State      string `json:"state"`
	Currency   string `json:"currency"`
	Refunds    int64  `json:"refunds"`
	Amount     int64  `json:"amount"`
}

// SummaryPromo is one promo code's share of the session's paid orders.
type SummaryPromo struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Currency string `json:"currency"`
	Orders   int64  `json:"orders"`
	Discount int64  `json:"discount"`
}

type SessionSummary struct {
	Session SummarySession               `json:"session"`
	Places  SummaryPlaces                `json:"places"`
	Tiers   []SummaryTier                `json:"tiers"`
	Money   []SummaryMoney               `json:"money"`
	Orders  []SummaryOrders              `json:"orders"`
	Tickets gen.SessionSummaryTicketsRow `json:"tickets"`
	Refunds []SummaryRefunds             `json:"refunds"`
	Promos  []SummaryPromo               `json:"promos"`
}

// orderWasPaid reports whether an order of this status took the buyer's money
// at some point — a refunded order did, and its refund is accounted separately.
func orderWasPaid(status string) bool {
	return status == "paid" || status == "partially_refunded" || status == "refunded"
}

// BuildSessionSummary folds the raw aggregates into the response. Pure, so
// the arithmetic is unit-tested without a database.
func BuildSessionSummary(
	header gen.SessionSummaryHeaderRow,
	places []gen.SessionSummaryPlacesRow,
	tiers []gen.SessionSummaryTierRow,
	orders []gen.SessionSummaryOrdersRow,
	tickets gen.SessionSummaryTicketsRow,
	refunds []gen.SessionSummaryRefundsRow,
	promos []gen.SessionSummaryPromoRow,
) SessionSummary {
	out := SessionSummary{
		Session: SummarySession{
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
		Tiers:   make([]SummaryTier, 0, len(tiers)),
		Money:   []SummaryMoney{},
		Orders:  make([]SummaryOrders, 0, len(orders)),
		Tickets: tickets,
		Refunds: make([]SummaryRefunds, 0, len(refunds)),
		Promos:  make([]SummaryPromo, 0, len(promos)),
	}
	for _, p := range promos {
		out.Promos = append(out.Promos, SummaryPromo{
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
		counts   PlaceCounts
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
		st := SummaryTier{
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

	money := map[string]*SummaryMoney{}
	moneyFor := func(currency string) *SummaryMoney {
		m := money[currency]
		if m == nil {
			m = &SummaryMoney{Currency: currency}
			money[currency] = m
		}
		return m
	}
	for _, o := range orders {
		out.Orders = append(out.Orders, SummaryOrders{
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
		out.Refunds = append(out.Refunds, SummaryRefunds{
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

	summary, part, err := LoadSessionSummary(ctx, h.queries, sessionID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	if err != nil {
		h.logger.Error("horders: session summary failed",
			slog.String("part", part), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to build the session summary", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, summary)
}

// LoadSessionSummary reads every aggregate of one session, scoped to the
// organization, and folds them with BuildSessionSummary. It is shared by the
// JSON route and the CSV export (hexport), so the two can never disagree. A
// session that is not this organization's answers pgx.ErrNoRows; any other
// error names the failing part for the log.
func LoadSessionSummary(ctx context.Context, q *gen.Queries, sessionID, orgID uuid.UUID) (SessionSummary, string, error) {
	header, err := q.GetSessionSummaryHeader(ctx, sessionID, orgID)
	if err != nil {
		return SessionSummary{}, "header", err
	}
	places, err := q.ListSessionSummaryPlaces(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "places", err
	}
	tiers, err := q.ListSessionSummaryTiers(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "tiers", err
	}
	orders, err := q.ListSessionSummaryOrders(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "orders", err
	}
	tickets, err := q.GetSessionSummaryTickets(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "tickets", err
	}
	refunds, err := q.ListSessionSummaryRefunds(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "refunds", err
	}
	promos, err := q.ListSessionSummaryPromos(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, "promos", err
	}
	return BuildSessionSummary(header, places, tiers, orders, tickets, refunds, promos), "", nil
}
