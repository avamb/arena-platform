package hbil24

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

// gatewayAttribution is CREATE_ORDER_EXT's attribution (spec 43 section 4.4),
// cleaned and resolved on the pool BEFORE the order transaction opens. Inside
// the transaction it is only written.
type gatewayAttribution struct {
	// present is true when the request carried an attribution that survived
	// attribution.Clean. A request without one (or with an unusable one)
	// leaves an existing order's attribution as it is.
	present bool
	attr    attribution.Attribution
	// linkID is the sales link of the order's organization the ref names;
	// nil for no ref, an unknown code or another organization's link.
	linkID *uuid.UUID
	// lookupFailed is true when the sales-link lookup failed (a transient
	// database error, not "no such link"). A new order still gets the json
	// without a link, but a re-send must not overwrite the order's stored
	// link with NULL on a guess: it keeps what the order has.
	lookupFailed bool
}

// json is the stored form for a NEW checkout session or order: nil when the
// request carried none (the columns then hold '{}').
func (g gatewayAttribution) json() []byte {
	if !g.present {
		return nil
	}
	return g.attr.JSON()
}

// orderAttribution cleans req.Attribution and resolves its ref within the
// gateway channel's organization. It never refuses the order: a bad object is
// nothing, and a failed link lookup is logged and treated as "no link".
// It must run before the order transaction opens (it reads the pool).
func (h *Handler) orderAttribution(ctx context.Context, req bil24Request, cc cartCtx) gatewayAttribution {
	if len(req.Attribution) == 0 {
		return gatewayAttribution{}
	}
	a, ok := attribution.Clean(req.Attribution, time.Now())
	if !ok || a.IsZero() {
		return gatewayAttribution{}
	}
	out := gatewayAttribution{present: true, attr: a}
	if h.orderDeps.Q == nil {
		return out
	}
	linkID, err := attribution.ResolveLink(ctx, h.orderDeps.Q, cc.orgID, a)
	if err != nil {
		h.logger.Warn("bil24_compat: CREATE_ORDER_EXT: sales link lookup failed, the order keeps the attribution without a link",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("org_id", cc.orgID.String()),
			slog.String("error", err.Error()),
		)
		out.lookupFailed = true
		return out
	}
	out.linkID = linkID
	return out
}

// forUpdate returns what a same-cart re-send writes onto the EXISTING order:
// nothing (the order keeps its attribution) when the request carried none,
// otherwise the new touch with the order's earlier touch kept as "first"
// (attribution.MergeTouch, last touch wins). A failed link lookup also
// writes nothing: the order keeps its link rather than losing it to a
// transient error.
func (g gatewayAttribution) forUpdate(existing gen.OrderRow) (*uuid.UUID, json.RawMessage) {
	if !g.present || g.lookupFailed {
		return nil, nil
	}
	var prev attribution.Attribution
	if len(existing.Attribution) > 0 {
		if err := json.Unmarshal(existing.Attribution, &prev); err != nil {
			prev = attribution.Attribution{}
		}
	}
	return g.linkID, attribution.MergeTouch(prev, g.attr).JSON()
}
