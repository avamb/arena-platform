// cart_conversions.go queues the widget cart's AddToCart (checkout/quote) and
// InitiateCheckout (checkout/start) for Meta CAPI and GA4 from the server.
//
// Only an explicit yes counts: buyer_context.tracking_consent must be true
// (with its text version, conversions.ConsentFromBuyerContext). No answer, a
// no, or an unreadable object queues nothing. Queuing is best effort: it runs
// on the pool after any transaction of the request has ended, and a failure
// is logged without the buyer's data and never changes the answer.
package hfeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// CartConversions queues a cart event (conversions.PGStore implements it).
type CartConversions interface {
	EnqueueEvent(ctx context.Context, orgID uuid.UUID, ev conversions.SiteEvent) error
	SessionItem(ctx context.Context, sessionID uuid.UUID) (conversions.PurchaseItem, bool)
}

// WithCartConversions wires the cart events; nil leaves them off.
func (h *Handler) WithCartConversions(c CartConversions) *Handler {
	h.cartConversions = c
	return h
}

// Bounds of the quote's tracking fields.
const (
	maxAddToCartID      = 64
	maxSeatsConsidered  = 1000
	addToCartEventIDPfx = "atc:"
	checkoutEventIDPfx  = "ic:"
)

// buyerContextConsents reports an explicit yes in buyer_context.
func buyerContextConsents(raw json.RawMessage) bool {
	in := conversions.ConsentFromBuyerContext(raw)
	return in != nil && in.Granted != nil && *in.Granted
}

// checkoutTracksCart decides InitiateCheckout for checkout/start: an explicit
// yes in buyer_context, not overruled by the stored answer (a top-level
// tracking_consent wins when readable).
func checkoutTracksCart(stored *trackingconsent.Consent, buyerContext json.RawMessage) bool {
	return stored != nil && stored.Granted && buyerContextConsents(buyerContext)
}

// parseAddToCartID reads add_to_cart_id: a JSON string of 1..64 characters
// [A-Za-z0-9_-]. Anything else is "" (no event), never an error.
func parseAddToCartID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxAddToCartID {
		return ""
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			return ""
		}
	}
	return s
}

// parseSeatsConsidered reads seats_considered: a whole number 0..1000.
// Anything else is 0, never an error.
func parseSeatsConsidered(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 || n > maxSeatsConsidered || n != float64(int(n)) {
		return 0
	}
	return int(n)
}

// queueCartEvent queues one cart event. cleanedCtx is the cleaned buyer
// context (conversions.CleanBuyerContext). The caller has already decided
// that the buyer said yes.
func (h *Handler) queueCartEvent(ctx context.Context, orgID uuid.UUID, kind, eventID string,
	sessionID uuid.UUID, bd hcheckout.PricingBreakdown, cleanedCtx []byte, seatsConsidered int) {
	if h.cartConversions == nil || eventID == "" {
		return
	}
	// A tracking label must never hold up a purchase: two pool queries at most 2 s.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var bc conversions.BuyerContext
	if len(cleanedCtx) > 0 {
		_ = json.Unmarshal(cleanedCtx, &bc)
	}
	item, _ := h.cartConversions.SessionItem(ctx, sessionID)
	if item.SessionID == "" {
		item.SessionID = sessionID.String()
	}
	var maxUnit int64
	for _, l := range bd.Lines {
		if l.UnitPrice > maxUnit {
			maxUnit = l.UnitPrice
		}
	}
	if len(bd.Lines) == 0 {
		maxUnit = bd.UnitPrice
	}
	ev := conversions.CartEvent(kind, eventID, item, int(bd.Quantity), maxUnit, bd.Total, bd.Currency, bc, seatsConsidered)
	if err := h.cartConversions.EnqueueEvent(ctx, orgID, ev); err != nil && h.logger != nil {
		h.logger.Warn("public_feed: cart conversion event not queued",
			slog.String("kind", kind),
			slog.String("org_id", orgID.String()),
			slog.String("error", err.Error()),
		)
	}
}
