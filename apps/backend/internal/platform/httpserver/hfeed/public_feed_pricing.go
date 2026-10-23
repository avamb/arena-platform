// public_feed_pricing.go holds the pieces of pricing a public cart that
// checkout/start and checkout/quote MUST share, so the amount a buyer is quoted
// is the amount the payment page asks for by construction, not by a second copy
// of the same rules staying in step.
//
//   - priceGAItems / gaPricingLines / cartCurrency: the general-admission lines
//     of a cart and the currency the cart is priced in;
//   - findPromoCode / applyPromoDiscount (public_feed_checkout.go): which code
//     a buyer's text names and whether it fits the cart;
//   - writePublicPromoError: the buyer-facing verdict on a code that does not.
//
// The seated lines come from seatPricingLines and the money from
// hcheckout.ComputePricingLines; both were already shared.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Caps on one public cart, shared by checkout/start and checkout/quote. A
// request is priced with one database read per line and per distinct seat
// category, so an unbounded list is an unbounded amount of work for one
// anonymous request; the caps sit far above any real purchase.
const (
	// maxCartGALines is the most general-admission lines (categories) in one cart.
	maxCartGALines = 20
	// maxLineQuantity is the most tickets on one general-admission line. It is
	// the cap checkout/start has always enforced on its legacy qty field.
	maxLineQuantity = 50
	// maxCartSeats is the most assigned seats in one cart.
	maxCartSeats = 100
)

// checkCartCaps answers "" for a cart within the caps, otherwise the stable
// error code to refuse it with (always a 400, before any database work):
// checkout.cart_too_large for too many lines or seats, checkout.invalid_qty
// for a line over maxLineQuantity.
func checkCartCaps(seats []string, items []PublicGAItem) (code, message string) {
	if len(items) > maxCartGALines {
		return "checkout.cart_too_large", fmt.Sprintf("a cart holds at most %d ticket lines", maxCartGALines)
	}
	if len(seats) > maxCartSeats {
		return "checkout.cart_too_large", fmt.Sprintf("a cart holds at most %d seats", maxCartSeats)
	}
	for i, it := range items {
		if it.Quantity > maxLineQuantity {
			return "checkout.invalid_qty", fmt.Sprintf("ga_items[%d].quantity must be at most %d", i, maxLineQuantity)
		}
	}
	return "", ""
}

// gaItemPriced is one general-admission line with its platform-resolved unit
// price (the tier's price window at "now").
type gaItemPriced struct {
	tierID    uuid.UUID
	qty       int32
	unitPrice int64
	currency  string
}

// priceGAItems resolves every GA item of a cart against the session's tiers.
// tierIDs are the already-parsed tier_id of each item, index for index. The
// returned code is "" on success; otherwise one of the codes writePricingError
// answers: dependency.tier_unavailable, checkout.tier_not_found,
// checkout.tier_lookup_failed, checkout.pwyw_not_supported and friends.
//
// Reads go through the pool: callers run this BEFORE any hold transaction.
func (h *Handler) priceGAItems(ctx context.Context, sessionID uuid.UUID, items []PublicGAItem, tierIDs []uuid.UUID) ([]gaItemPriced, string) {
	if len(items) == 0 {
		return nil, ""
	}
	if h.tierQueries == nil {
		return nil, "dependency.tier_unavailable"
	}
	priced := make([]gaItemPriced, 0, len(items))
	for i, item := range items {
		tierID := tierIDs[i]
		tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "checkout.tier_not_found"
			}
			h.logger.Error("public_feed_checkout: tier lookup failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			return nil, "checkout.tier_lookup_failed"
		}
		unitPrice, priceErr := h.publicTierUnitPrice(ctx, h.tierQueries, tier)
		if priceErr != "" {
			return nil, priceErr
		}
		priced = append(priced, gaItemPriced{tierID: tierID, qty: item.Quantity, unitPrice: unitPrice, currency: tier.Currency})
	}
	return priced, ""
}

// gaPricingLines turns the priced GA items into pricing-pipeline input
// (guardrail #15: every line is priced platform-side).
func gaPricingLines(priced []gaItemPriced) []hcheckout.PricingLineInput {
	lines := make([]hcheckout.PricingLineInput, 0, len(priced))
	for _, g := range priced {
		lines = append(lines, hcheckout.PricingLineInput{
			TierID:    g.tierID.String(),
			Quantity:  g.qty,
			UnitPrice: g.unitPrice,
		})
	}
	return lines
}

// cartCurrency is the currency a cart is priced in: the first GA line's when
// the cart has one, otherwise the seats' (which may be "" when no seat has a
// tier bound — the caller decides the fallback).
func cartCurrency(priced []gaItemPriced, seatCurrency string) string {
	if len(priced) > 0 {
		return priced[0].currency
	}
	return seatCurrency
}

// errPromoUnavailable is findPromoCode's answer when the promo store is not
// wired: callers answer it as a failed lookup (500), never as "no code".
var errPromoUnavailable = errors.New("promo store unavailable")

// findPromoCode resolves the text a buyer typed to one of the organization's
// promo codes. Buyers type codes by hand, so the match ignores case and the
// stray spaces of a paste, like the Bil24 gateway's. A blank input is "no
// code" (nil, nil); an unknown one is pgx.ErrNoRows. The lookup is scoped to
// the organization, so another organization's code does not exist here.
func (h *Handler) findPromoCode(ctx context.Context, orgID uuid.UUID, raw string) (*gen.PromoCodeRow, error) {
	code := strings.TrimSpace(raw)
	if code == "" {
		return nil, nil
	}
	if h.promoQueries == nil {
		// A code was named and cannot be checked: fail closed. Charging the
		// full price for a cart the buyer was promised a discount on, or the
		// reverse, is worse than refusing.
		return nil, errPromoUnavailable
	}
	row, err := h.promoQueries.GetPromoCodeByCodeCI(ctx, orgID, code)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Public promo verdicts. The platform's internal checks are finer than what a
// buyer may be told: within one organization, "this code exists but belongs to
// another event" must read exactly like "no such code". Everything that would
// show the shape of a code the cart is not entitled to collapses into
// promoNotApplicable; only verdicts reached AFTER the code is proven to fit the
// cart (a usage cap, the minimum order) name their cause.
const (
	promoNotApplicable = "promo.not_applicable"
	promoMinimum       = "promo.minimum_not_reached"
)

// publicPromoError maps an internal promo verdict onto the stable public code
// and a neutral message.
func publicPromoError(internal string) (code, message string) {
	switch internal {
	case hcheckout.PromoErrExhausted:
		return hcheckout.PromoErrExhausted, "This code has been used the maximum number of times."
	case hcheckout.PromoErrPerCustomerLimit:
		return hcheckout.PromoErrPerCustomerLimit, "You have already used this code the maximum number of times."
	case "promo.invalid_order_amount":
		return promoMinimum, "The order is below the minimum amount for this code."
	default:
		return promoNotApplicable, "This code does not apply to this order."
	}
}

// PromoFailureLimiter is the optional second half of the rate limiter: a
// separate, tighter budget for promo codes that were REFUSED, so that telling
// an accepted code from a refused one is no cheaper than the general limits
// allow. A limiter that does not implement it leaves the route on the general
// limits only.
type PromoFailureLimiter interface {
	// ReservePromoAttempt takes one slot of the refused-code budget of the feed
	// token and of the client IP at the moment a request names a code, in one
	// atomic step, so a burst of concurrent requests cannot all pass a check
	// made before any of them is counted. When either budget is spent nothing
	// is taken and blocked is true, with the seconds until it renews. The caller
	// calls release when the code turned out acceptable (or the request failed
	// for another reason): only a refused code keeps its slot.
	ReservePromoAttempt(token, ip string) (release func(), blocked bool, retryAfterSeconds int)
}

// promoAttempt is one reserved slot of the refused-code budget. The zero
// value, and nil, hold nothing.
type promoAttempt struct {
	release func()
	kept    bool
}

// Release gives the slot back unless the code was refused. Safe to call more
// than once and on nil, so handlers defer it.
func (a *promoAttempt) Release() {
	if a == nil || a.kept || a.release == nil {
		return
	}
	a.release()
	a.release = nil
}

// keep marks the code as refused: the slot stays spent.
func (a *promoAttempt) keep() {
	if a != nil {
		a.kept = true
	}
}

// reservePromoAttempt answers 429 promo.rate_limited when the refused-code
// budget is spent; otherwise it reserves a slot for a request that names a
// code. A request that names none holds nothing (nil). The caller returns at
// once when blocked is true, and defers the returned attempt's Release.
func (h *Handler) reservePromoAttempt(w http.ResponseWriter, r *http.Request, feedToken string, namesCode bool) (attempt *promoAttempt, blocked bool) {
	if !namesCode {
		return nil, false
	}
	lim, ok := h.rl.(PromoFailureLimiter)
	if !ok {
		return nil, false
	}
	release, isBlocked, retry := lim.ReservePromoAttempt(feedToken, h.clientIP(r))
	if !isBlocked {
		return &promoAttempt{release: release}, false
	}
	if retry < 1 {
		retry = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
		"promo.rate_limited", "Too many code attempts. Wait one minute and try again.", r,
	))
	return nil, true
}

// refusePromo answers a refused code. The slot reserved for the request stays
// spent, except when the check itself failed (a platform fault, not a guess).
func (h *Handler) refusePromo(w http.ResponseWriter, r *http.Request, attempt *promoAttempt, internal string) {
	if internal != "checkout.promo_lookup_failed" {
		attempt.keep()
	}
	writePublicPromoError(w, r, internal)
}

// refusePromoFor is refusePromo for a verdict that still has its code row.
// A code that exists in the buyer's own organization but is bound to other
// events answers the same stable 422 promo.not_applicable, now with the
// events it does work on ("This code works on: A, B"). Only published events
// with sales still open are named (hcheckout.PromoScopeHint), and only the
// organization's own codes ever get here (findPromoCode is org-scoped), so
// nothing about a draft event or another organization is shown.
func (h *Handler) refusePromoFor(w http.ResponseWriter, r *http.Request, attempt *promoAttempt, internal string, row *gen.PromoCodeRow) {
	if internal == "promo.session_not_applicable" && row != nil && h.promoQueries != nil {
		if hint := hcheckout.PromoScopeHint(r.Context(), h.promoQueries, *row, time.Now().UTC()); hint != "" {
			attempt.keep()
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				promoNotApplicable, "This code works on: "+hint+". Choose tickets for that event to use it.", r,
				map[string]any{"events": hint},
			))
			return
		}
	}
	h.refusePromo(w, r, attempt, internal)
}

// writePublicPromoError answers a refused promo code on a public route: 422
// with the stable public code, or 500 when the check itself failed (a cap the
// platform cannot verify is never waved through).
func writePublicPromoError(w http.ResponseWriter, r *http.Request, internal string) {
	if internal == "checkout.promo_lookup_failed" {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			internal, "We could not check the promo code. Try again in a minute.", r,
		))
		return
	}
	code, msg := publicPromoError(internal)
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(code, msg, r))
}
