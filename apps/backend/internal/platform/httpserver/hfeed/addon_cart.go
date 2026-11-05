package hfeed

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// checkAddonCart refuses a cart of add-on tickets only (migration 0197:
// "extra child" without "child + adult") with 409 tier.addon_requires_main.
// A seat is always a main ticket. It reads on the pool BEFORE the hold
// transaction opens (AGENTS.md: no pool read inside an open transaction) and
// reports whether the request may go on.
func (h *Handler) checkAddonCart(ctx context.Context, w http.ResponseWriter, r *http.Request, hasSeats bool, gaTierIDs []uuid.UUID) bool {
	if hasSeats || len(gaTierIDs) == 0 || h.tierQueries == nil {
		return true
	}
	err := hcheckout.CheckAddonCompanions(ctx, h.tierQueries, gaTierIDs)
	if err == nil {
		return true
	}
	if hcheckout.CategoryGateErrorCode(err) == "" {
		h.logger.Error("public_feed: add-on check failed", slog.String("error", err.Error()))
	}
	hcheckout.WriteCategoryGateError(w, r, err)
	return false
}
