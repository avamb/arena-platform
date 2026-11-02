// public_referral.go — the buyer's side of "Refer a friend" (spec 52).
//
// GET /v1/public/checkout/{checkout_token}/referral
//
// No JWT. The checkout token is the credential, exactly as for the order
// status route, and the same per-token and per-IP limits apply. For a paid
// order whose event has an active programme the route returns the order's
// personal code (issued on the first call, the same code on every later one),
// the two percentages it promises and a ready-to-share link. For anything else
// it answers the one 404 referral.not_available, so a buyer's page cannot tell
// "no programme" from "not paid yet" from "an order that is not eligible".
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/publicurl"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

// promoOwnReferralCode is the verdict for a personal code used by the person
// it was issued to (spec 52 section 8). It is a stable public code.
const promoOwnReferralCode = "promo.own_referral_code"

// referralVerdict judges a personal "Refer a friend" code for a cart: "" when
// the code is an ordinary one or fits, promo.not_active when its programme is
// paused, ended or outside its period (a pause or the end stops NEW discounts,
// spec 52 section 3.1), promoOwnReferralCode when the cart names the person it
// was issued to. The buyer's e-mail and phone are optional: a quote names no
// buyer and skips the self-invitation half.
func (h *Handler) referralVerdict(ctx context.Context, q *gen.Queries, promoID uuid.UUID, email, phone string) (string, error) {
	meta, err := q.GetReferralPromoMeta(ctx, promoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if meta.Origin != "referral" {
		return "", nil
	}
	if meta.ProgramID != nil {
		p, err := q.GetReferralProgramByID(ctx, *meta.ProgramID)
		if err != nil {
			return "", err
		}
		now := time.Now().UTC()
		if p.Status != "active" || (p.StartsAt != nil && now.Before(*p.StartsAt)) || (p.EndsAt != nil && !now.Before(*p.EndsAt)) {
			return "promo.not_active", nil
		}
	}
	if meta.ReferrerOrderID == nil || (strings.TrimSpace(email) == "" && strings.TrimSpace(phone) == "") {
		return "", nil
	}
	own, err := referral.IsSelfReferralBuyer(ctx, q.DB(), *meta.ReferrerOrderID, email, phone)
	if err != nil {
		return "", err
	}
	if own {
		return promoOwnReferralCode, nil
	}
	return "", nil
}

type publicReferralResponse struct {
	Code              string  `json:"code"`
	FriendDiscountPct int     `json:"friend_discount_pct"`
	CashbackPct       int     `json:"cashback_pct"`
	ShareURL          *string `json:"share_url"`
}

func notAvailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"referral.not_available", "There is no friend code for this order.", r,
	))
}

// shareURL builds <event page>?promo=<code>&utm_source=referral&utm_medium=friend&utm_campaign=<slug>.
// "" when the page address cannot be built (no tickets base URL or no slug).
func shareURL(base string, f gen.ReferralShareFacts, code string) string {
	page := publicurl.Page(base, f.OrgSlug, f.PromoterSlug, f.EventSlug)
	if page == "" {
		return ""
	}
	campaign := f.EventSlug
	if campaign == "" {
		campaign = "event"
	}
	var b strings.Builder
	b.WriteString(page)
	b.WriteString("?promo=" + url.QueryEscape(code))
	b.WriteString("&utm_source=referral&utm_medium=friend&utm_campaign=" + url.QueryEscape(campaign))
	return b.String()
}

// HandleGetPublicReferral serves GET /v1/public/checkout/{checkout_token}/referral.
func (h *Handler) HandleGetPublicReferral(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	ctx := r.Context()
	fail := func(what string, err error) {
		h.logger.Error("public_referral: "+what+" failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"referral.failed", "We could not load the friend code. Try again in a minute.", r,
		))
	}

	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if errors.Is(err, pgx.ErrNoRows) {
		notAvailable(w, r)
		return
	}
	if err != nil {
		fail("session lookup", err)
		return
	}
	ord, err := h.checkoutQueries.GetOrderByCheckoutSession(ctx, cs.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		notAvailable(w, r)
		return
	}
	if err != nil {
		fail("order lookup", err)
		return
	}
	if ord.Status != "paid" && ord.Status != "partially_refunded" {
		notAvailable(w, r)
		return
	}
	// The programme must be on today: a code issued earlier is not offered
	// again while the programme is paused or over.
	if _, err := h.checkoutQueries.GetActiveReferralProgramForEvent(ctx, ord.EventID, time.Now().UTC()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notAvailable(w, r)
			return
		}
		fail("programme lookup", err)
		return
	}
	code, _, err := referral.IssueCode(ctx, h.checkoutQueries.DB(), ord.ID)
	switch {
	case errors.Is(err, referral.ErrNoProgram), errors.Is(err, referral.ErrNotEligible), errors.Is(err, referral.ErrOrderNotFound):
		notAvailable(w, r)
		return
	case err != nil:
		fail("issue", err)
		return
	}
	if code.Status != "active" {
		notAvailable(w, r)
		return
	}

	resp := publicReferralResponse{
		Code: code.Code, FriendDiscountPct: code.FriendDiscountPct, CashbackPct: code.CashbackPct,
	}
	facts, err := h.checkoutQueries.GetReferralShareFacts(ctx, ord.EventID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Warn("public_referral: share facts failed", slog.String("error", err.Error()))
	} else if err == nil {
		if u := shareURL(h.returnURLPolicy.Fallback, facts, code.Code); u != "" {
			resp.ShareURL = &u
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httputil.WriteJSON(w, http.StatusOK, resp)
}
