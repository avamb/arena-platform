// promo_scope_info.go — makes a promo code's scope readable and checkable:
// which events and dates it works on, and the mistakes an organizer can spot
// before a buyer does (a validity window that ends before sales do, a code
// bound to a cancelled or past date). The same calculation feeds the
// promo-code answers (`sessions`, `warnings`) and the buyer-facing refusal
// "the code works on: <events>".
package hcheckout

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Warning codes. A warning never blocks a save; it tells the organizer what
// the code will do to a buyer.
const (
	PromoWarnUntilBeforeSalesEnd = "promo.warning.valid_until_before_sales_end"
	PromoWarnFromAfterSalesStart = "promo.warning.valid_from_after_sales_start"
	PromoWarnSessionCancelled    = "promo.warning.session_cancelled"
	PromoWarnSessionPast         = "promo.warning.session_past"
)

// promoScopeSession is one session a code is bound to, as the organizer reads it.
type promoScopeSession struct {
	SessionID  string  `json:"session_id"`
	EventID    string  `json:"event_id"`
	EventName  string  `json:"event_name"`
	StartAt    string  `json:"start_at"`
	SalesEndAt string  `json:"sales_end_at"`
	Timezone   string  `json:"timezone"`
	Status     string  `json:"status"`
	SalesStart *string `json:"sales_start_at"`
}

// promoWarning is one non-blocking finding. SessionID names the session the
// finding is about; EventName and StartAt let a client word it without a
// second lookup.
type promoWarning struct {
	Code      string `json:"code"`
	SessionID string `json:"session_id"`
	EventName string `json:"event_name"`
	StartAt   string `json:"start_at"`
}

func rfc3339UTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// promoScopeSessions projects session rows onto the response shape. A deleted
// session is reported as cancelled: for a buyer it is gone.
func promoScopeSessions(rows []gen.PromoScopeSessionRow) []promoScopeSession {
	out := make([]promoScopeSession, 0, len(rows))
	for _, r := range rows {
		st := r.SessionStatus
		if r.Deleted || r.EventDeleted {
			st = "cancelled"
		}
		s := promoScopeSession{
			SessionID: r.SessionID.String(), EventID: r.EventID.String(), EventName: r.EventName,
			StartAt: rfc3339UTC(r.StartAt), SalesEndAt: rfc3339UTC(r.SalesEndAt),
			Timezone: r.Timezone, Status: st,
		}
		if r.SalesStartAt != nil {
			v := rfc3339UTC(*r.SalesStartAt)
			s.SalesStart = &v
		}
		out = append(out, s)
	}
	return out
}

// PromoWarnings is the pure calculation behind the `warnings` field. validFrom
// and validUntil are the code's window (nil = open); rows are the sessions it
// is bound to (empty scope = whole organization = nothing to compare against).
func PromoWarnings(validFrom, validUntil *time.Time, rows []gen.PromoScopeSessionRow, now time.Time) []promoWarning {
	out := []promoWarning{}
	for _, r := range rows {
		add := func(code string) {
			out = append(out, promoWarning{
				Code: code, SessionID: r.SessionID.String(),
				EventName: r.EventName, StartAt: rfc3339UTC(r.StartAt),
			})
		}
		if r.Deleted || r.EventDeleted || r.SessionStatus == "cancelled" || r.EventStatus == "cancelled" {
			add(PromoWarnSessionCancelled)
			continue
		}
		if r.StartAt.Before(now) {
			add(PromoWarnSessionPast)
			continue
		}
		if validUntil != nil && validUntil.Before(r.SalesEndAt) {
			add(PromoWarnUntilBeforeSalesEnd)
		}
		if validFrom != nil {
			start := r.SalesEndAt
			if r.SalesStartAt != nil {
				start = *r.SalesStartAt
			}
			if validFrom.After(start) {
				add(PromoWarnFromAfterSalesStart)
			}
		}
	}
	return out
}

// loadPromoScopeRows reads the sessions of the given codes in ONE query,
// keyed by session id. A failed read is logged by the caller's convention and
// yields an empty map: the code list must not fail because a hint is missing.
func (h *Handler) loadPromoScopeRows(ctx context.Context, orgID uuid.UUID, codes []gen.PromoCodeRow) map[string]gen.PromoScopeSessionRow {
	set := map[string]struct{}{}
	for _, c := range codes {
		for _, s := range c.AppliesToSessionIDs {
			set[s] = struct{}{}
		}
	}
	out := map[string]gen.PromoScopeSessionRow{}
	if len(set) == 0 {
		return out
	}
	ids := make([]string, 0, len(set))
	for s := range set {
		ids = append(ids, s)
	}
	rows, err := h.promoQueries.ListPromoScopeSessions(ctx, orgID, ids)
	if err != nil {
		h.logger.Error("promo: scope lookup failed")
		return out
	}
	for _, r := range rows {
		out[r.SessionID.String()] = r
	}
	return out
}

// promoCodeWithScope is promoCodeFromRow plus `sessions` and `warnings`.
func (h *Handler) promoCodeWithScope(
	pc gen.PromoCodeRow, usage *gen.PromoCodeUsageRow,
	rows map[string]gen.PromoScopeSessionRow, now time.Time,
) promoCodeResponse {
	resp := promoCodeFromRow(pc, usage)
	own := make([]gen.PromoScopeSessionRow, 0, len(pc.AppliesToSessionIDs))
	for _, s := range pc.AppliesToSessionIDs {
		if r, ok := rows[s]; ok {
			own = append(own, r)
		}
	}
	sort.SliceStable(own, func(i, j int) bool { return own[i].StartAt.Before(own[j].StartAt) })
	resp.Sessions = promoScopeSessions(own)
	resp.Warnings = PromoWarnings(pc.ValidFrom, pc.ValidUntil, own, now)
	return resp
}

// PromoScopeHint names the events a code works on, for a buyer who typed it
// into a cart it does not fit: "A, B". Only live sessions of published events
// whose sale is still open are named, so a draft or finished event never leaks
// through a code; an empty string means "nothing to say" and the caller keeps
// its generic refusal. The code is looked up inside the buyer's organization
// by the caller, so another organization's codes cannot reach this far.
func PromoScopeHint(ctx context.Context, q interface {
	ListPromoScopeSessions(ctx context.Context, orgID uuid.UUID, sessionIDs []string) ([]gen.PromoScopeSessionRow, error)
}, pc gen.PromoCodeRow, now time.Time) string {
	if len(pc.AppliesToSessionIDs) == 0 {
		return ""
	}
	rows, err := q.ListPromoScopeSessions(ctx, pc.OrgID, pc.AppliesToSessionIDs)
	if err != nil {
		return ""
	}
	return promoHintFromRows(rows, now)
}

func promoHintFromRows(rows []gen.PromoScopeSessionRow, now time.Time) string {
	seen := map[string]struct{}{}
	names := []string{}
	for _, r := range rows {
		if r.Deleted || r.EventDeleted || r.SessionStatus == "cancelled" || r.EventStatus != "published" {
			continue
		}
		if !r.SalesEndAt.After(now) {
			continue
		}
		n := strings.TrimSpace(r.EventName)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
		if len(names) == 5 {
			break
		}
	}
	return strings.Join(names, ", ")
}
