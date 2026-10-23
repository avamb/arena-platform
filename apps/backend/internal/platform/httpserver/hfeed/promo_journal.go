package hfeed

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

// WithPromoAttempts wires the journal of promo-code checks (migration 0166).
// Without it the widget keeps no journal. Returns the receiver for chaining.
func (h *Handler) WithPromoAttempts(rec *promoattempts.Recorder) *Handler {
	h.promoRec = rec
	return h
}

// promoJournal carries what one request knows about the code it is judging.
// A nil *promoJournal (no recorder, no code named) records nothing.
type promoJournal struct {
	rec     *promoattempts.Recorder
	attempt promoattempts.Attempt
}

// newPromoJournal prepares the journal of one request. The cart key joins
// the session and a hash of the client address, so a repeated press of
// "Apply" for the same cart collapses while two buyers do not.
func (h *Handler) newPromoJournal(r *http.Request, surface string, orgID, channelID, sessionID uuid.UUID, typed string) *promoJournal {
	typed = strings.TrimSpace(typed)
	if h.promoRec == nil || typed == "" {
		return nil
	}
	ip := h.clientIP(r)
	key := sessionID.String()
	if ipHash := h.promoRec.HashIP(ip); ipHash != "" {
		key += "|" + ipHash[:16]
	}
	return &promoJournal{rec: h.promoRec, attempt: promoattempts.Attempt{
		OrgID: orgID, ChannelID: channelID, Surface: surface, Code: typed,
		SessionIDs: []string{sessionID.String()}, ClientIP: ip, CartKey: key,
	}}
}

// record journals the verdict of one check: errCode "" = applied. A failed
// check (a platform fault, not a verdict) is not journaled.
func (j *promoJournal) record(ctx context.Context, row *gen.PromoCodeRow, errCode string) {
	if j == nil || errCode == "checkout.promo_lookup_failed" {
		return
	}
	a := j.attempt
	a.ErrCode = errCode
	if row != nil {
		a.PromoCodeID = row.ID
	}
	j.rec.Record(ctx, a)
}
