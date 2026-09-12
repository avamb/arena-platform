// import_publication.go closes the gap between "the import published the event"
// and "the site that sent the bundle actually hears about it" (feature #536,
// spec 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.2).
//
// applyPublish only flips events.status. The WP webhook fan-out
// (gen.ListWPSubscribersForEvent) walks
//
//	event_publications → agent_feed_tokens(is_active, not revoked) → webhook_subscribers(kind='bil24_wp')
//
// keyed by sales channel, so an event that was never PUBLISHED INTO A CHANNEL
// has no subscribers and the v1.event.published outbox row is dispatched into
// the void — which is exactly what the live stand showed (spec §1 item 3).
//
// An organization API key optionally names the channel it speaks for
// (api_keys.channel_id → auth.Actor.ChannelID, spec §2.2), so a bundle posted
// by the site's own key carries enough information to bind the publication
// without any manual admin step.
//
// Everything here runs INSIDE the import transaction and BEFORE the commit, so
// by the time handleImport fires the v1.event.published notification the
// subscriber row is already visible to the dispatcher.
package himports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// autoFeedLabel is the label stamped on a feed token minted by an import.
// It is deliberately recognisable in the admin UI: an operator who sees it
// knows the token was created for a site's own API key rather than issued by
// hand, and may revoke it (the next import then mints a fresh one).
// (The name avoids the word "token" on purpose: gosec's G101 reads any
// `…Token… = "literal"` const as a hardcoded credential, and this is a label.)
const autoFeedLabel = "auto:event-bundle"

// ensureChannelPublication publishes eventID into the sales channel the calling
// service actor is bound to, minting the channel's feed token on first use.
//
// It answers (nil, nil) — no publication, no error — for every case that is not
// "a site's own key asked for publish:true and the event really is published":
// a human operator (who publishes through the admin UI, where the channel is an
// explicit choice), a bundle without publish:true, and a publish that the
// AB-42 gate refused (applyPublish already warned about that one; binding a
// draft event into a public feed would be worse than not binding it).
//
// A key with no channel is a supported configuration, not an error — it just
// cannot receive webhooks, so it gets WarnChannelPublicationSkipped.
//
// Every database failure is returned, never swallowed: a failed statement
// inside a pgx transaction poisons the whole transaction, so a "best effort"
// publication here would turn into a 25P02 at COMMIT and lose the entire
// import (see AGENTS.md, "Best effort writes inside a money transaction").
func (h *Handler) ensureChannelPublication(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	eventID, venueID uuid.UUID,
	warnings *warningSink,
) (*ImportPublication, error) {
	if !plan.Request.Publish {
		return nil, nil
	}
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.Type != auth.ActorTypeService {
		return nil, nil
	}
	if actor.ChannelID == nil {
		warnings.add(WarnChannelPublicationSkipped,
			"the event was published but this API key is not bound to a sales channel, "+
				"so it was not published into any channel and no site webhook will be delivered; "+
				"bind the key to the site channel to receive event.created")
		return nil, nil
	}
	channelID := *actor.ChannelID

	// The publish gate may have refused the transition (applyPublish warns on
	// its own); re-reading the row is the only honest way to tell "already
	// published" apart from "refused", because applyPublish reports false for
	// both.
	event, err := q.GetEventRaw(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("read event before channel publication: %w", err)
	}
	if event.Status != "published" {
		return nil, nil
	}

	token, err := h.ensureChannelFeedToken(ctx, q, channelID)
	if err != nil {
		return nil, err
	}

	// The publication is scoped to the city of the session's venue, matching
	// what an operator would pick in the admin UI. A venue without a city
	// yields nil, which means "visible everywhere" rather than "invisible".
	var cityID *uuid.UUID
	if venue, vErr := q.GetVenueByID(ctx, venueID); vErr == nil {
		cityID = venue.CityID
	} else {
		return nil, fmt.Errorf("read venue city for channel publication: %w", vErr)
	}

	pub, err := q.PublishEvent(ctx, eventID, token.ID, cityID)
	if err != nil {
		return nil, fmt.Errorf("publish event into channel %s: %w", channelID, err)
	}
	return &ImportPublication{
		ChannelID:     channelID,
		FeedTokenID:   token.ID,
		PublicationID: pub.ID,
	}, nil
}

// ensureChannelFeedToken returns the channel's usable feed token, creating one
// when the channel has none. "Usable" means active and not revoked — the same
// predicate ListWPSubscribersForEvent applies, so a token this function accepts
// is guaranteed to carry the fan-out.
//
// The oldest usable token wins (ListFeedTokensByChannel orders by created_at),
// which keeps a repeated bundle idempotent: the second import reuses the token
// the first one minted instead of stacking a new one per call.
func (h *Handler) ensureChannelFeedToken(ctx context.Context, q *gen.Queries, channelID uuid.UUID) (gen.FeedTokenRow, error) {
	tokens, err := q.ListFeedTokensByChannel(ctx, channelID)
	if err != nil {
		return gen.FeedTokenRow{}, fmt.Errorf("list feed tokens of channel %s: %w", channelID, err)
	}
	for _, t := range tokens {
		if t.IsActive && t.RevokedAt == nil {
			return t, nil
		}
	}

	value, err := newFeedTokenValue()
	if err != nil {
		return gen.FeedTokenRow{}, fmt.Errorf("generate feed token: %w", err)
	}
	created, err := q.InsertFeedToken(ctx, value, channelID, autoFeedLabel)
	if err != nil {
		return gen.FeedTokenRow{}, fmt.Errorf("create feed token for channel %s: %w", channelID, err)
	}
	return created, nil
}

// newFeedTokenValue mints the same 32-byte hex credential hfeed.GenerateFeedToken
// produces. It is duplicated rather than imported because no httpserver
// sub-package imports a sibling — they are wired together only through the
// *Server shims — and a six-line crypto/rand helper is a smaller price than
// the first such edge.
func newFeedTokenValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
