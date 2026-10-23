// org_charges.go - the organizer's read-only view of what the platform
// charged them (billing wave B-A, task B2-6, spec 41 section 6.9):
//
//	GET /v1/organizations/{org_id}/billing/charges?from&to&event_id&limit&cursor
//	GET /v1/organizations/{org_id}/billing/events/{event_id}/summary
//
// Both routes need billing.read inside the path organization (the server
// checks membership and the permission before the handler runs), read only
// charges whose billable event carries THAT organization, and answer the
// route's own 404 for an event of another organization. Every answer says
// `preliminary: true`: nothing is invoiced yet, the numbers are what the rate
// job has charged so far and a correction arrives as a reversal.
//
// Money is an integer in minor units with its currency beside it; sums are
// always per currency, never across currencies.
package hbilling

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	defaultChargesLimit = 50
	maxChargesLimit     = 200
)

// ChargesHandler serves the organizer's billing charges routes.
type ChargesHandler struct {
	queries *gen.Queries
	logger  *slog.Logger
}

// NewCharges builds the handler. A nil pool makes every route answer 503.
func NewCharges(pool *pgxpool.Pool, logger *slog.Logger) *ChargesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &ChargesHandler{logger: logger}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

// ── response shapes ────────────────────────────────────────────────────────

// ChargeItem is one charge. A reversal has reversal_of set and a negative
// amount; it carries the time of the event it reverses.
type ChargeItem struct {
	ID               uuid.UUID  `json:"id"`
	BillableEventID  uuid.UUID  `json:"billable_event_id"`
	Kind             string     `json:"kind"`
	OccurredAt       time.Time  `json:"occurred_at"`
	RatedAt          time.Time  `json:"rated_at"`
	EventID          *uuid.UUID `json:"event_id"`
	SessionID        *uuid.UUID `json:"session_id"`
	OrderID          *uuid.UUID `json:"order_id"`
	Currency         string     `json:"currency"`
	AmountMinor      int64      `json:"amount_minor"`
	RuleType         string     `json:"rule_type"`
	Reason           *string    `json:"reason"`
	ReversalOf       *uuid.UUID `json:"reversal_of"`
	PriceListVersion *int32     `json:"price_list_version"`
	PricingClass     *string    `json:"pricing_class"`
	Seated           *bool      `json:"seated"`
	SaleCurrency     *string    `json:"sale_currency"`
	UnitPriceMinor   *int64     `json:"unit_price_minor"`
}

// ChargeTotal is the sum of charges in one currency.
type ChargeTotal struct {
	Currency      string `json:"currency"`
	ChargedMinor  int64  `json:"charged_minor"`
	ReversedMinor int64  `json:"reversed_minor"`
	NetMinor      int64  `json:"net_minor"`
	Count         int64  `json:"count"`
}

// ChargeListResponse is the answer of the charges list.
type ChargeListResponse struct {
	Items       []ChargeItem  `json:"items"`
	Totals      []ChargeTotal `json:"totals"`
	NextCursor  *string       `json:"next_cursor"`
	Preliminary bool          `json:"preliminary"`
}

// ChargeRuleLine is the net charge of one rule type in one currency.
type ChargeRuleLine struct {
	RuleType string  `json:"rule_type"`
	Reason   *string `json:"reason"`
	Currency string  `json:"currency"`
	NetMinor int64   `json:"net_minor"`
	Count    int64   `json:"count"`
}

// ChargeSessionLine is the charge summary of one session in one currency.
type ChargeSessionLine struct {
	SessionID      *uuid.UUID `json:"session_id"`
	SessionStartAt *time.Time `json:"session_start_at"`
	Currency       string     `json:"currency"`
	ChargedMinor   int64      `json:"charged_minor"`
	ReversedMinor  int64      `json:"reversed_minor"`
	NetMinor       int64      `json:"net_minor"`
	Count          int64      `json:"count"`
}

// EventBillingSummaryResponse is the charge summary of one event.
type EventBillingSummaryResponse struct {
	EventID      uuid.UUID           `json:"event_id"`
	Totals       []ChargeTotal       `json:"totals"`
	ByRule       []ChargeRuleLine    `json:"by_rule"`
	BySession    []ChargeSessionLine `json:"by_session"`
	EventsByKind map[string]int64    `json:"events_by_kind"`
	Preliminary  bool                `json:"preliminary"`
}

// ── cursor ──────────────────────────────────────────────────────────────────

// encodeCursor packs the position after the last row of a page.
func encodeCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + "|" + id.String()))
}

// decodeCursor is the inverse; ok is false for anything encodeCursor did not
// produce.
func decodeCursor(s string) (at time.Time, id uuid.UUID, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	micro, rest, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, false
	}
	us, err := strconv.ParseInt(micro, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err = uuid.Parse(rest)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return time.UnixMicro(us).UTC(), id, true
}

// parseBoundary reads a from/to value: RFC 3339, or a plain date (midnight
// UTC).
func parseBoundary(v string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func (h *ChargesHandler) unavailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"billing.unavailable", "billing is not configured", r))
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", msg, r))
}

func (h *ChargesHandler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("billing charges: "+what, slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"billing.internal", "failed to read billing charges", r))
}

// eventInOrg answers whether eventID is an event of orgID. A foreign or
// unknown event writes the route's own 404 and returns false.
func (h *ChargesHandler) eventInOrg(w http.ResponseWriter, r *http.Request, orgID, eventID uuid.UUID) bool {
	owner, err := h.queries.GetBillingEventOrgID(r.Context(), eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("billing.event_not_found", "event not found", r))
			return false
		}
		h.internal(w, r, "event lookup", err)
		return false
	}
	if owner != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("billing.event_not_found", "event not found", r))
		return false
	}
	return true
}

// HandleListCharges serves GET /v1/organizations/{org_id}/billing/charges.
func (h *ChargesHandler) HandleListCharges(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		h.unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	var from, to *time.Time
	if v := q.Get("from"); v != "" {
		t, ok := parseBoundary(v)
		if !ok {
			badRequest(w, r, "from must be an RFC 3339 time or a YYYY-MM-DD date")
			return
		}
		from = &t
	}
	if v := q.Get("to"); v != "" {
		t, ok := parseBoundary(v)
		if !ok {
			badRequest(w, r, "to must be an RFC 3339 time or a YYYY-MM-DD date")
			return
		}
		to = &t
	}
	if from != nil && to != nil && !to.After(*from) {
		badRequest(w, r, "to must be after from")
		return
	}
	var eventID *uuid.UUID
	if v := q.Get("event_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			badRequest(w, r, "event_id must be a valid UUID")
			return
		}
		eventID = &id
	}
	limit := int32(defaultChargesLimit)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxChargesLimit {
			badRequest(w, r, fmt.Sprintf("limit must be between 1 and %d", maxChargesLimit))
			return
		}
		limit = int32(n)
	}
	var cursorAt *time.Time
	var cursorID *uuid.UUID
	if v := q.Get("cursor"); v != "" {
		at, id, ok := decodeCursor(v)
		if !ok {
			badRequest(w, r, "cursor is not valid")
			return
		}
		cursorAt, cursorID = &at, &id
	}
	if eventID != nil && !h.eventInOrg(w, r, orgID, *eventID) {
		return
	}

	ctx := r.Context()
	rows, err := h.queries.ListOrgCharges(ctx, orgID, from, to, eventID, cursorAt, cursorID, limit+1)
	if err != nil {
		h.internal(w, r, "list", err)
		return
	}
	totals, err := h.queries.SumOrgCharges(ctx, orgID, from, to, eventID)
	if err != nil {
		h.internal(w, r, "sums", err)
		return
	}
	resp := ChargeListResponse{Items: []ChargeItem{}, Totals: chargeTotals(totals), Preliminary: true}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		c := encodeCursor(last.OccurredAt, last.ChargeID)
		resp.NextCursor = &c
	}
	for _, c := range rows {
		resp.Items = append(resp.Items, ChargeItem{
			ID: c.ChargeID, BillableEventID: c.BillableEventID, Kind: c.Kind,
			OccurredAt: c.OccurredAt, RatedAt: c.RatedAt, EventID: c.EventID,
			SessionID: c.SessionID, OrderID: c.OrderID, Currency: c.Currency,
			AmountMinor: c.AmountMinor, RuleType: c.RuleType, Reason: c.Reason,
			ReversalOf: c.ReversalOf, PriceListVersion: c.PriceListVersion,
			PricingClass: c.PricingClass, Seated: c.Seated, SaleCurrency: c.SaleCurrency,
			UnitPriceMinor: c.UnitPriceMinor,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

func chargeTotals(rows []gen.ChargeTotalRow) []ChargeTotal {
	out := make([]ChargeTotal, 0, len(rows))
	for _, t := range rows {
		out = append(out, ChargeTotal{Currency: t.Currency, ChargedMinor: t.ChargedMinor,
			ReversedMinor: t.ReversedMinor, NetMinor: t.NetMinor, Count: t.Count})
	}
	return out
}

// HandleEventSummary serves GET /v1/organizations/{org_id}/billing/events/{event_id}/summary.
func (h *ChargesHandler) HandleEventSummary(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		h.unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		badRequest(w, r, "event_id must be a valid UUID")
		return
	}
	if !h.eventInOrg(w, r, orgID, eventID) {
		return
	}
	ctx := r.Context()
	totals, err := h.queries.SumOrgCharges(ctx, orgID, nil, nil, &eventID)
	if err != nil {
		h.internal(w, r, "event sums", err)
		return
	}
	rules, err := h.queries.ListEventChargesByRule(ctx, orgID, eventID)
	if err != nil {
		h.internal(w, r, "event rules", err)
		return
	}
	sessions, err := h.queries.ListEventChargesBySession(ctx, orgID, eventID)
	if err != nil {
		h.internal(w, r, "event sessions", err)
		return
	}
	kinds, err := h.queries.CountEventBillingKinds(ctx, orgID, eventID)
	if err != nil {
		h.internal(w, r, "event kinds", err)
		return
	}
	resp := EventBillingSummaryResponse{
		EventID: eventID, Totals: chargeTotals(totals), Preliminary: true,
		ByRule: []ChargeRuleLine{}, BySession: []ChargeSessionLine{}, EventsByKind: map[string]int64{},
	}
	for _, l := range rules {
		resp.ByRule = append(resp.ByRule, ChargeRuleLine{RuleType: l.RuleType, Reason: l.Reason,
			Currency: l.Currency, NetMinor: l.NetMinor, Count: l.Count})
	}
	for _, s := range sessions {
		resp.BySession = append(resp.BySession, ChargeSessionLine{SessionID: s.SessionID,
			SessionStartAt: s.SessionStartAt, Currency: s.Currency, ChargedMinor: s.ChargedMinor,
			ReversedMinor: s.ReversedMinor, NetMinor: s.NetMinor, Count: s.Count})
	}
	for _, k := range kinds {
		resp.EventsByKind[k.Kind] = k.Count
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}
