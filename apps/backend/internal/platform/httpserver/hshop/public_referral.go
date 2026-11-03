package hshop

// public_referral.go — the buyer's personal code after a paid shop order
// (spec 52 section 3.5).

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// shareURL is the storefront address a friend opens: the shop's own domain
// when it has one, else the platform shop host plus the organization's slug,
// carrying the code and the sales-source marks. "" when the shop has no known
// address.
func shareURL(domain, platformBase, orgSlug, code string) string {
	var base string
	switch {
	case strings.TrimSpace(domain) != "":
		base = "https://" + strings.TrimSpace(domain)
	case strings.TrimSpace(platformBase) != "" && orgSlug != "":
		base = strings.TrimRight(strings.TrimSpace(platformBase), "/") + "/" + orgSlug
	default:
		return ""
	}
	q := url.Values{}
	q.Set("promo", code)
	q.Set("utm_source", "referral")
	q.Set("utm_medium", "friend")
	return base + "/?" + q.Encode()
}

// HandlePublicCheckoutReferral serves GET
// /v1/public/shop/checkout/{order_token}/referral: the personal code of a paid
// order and the link to share. The first call issues the code; a repeat returns
// the same one. 404 referral.not_available for everything that is not a paid
// order of a shop with a live programme (unknown token included), so the route
// reveals nothing about tokens. The answer is private to the buyer.
func (h *Handler) HandlePublicCheckoutReferral(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "order_token"))
	if h.limited(w, r, token) {
		return
	}
	sh, err := h.o.Service.ReferralForOrder(r.Context(), token)
	if err != nil {
		if errors.Is(err, shop.ErrReferralNotAvailable) {
			w.Header().Set("Cache-Control", "no-store")
			writeErr(w, r, http.StatusNotFound, "referral.not_available", "there is no friend code for this order")
			return
		}
		h.fail(w, r, "referral code", err)
		return
	}
	// null, not "", when the shop has no known address: the client then shows the code alone.
	var link any
	if u := shareURL(sh.ShopDomain, h.o.ShopBaseURL, sh.OrgSlug, sh.Code); u != "" {
		link = u
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"code":                sh.Code,
		"friend_discount_pct": sh.FriendDiscountPct,
		"cashback_pct":        sh.CashbackPct,
		"share_url":           link,
	})
}
