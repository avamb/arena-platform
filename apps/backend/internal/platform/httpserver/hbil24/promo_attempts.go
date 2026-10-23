package hbil24

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

// WithPromoAttempts wires the journal of promo-code checks (migration 0161).
// Without it the gateway keeps no journal. Returns the receiver for chaining.
func (h *Handler) WithPromoAttempts(rec *promoattempts.Recorder) *Handler {
	h.promoRec = rec
	return h
}

// evaluatePromoCode judges one code (evaluatePromoCodeRaw) and journals the
// verdict under surface. A lookup failure is not a verdict and is not
// journaled. The write is asynchronous and cannot fail the command.
func (h *Handler) evaluatePromoCode(ctx context.Context, pc promoCtx, surface, code string, now time.Time) (promoVerdict, error) {
	v, err := h.evaluatePromoCodeRaw(ctx, pc, code, now)
	if err != nil || h.promoRec == nil {
		return v, err
	}
	sessions := make([]string, 0, 2)
	for _, l := range pc.snap.lines {
		if l.sessionID != uuid.Nil {
			sessions = append(sessions, l.sessionID.String())
		}
	}
	if len(sessions) == 0 && pc.cc.sessionID != uuid.Nil {
		sessions = append(sessions, pc.cc.sessionID.String())
	}
	h.promoRec.Record(ctx, promoattempts.Attempt{
		OrgID:            pc.cc.orgID,
		ChannelID:        pc.cc.channel.ID,
		Surface:          surface,
		Code:             code,
		PromoCodeID:      v.row.ID,
		SessionIDs:       sessions,
		ErrCode:          v.errCode,
		CustomerID:       pc.cc.gw.CustomerID,
		GatewaySessionID: pc.cc.gw.ID,
		CartKey:          pc.cc.gw.ID.String(),
	})
	return v, nil
}
