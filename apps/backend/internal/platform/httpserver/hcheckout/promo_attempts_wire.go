package hcheckout

import "github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"

// WithPromoAttempts wires the journal of promo-code checks (migration 0161)
// used by POST /v1/checkout/promo-validate. Returns the receiver for chaining.
func (h *Handler) WithPromoAttempts(rec *promoattempts.Recorder) *Handler {
	h.promoRec = rec
	return h
}
