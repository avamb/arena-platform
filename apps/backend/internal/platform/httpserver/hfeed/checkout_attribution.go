package hfeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

// orderAttribution is the attribution of one public checkout (spec 43
// section 4.4), cleaned and resolved BEFORE the hold transaction opens. Inside
// the checkout transactions it is only written: nothing here reads the pool
// while a transaction is open (AGENTS.md hold-transaction rule).
type orderAttribution struct {
	// json is the stored form of the cleaned object, nil when the request
	// carried none or nothing usable (the columns then hold '{}').
	json []byte
	// linkID is the sales link of the organization the ref names, nil when
	// there is no ref, the code is unknown or belongs to another organization.
	linkID *uuid.UUID
}

// resolveCheckoutAttribution cleans the client's attribution object and turns
// its ref into a sales link of orgID. It never fails the purchase: an empty or
// invalid object is silently nothing, and a failed link lookup is logged and
// treated as "no link" while the cleaned json is still kept.
func (h *Handler) resolveCheckoutAttribution(ctx context.Context, orgID uuid.UUID, raw json.RawMessage, now time.Time) orderAttribution {
	if len(raw) == 0 {
		return orderAttribution{}
	}
	a, ok := attribution.Clean(raw, now)
	if !ok || a.IsZero() {
		return orderAttribution{}
	}
	out := orderAttribution{json: a.JSON()}
	if h.publicFeedQueries == nil {
		return out
	}
	linkID, err := attribution.ResolveLink(ctx, h.publicFeedQueries, orgID, a)
	if err != nil {
		h.logger.Warn("public_feed_checkout: sales link lookup failed, the order keeps the attribution without a link",
			slog.String("org_id", orgID.String()),
			slog.String("error", err.Error()),
		)
		return out
	}
	out.linkID = linkID
	return out
}
