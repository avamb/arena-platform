// public_feed_page_url.go — page_url of the public feed's events: the
// buyers' hosted page address of the event, built ONLY by publicurl.Page with
// the PUBLIC_TICKETS_BASE_URL base, exactly as hcatalog.hydratePublicURLs
// does (a promoter's slug wins; an archived promoter is skipped).
package hfeed

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/publicurl"
)

// hydrateFeedPageURLs fills PageURL in two batched reads (promoter links and
// organization slugs). Nothing is read when the server has no tickets base or
// no event has a slug. A failure logs and leaves page_url empty: the address
// is presentational and must not fail the feed.
func (h *Handler) hydrateFeedPageURLs(ctx context.Context, responses []publicFeedEventResponse) {
	base := h.returnURLPolicy.Fallback
	if h.publicFeedQueries == nil || base == "" || len(responses) == 0 {
		return
	}
	seen := map[uuid.UUID]struct{}{}
	orgIDs := make([]uuid.UUID, 0, len(responses))
	eventIDs := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if r.Slug == nil || *r.Slug == "" {
			continue
		}
		if id, err := uuid.Parse(r.ID); err == nil {
			eventIDs = append(eventIDs, id)
		}
		if id, err := uuid.Parse(r.OrgID); err == nil {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				orgIDs = append(orgIDs, id)
			}
		}
	}
	if len(eventIDs) == 0 {
		return
	}
	slugs, err := h.publicFeedQueries.ListOrganizationSlugs(ctx, orgIDs)
	if err != nil {
		h.logger.Warn("public_feed: page url hydration failed", slog.String("error", err.Error()))
		return
	}
	promoters, err := h.publicFeedQueries.ListEventPromoters(ctx, eventIDs)
	if err != nil {
		h.logger.Warn("public_feed: page url promoter read failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		if responses[i].Slug == nil || *responses[i].Slug == "" {
			continue
		}
		eventID, e1 := uuid.Parse(responses[i].ID)
		orgID, e2 := uuid.Parse(responses[i].OrgID)
		if e1 != nil || e2 != nil {
			continue
		}
		promoterSlug := ""
		if ref, ok := promoters[eventID]; ok && !ref.Archived {
			promoterSlug = ref.Slug
		}
		responses[i].PageURL = publicurl.Page(base, slugs[orgID], promoterSlug, *responses[i].Slug)
	}
}
