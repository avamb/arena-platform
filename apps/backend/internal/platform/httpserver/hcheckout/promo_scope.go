// promo_scope.go — what a promo code is FOR (sessions, currency) and how
// often it may be used, shared by every surface that applies one: the org
// API that creates codes, the Bil24 gateway commands, the widget checkout.
//
// Owner decisions of 2026-09-22 (08_architecture/25_promo_codes_plan_ru.md):
// a code applies to a SESSION, both usage limits count from day one, and a
// buyer learns about an exhausted code when they type it — not after the
// selling site has already taken their money.
package hcheckout

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// promoCurrencyRE is the shape migration 0108's CHECK accepts.
var promoCurrencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

// promoScopeInput is the part of a create/update body checkPromoScope judges.
// Currency nil means "not in the body" (PATCH keeps the stored value); a
// pointer to "" means "any currency". SessionIDs / TierIDs nil mean "not in
// the body" for PATCH and are validated only when present.
type promoScopeInput struct {
	DiscountType string
	Currency     *string
	SessionIDs   []string
	TierIDs      []string
}

// promoScope is the normalised result: Currency upper-cased (nil = keep).
type promoScope struct {
	Currency *string
}

// checkPromoScope validates the session ids (well-formed AND owned by the
// organization — a code must never reach into another tenant's sessions),
// the tier ids (well-formed) and the currency (ISO-4217 shape; REQUIRED for a
// fixed_amount code, because "500 off" means nothing without one). It writes
// the 4xx envelope itself and returns ok=false when the body is refused.
func (h *Handler) checkPromoScope(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
	orgID uuid.UUID, in promoScopeInput,
) (promoScope, bool) {
	for _, raw := range in.TierIDs {
		if _, err := uuid.Parse(strings.TrimSpace(raw)); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_tier_id", "applies_to_tier_ids must hold tier UUIDs", r,
				map[string]any{"field": "applies_to_tier_ids", "value": raw},
			))
			return promoScope{}, false
		}
	}
	for _, raw := range in.SessionIDs {
		sid, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_session_id", "applies_to_session_ids must hold session UUIDs", r,
				map[string]any{"field": "applies_to_session_ids", "value": raw},
			))
			return promoScope{}, false
		}
		octx, err := h.promoQueries.GetSessionOrgContext(ctx, sid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"promo.session_lookup_failed", "failed to look up a session", r,
			))
			return promoScope{}, false
		}
		if err != nil || octx.OrgID != orgID {
			// Unknown and foreign read the same: nothing here for this org.
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_session", "session does not belong to this organization", r,
				map[string]any{"field": "applies_to_session_ids", "value": sid.String()},
			))
			return promoScope{}, false
		}
	}

	out := promoScope{}
	if in.Currency != nil {
		cur := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if cur != "" && !promoCurrencyRE.MatchString(cur) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_currency", "currency must be a three-letter ISO 4217 code", r,
				map[string]any{"field": "currency"},
			))
			return promoScope{}, false
		}
		if cur == "" && in.DiscountType == "fixed_amount" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.currency_required", "a fixed_amount discount needs a currency", r,
				map[string]any{"field": "currency"},
			))
			return promoScope{}, false
		}
		out.Currency = &cur
	}
	return out, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Usage limits
// ─────────────────────────────────────────────────────────────────────────────

// PromoLimitQuerier is the redemption-counting surface CheckPromoLimits
// needs. *gen.Queries satisfies it.
type PromoLimitQuerier interface {
	CountPromoCodeRedemptions(ctx context.Context, promoCodeID uuid.UUID) (int32, error)
	CountPromoRedemptionsByCustomer(ctx context.Context, promoCodeID, customerID uuid.UUID) (int32, error)
	CountPromoRedemptionsByBuyerEmail(ctx context.Context, promoCodeID uuid.UUID, email string) (int32, error)
}

// Error codes CheckPromoLimits answers with; both are also the 409 codes of
// the legacy REST completion, so every surface names a limit the same way.
const (
	PromoErrExhausted        = "promo.exhausted"
	PromoErrPerCustomerLimit = "promo.per_customer_limit"
)

// CheckPromoLimits answers whether the code may be used ONE more time: the
// total cap first, then the per-customer cap for whoever is buying.
// customerID identifies the buyer where a customer row already exists (the
// Bil24 gateway session, a paid order); buyerEmail where only the e-mail is
// known yet (the widget pricing a cart). Either may be absent; an absent
// identity skips the per-customer check rather than failing it — an
// anonymous probe cannot be over its own limit.
//
// This is the pre-flight. The count-then-insert race between two buyers on
// the last use is closed at write time by the FOR UPDATE lock the recording
// paths take on the promo row; here the honest answer for a code on its last
// use is "still usable".
func CheckPromoLimits(
	ctx context.Context, q PromoLimitQuerier, pc gen.PromoCodeRow,
	customerID *uuid.UUID, buyerEmail string,
) (string, error) {
	if q == nil {
		return "", nil
	}
	if pc.MaxUses != nil {
		used, err := q.CountPromoCodeRedemptions(ctx, pc.ID)
		if err != nil {
			return "", err
		}
		if used >= *pc.MaxUses {
			return PromoErrExhausted, nil
		}
	}
	if pc.MaxUsesPerCustomer == nil {
		return "", nil
	}
	var mine int32
	switch {
	case customerID != nil && *customerID != uuid.Nil:
		n, err := q.CountPromoRedemptionsByCustomer(ctx, pc.ID, *customerID)
		if err != nil {
			return "", err
		}
		mine = n
	case strings.TrimSpace(buyerEmail) != "":
		n, err := q.CountPromoRedemptionsByBuyerEmail(ctx, pc.ID, strings.TrimSpace(buyerEmail))
		if err != nil {
			return "", err
		}
		mine = n
	default:
		return "", nil
	}
	if mine >= *pc.MaxUsesPerCustomer {
		return PromoErrPerCustomerLimit, nil
	}
	return "", nil
}

// validPromoStatus is the API contract's lifecycle set (openapi.yaml:
// `status: active | paused`). Anything else used to reach the column CHECK
// and answer a bare 500 promo.update_failed; migration 0110 added 'paused'
// to that CHECK, and this guard keeps an unknown value a 400.
func validPromoStatus(s string) bool {
	return s == "active" || s == "paused"
}

func writeInvalidPromoStatus(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"promo.invalid_status", "status must be 'active' or 'paused'", r,
		map[string]any{"field": "status"},
	))
}
