// marketing_consent.go — the marketing consent tick of the public checkout
// and the discount it earns (spec 51-1, package marketing).
//
//   - The feed session answers a `marketing_optin` block: whether the tick is
//     offered, whose mailings it is for, the text version and the reward.
//   - checkout/quote and checkout/start take `marketing_consent`. When it is
//     ticked and the organization names a reward code, the reward is the
//     cart's discount; a code the buyer typed as well competes with it and
//     the larger discount wins (owner decision В-9). The cart keeps ONE code.
//   - The reward code typed by hand without the tick is refused like any
//     code that does not fit (promo.not_applicable): it is not a public code.
//   - A reward the buyer already used (one use per buyer, checked at start
//     where the e-mail is known) answers 422 marketing.discount_used, so the
//     widget can tell the buyer and send the cart again with discount false.
//   - A tick sent to an organization that does not offer it is ignored, and
//     nothing of the buyer is stored for it.
package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

// PublicMarketingConsent is the buyer's tick on "send me news and offers".
type PublicMarketingConsent struct {
	Accepted bool `json:"accepted"`
	// TextVersion is the version of the text the widget showed
	// (marketing.TextVersion); another version is 422 marketing.consent_outdated.
	TextVersion string `json:"text_version"`
	// Discount false asks for no reward: the buyer was told it is used up.
	// Absent means true.
	Discount *bool `json:"discount,omitempty"`
}

// publicMarketingBlock is the feed session's description of the tick.
type publicMarketingBlock struct {
	Enabled bool `json:"enabled"`
	// OrgName is whose mailings the buyer agrees to: the seller's legal name,
	// else the organization's name.
	OrgName     string                   `json:"org_name,omitempty"`
	TextVersion string                   `json:"text_version,omitempty"`
	Discount    *publicMarketingDiscount `json:"discount,omitempty"`
}

// publicMarketingDiscount describes the reward: percent (1-100) or a fixed
// amount in minor units of Currency.
type publicMarketingDiscount struct {
	Type     string  `json:"type"`
	Value    int64   `json:"value"`
	Currency *string `json:"currency,omitempty"`
}

// marketingState is what one checkout request knows about the tick.
type marketingState struct {
	offer    marketing.Offer
	orgName  string
	accepted bool // ticked, current text, and the organization offers it
	reward   bool // the reward may be applied
}

// loadMarketing reads the organization's offer once per request, on the
// pool, before any transaction. A failure degrades to "not offered" (logged):
// a tick must never cost a sale.
func (h *Handler) loadMarketing(ctx context.Context, orgID uuid.UUID) (marketing.Offer, string) {
	if h.promoQueries == nil {
		return marketing.Offer{}, ""
	}
	db := h.promoQueries.DB()
	offer, err := marketing.LoadOffer(ctx, db, orgID)
	if err != nil {
		h.logger.Error("public_feed: marketing offer lookup failed",
			slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		return marketing.Offer{}, ""
	}
	if !offer.Enabled {
		return offer, ""
	}
	var name string
	if err := db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(btrim(legal_name), ''), name) FROM organizations WHERE id = $1`, orgID).Scan(&name); err != nil {
		h.logger.Error("public_feed: marketing org name lookup failed",
			slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		return marketing.Offer{}, ""
	}
	return offer, name
}

// cartReminderOn reads whether the organizer sends the unpaid-order reminder.
// A failed read says no: the widget then promises nothing.
func (h *Handler) cartReminderOn(ctx context.Context, orgID uuid.UUID) bool {
	if h.promoQueries == nil {
		return false
	}
	s, err := marketing.LoadSettings(ctx, h.promoQueries.DB(), orgID)
	if err != nil {
		h.logger.Error("public_feed: growth settings lookup failed",
			slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		return false
	}
	return s.CartReminder
}

// marketingBlockFor is the feed session block.
func marketingBlockFor(offer marketing.Offer, orgName string) publicMarketingBlock {
	if !offer.Enabled {
		return publicMarketingBlock{}
	}
	b := publicMarketingBlock{Enabled: true, OrgName: orgName, TextVersion: marketing.TextVersion}
	if offer.Code != nil {
		b.Discount = &publicMarketingDiscount{Type: offer.Code.DiscountType, Value: offer.Code.DiscountValue, Currency: offer.Code.Currency}
	}
	return b
}

// marketingFor checks the tick of a request. It answers false after writing
// 422 marketing.consent_outdated for a tick on an older text.
func (h *Handler) marketingFor(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID uuid.UUID, in *PublicMarketingConsent) (marketingState, bool) {
	offer, name := h.loadMarketing(ctx, orgID)
	st := marketingState{offer: offer, orgName: name}
	if in == nil || !in.Accepted || !offer.Enabled {
		return st, true // ignored: nothing of the buyer is stored for it
	}
	if in.TextVersion != marketing.TextVersion {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"marketing.consent_outdated", "The page shows an older text of the subscription. Reload the page and tick the box again.", r,
			map[string]any{"field": "marketing_consent.text_version", "text_version": marketing.TextVersion},
		))
		return marketingState{}, false
	}
	st.accepted = true
	st.reward = offer.Code != nil && (in.Discount == nil || *in.Discount)
	return st, true
}

// typedRewardRefused reports whether the buyer typed the reward code without
// the tick: it is refused as a code that does not apply.
func (st marketingState) typedRewardRefused(typed *gen.PromoCodeRow) bool {
	return typed != nil && st.offer.IsRewardCode(typed.ID) && !st.accepted
}

// pickDiscount weighs the reward against the code the buyer typed (already
// validated: typedD/typedID) and answers the cart's one discount, whether the
// reward won, and a blocking code: "marketing.discount_used" when the reward
// would win but the buyer has used it already (only knowable with an e-mail).
func (h *Handler) pickDiscount(ctx context.Context, q *gen.Queries, st marketingState, lines []hcheckout.TierLine,
	email string, typedD int64, typedID *uuid.UUID) (int64, *uuid.UUID, bool, string) {
	if !st.accepted || !st.reward {
		return typedD, typedID, false, ""
	}
	if typedID != nil && st.offer.IsRewardCode(*typedID) {
		return typedD, typedID, true, ""
	}
	d, errCode := hcheckout.ValidatePromoForLines(*st.offer.Code, lines, time.Now().UTC())
	if errCode != "" || d <= typedD {
		return typedD, typedID, false, ""
	}
	if email != "" {
		var limits hcheckout.PromoLimitQuerier
		if q != nil {
			limits = q
		}
		limitErr, err := hcheckout.CheckPromoLimits(ctx, limits, *st.offer.Code, nil, email)
		if err != nil {
			h.logger.Error("public_feed: reward usage count failed", slog.String("error", err.Error()))
			return typedD, typedID, false, "checkout.promo_lookup_failed"
		}
		if limitErr == hcheckout.PromoErrPerCustomerLimit {
			return typedD, typedID, false, "marketing.discount_used"
		}
		if limitErr != "" {
			return typedD, typedID, false, ""
		}
	}
	id := st.offer.Code.ID
	return d, &id, true, ""
}

// writeMarketingBlock answers a blocking verdict of pickDiscount.
func writeMarketingBlock(w http.ResponseWriter, r *http.Request, code string) {
	if code == "marketing.discount_used" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			code, "The discount for the subscription was already used with this e-mail. The subscription still counts.", r,
		))
		return
	}
	writePublicPromoError(w, r, code)
}

// grantFor builds the consent to record at checkout/start, nil when nothing
// is to be stored.
func grantFor(st marketingState, r *http.Request, orgID uuid.UUID, buyer publicOrderBuyer, locale, pageURL string) *marketing.Grant {
	if !st.accepted {
		return nil
	}
	g := &marketing.Grant{
		OrgID:       orgID,
		Email:       buyer.Email,
		Locale:      locale,
		TextVersion: marketing.TextVersion,
		PageURL:     pageURL,
		ClientIP:    httputil.TrustedClientIP(r, 0),
		UserAgent:   r.UserAgent(),
	}
	if buyer.Name != nil {
		g.Name = *buyer.Name
	}
	return g
}

// growthCheckout is what checkout/start carries into the confirmation
// transaction for spec 51-1: the consent to record and whether the reward won.
type growthCheckout struct {
	grant     *marketing.Grant
	rewardWon bool
}

// recordGrowthTx stores, in the checkout-confirmation transaction, the buyer's
// page the checkout started on (the reminder links back to it) and the
// ticked marketing consent. returnBase is the page already checked by the
// return-URL policy ("" for a free cart); it is also where the confirmation
// link sends the buyer, never the raw value the client sent.
func (h *Handler) recordGrowthTx(ctx context.Context, tx pgx.Tx, checkoutID uuid.UUID, returnBase string, g growthCheckout) error {
	if returnBase != "" {
		if _, err := tx.Exec(ctx, `UPDATE checkout_sessions SET return_url = $2 WHERE id = $1`, checkoutID, returnBase); err != nil {
			return err
		}
	}
	if g.grant == nil {
		return nil
	}
	grant := *g.grant
	grant.CheckoutSessionID = &checkoutID
	grant.PageURL = returnBase
	_, err := marketing.RecordGrantTx(ctx, tx, grant)
	return err
}
