// tier_color.go — the colour of a session category (task S1d, migration
// 0171). A category carries an optional #rrggbb colour the seat map draws
// its seats with; null means "the colour of the geometry category".
//
// The colour is read and written through queries of its own (not through
// gen.TicketTierRow, whose scanner a dozen SELECT lists feed), so the
// helpers here hydrate a response after it was built from a row.
package hcatalog

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

// parseTierColor reads a tri-state colour: absent = keep, null or "" =
// clear, a value = set (stored lower-case). A malformed value answers
// 400 tier.invalid_color. The rule itself is seatlayout.NormalizeColor, the
// one colour rule of a category (the seat batch's new_tiers use it too).
func parseTierColor(w http.ResponseWriter, r *http.Request, in optionalString) (color *string, set bool, ok bool) {
	if !in.Present {
		return nil, false, true
	}
	if in.Value == nil || strings.TrimSpace(*in.Value) == "" {
		return nil, true, true
	}
	v, valid := seatlayout.NormalizeColor(*in.Value)
	if !valid {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_color", "color must be #rrggbb", r,
			map[string]any{"field": "color"},
		))
		return nil, false, false
	}
	return &v, true, true
}

// withColor fills the colour and the add-on flag (migration 0197) of one
// category. Non-fatal: a failed read leaves the colour null (the flag false)
// and is logged.
func (h *Handler) withColor(ctx context.Context, tr tierResponse) tierResponse {
	id, err := uuid.Parse(tr.ID)
	if err != nil {
		return tr
	}
	sid, err := uuid.Parse(tr.SessionID)
	if err != nil {
		return tr
	}
	c, err := h.tierQueries.GetTicketTierColor(ctx, id, sid)
	if err != nil {
		h.logger.Warn("tier: colour read failed (non-fatal)", slog.String("tier_id", tr.ID), slog.String("error", err.Error()))
		return tr
	}
	tr.Color = c
	if addon, aerr := h.tierQueries.ListAddonTierIDs(ctx, []uuid.UUID{id}); aerr == nil {
		tr.IsAddon = addon[id]
	} else {
		h.logger.Warn("tier: add-on flag read failed (non-fatal)", slog.String("tier_id", tr.ID), slog.String("error", aerr.Error()))
	}
	return tr
}
