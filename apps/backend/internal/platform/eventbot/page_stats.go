package eventbot

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// pageStatsDays is the window of the figure on the event card.
const pageStatsDays = 30

// PageStats reads the hosted page's beacon counters of an event for the last
// 30 days (GET .../events/{event_id}/page-stats, HP-2a). The default window of
// the route is exactly that, so the call names no dates.
func (c *ArenaClient) PageStats(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.PageStatsResponse, error) {
	var out openapi.PageStatsResponse
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/page-stats"
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// pageStatsLine is the card's "Page views" line: every page view counted in
// the window, and how many of them look like bots. Empty when nobody opened
// the page, so an event without a hosted page shows no line.
func (b *Bot) pageStatsLine(ctx context.Context, loc, jwt string, orgID, eventID uuid.UUID) string {
	st, err := b.arena.PageStats(ctx, jwt, orgID, eventID)
	if err != nil {
		// A card must never fail over a guide figure.
		b.logger.Warn("eventbot: page stats failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		return ""
	}
	return b.pageStatsText(loc, st.Totals.PageView)
}

// pageStatsText renders the line for one set of counters.
func (b *Bot) pageStatsText(loc string, v openapi.PageStatsCounts) string {
	total := v.Hits + v.SuspectHits
	if total <= 0 {
		return ""
	}
	return "\n" + b.texts.T(loc, "bot.ec.page_stats_line", map[string]any{
		"Views": total, "Days": pageStatsDays, "Suspect": v.SuspectHits,
	})
}
