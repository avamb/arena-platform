package hreferral

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Reward states a list may be filtered by (referral_rewards.state).
var rewardStates = map[string]bool{
	"due": true, "sent": true, "paid": true, "capped": true,
	"not_possible": true, "failed": true, "cancelled": true,
}

type rewardJSON struct {
	ID                  string  `json:"id"`
	Code                string  `json:"code"`
	FriendOrderNumber   *int64  `json:"friend_order_number"`
	FriendAmountMinor   *int64  `json:"friend_amount_minor"`
	FriendCurrency      *string `json:"friend_currency"`
	ReferrerOrderNumber *int64  `json:"referrer_order_number"`
	CashbackMinor       int64   `json:"cashback_minor"`
	Currency            string  `json:"currency"`
	CashbackPct         int     `json:"cashback_pct"`
	State               string  `json:"state"`
	Reason              *string `json:"reason"`
	FriendReversal      *string `json:"friend_reversal"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

func toRewardJSON(r gen.ReferralRewardListRow) rewardJSON {
	return rewardJSON{
		ID: r.ID.String(), Code: r.Code, FriendOrderNumber: r.FriendOrderNumber,
		FriendAmountMinor: r.FriendAmountMinor, FriendCurrency: r.FriendCurrency,
		ReferrerOrderNumber: r.ReferrerOrderNumber, CashbackMinor: r.AmountMinor, Currency: r.Currency,
		CashbackPct: int(r.CashbackPct), State: r.State, Reason: r.Reason, FriendReversal: r.FriendReversal,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// encodeCursor makes the opaque keyset cursor of a row.
func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

// decodeCursor reads a cursor; ok=false for anything encodeCursor did not make.
func decodeCursor(c string) (time.Time, uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	ts, idText, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return t, id, true
}

func bad(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
}

// HandleListRewards serves GET .../events/{event_id}/referral-rewards: the
// cashback owed for every friend who used a personal code of the event,
// newest first. state filters; limit is 1-200 (default 50); cursor is the
// next_cursor of the previous page.
func (h *Handler) HandleListRewards(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state != "" && !rewardStates[state] {
		bad(w, r, "referral.invalid_state", "state must be one of due, sent, paid, capped, not_possible, failed, cancelled")
		return
	}
	limit := defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			bad(w, r, "referral.invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	var before *time.Time
	beforeID := uuid.Nil
	if v := q.Get("cursor"); v != "" {
		t, id, ok := decodeCursor(v)
		if !ok {
			bad(w, r, "referral.invalid_cursor", "cursor is not one this route returned")
			return
		}
		before, beforeID = &t, id
	}
	rows, err := h.queries.ListReferralRewardsForEvent(r.Context(), orgID, eventID, state, before, beforeID, int32(limit+1))
	if err != nil {
		h.failed(w, r, "rewards list", err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		c := encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
		next = &c
	}
	items := make([]rewardJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRewardJSON(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

type moneyJSON struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// cashbackJSON is the cashback of one currency by reward state. due is money
// promised and not yet sent; sent is on its way to the card; paid is back on
// it; the other states are money that stayed with the organizer.
type cashbackJSON struct {
	Currency         string `json:"currency"`
	PaidMinor        int64  `json:"paid_minor"`
	SentMinor        int64  `json:"sent_minor"`
	DueMinor         int64  `json:"due_minor"`
	NotPossibleMinor int64  `json:"not_possible_minor"`
	CappedMinor      int64  `json:"capped_minor"`
	FailedMinor      int64  `json:"failed_minor"`
	CancelledMinor   int64  `json:"cancelled_minor"`
}

// foldCashback turns (currency, state, sum) rows into one object per currency,
// in currency order (pure).
func foldCashback(rows []gen.ReferralMoney) []cashbackJSON {
	by := map[string]*cashbackJSON{}
	for _, m := range rows {
		c := by[m.Currency]
		if c == nil {
			c = &cashbackJSON{Currency: m.Currency}
			by[m.Currency] = c
		}
		switch m.State {
		case "paid":
			c.PaidMinor += m.AmountMinor
		case "sent":
			c.SentMinor += m.AmountMinor
		case "due":
			c.DueMinor += m.AmountMinor
		case "not_possible":
			c.NotPossibleMinor += m.AmountMinor
		case "capped":
			c.CappedMinor += m.AmountMinor
		case "failed":
			c.FailedMinor += m.AmountMinor
		case "cancelled":
			c.CancelledMinor += m.AmountMinor
		}
	}
	out := make([]cashbackJSON, 0, len(by))
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// HandleSummary serves GET .../events/{event_id}/referral-program/summary: the
// head numbers of the event's programme(s), all of them (an ended programme's
// figures stay), in the organization's own money.
func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	counts, err := h.queries.GetReferralSummaryCounts(ctx, orgID, eventID)
	if err != nil {
		h.failed(w, r, "summary counts", err)
		return
	}
	discounts, err := h.queries.ListReferralDiscountTotals(ctx, orgID, eventID)
	if err != nil {
		h.failed(w, r, "summary discounts", err)
		return
	}
	cashback, err := h.queries.ListReferralCashbackTotals(ctx, orgID, eventID)
	if err != nil {
		h.failed(w, r, "summary cashback", err)
		return
	}
	disc := make([]moneyJSON, 0, len(discounts))
	for _, d := range discounts {
		disc = append(disc, moneyJSON{Currency: d.Currency, AmountMinor: d.AmountMinor})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event_id":         eventID.String(),
		"codes_issued":     counts.CodesIssued,
		"friends":          counts.Friends,
		"friend_reversals": counts.FriendReversals,
		"discount_given":   disc,
		"cashback":         foldCashback(cashback),
	})
}
