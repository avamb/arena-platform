// service_fee_quote.go — GET /v1/organizations/{org_id}/billing/service-fee-quote
// (billing.read). An organizer who wants the net ticket price lets the buyer
// pay the platform's commission as the service charge (terms fee_mode
// buyer_pays). This answers how large that service charge must be per ticket
// at a given price: the price-list (or individual) rate of a sold ticket
// right now, computed by the same rating code that bills the sale.
package hbilling

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleServiceFeeQuote answers ?currency=EUR&price_minor=2000&seated=false.
func (h *AccountBillingHandler) HandleServiceFeeQuote(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	qs := r.URL.Query()
	currency := strings.ToUpper(strings.TrimSpace(qs.Get("currency")))
	price, err := strconv.ParseInt(qs.Get("price_minor"), 10, 64)
	seated := qs.Get("seated") == "true"
	switch {
	case len(currency) != 3:
		badRequest(w, r, "currency is a 3-letter code")
		return
	case err != nil || price < 0 || price > 1_000_000_000:
		badRequest(w, r, "price_minor is the ticket price in minor units, 0-1000000000")
		return
	case qs.Get("seated") != "" && qs.Get("seated") != "true" && qs.Get("seated") != "false":
		badRequest(w, r, "seated is true or false")
		return
	}
	ctx, q, now := r.Context(), gen.New(h.pool), h.now()
	acc, err := q.GetBillingAccount(ctx, orgID, currency, "organizer")
	if errors.Is(err, pgx.ErrNoRows) {
		// The sale may be in another currency than the account's: use the
		// organization's only account then.
		accounts, lerr := q.ListBillingAccountsByOrg(ctx, orgID)
		if lerr != nil {
			h.fail(w, r, "accounts", lerr)
			return
		}
		if len(accounts) != 1 {
			notFound(w, r, "billing.account_not_found", "this organization has no billing account in "+currency)
			return
		}
		acc, err = accounts[0], nil
	}
	if err != nil {
		h.fail(w, r, "account", err)
		return
	}
	terms, err := billing.TermsAt(ctx, q, acc.ID, now)
	if err != nil {
		unprocessable(w, r, "billing.terms_unavailable", "this billing account has no usable terms right now; please contact support")
		return
	}
	class, _ := billing.ClassifyLowPrice(terms, currency, price)
	c, err := billing.StandardTicketCharge(terms, billing.Event{
		OccurredAt: now, Seated: seated, Currency: currency, UnitPriceMinor: price, PricingClass: class,
	})
	if err != nil {
		unprocessable(w, r, "billing.quote_unavailable", "the commission for this ticket cannot be computed; please contact support")
		return
	}
	if c.Deferred != "" {
		unprocessable(w, r, "billing.fx_rate_missing",
			"the commission is in "+acc.Currency+" and the ticket in "+currency+"; it cannot be converted yet")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"account_id": acc.ID.String(), "currency": c.Currency, "ticket_currency": currency, "price_minor": price,
		"seated": seated, "pricing_class": class, "fee_mode": terms.FeeMode(), "service_fee_minor": c.AmountMinor,
		"rule_type": string(c.RuleType), "reason": c.Reason, "price_list_version": c.PriceListVersion,
	})
}
