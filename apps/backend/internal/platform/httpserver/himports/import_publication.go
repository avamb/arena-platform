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
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
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

// ensureChannelPublication publishes eventID into every sales channel the
// import is asked to bind it to, minting each channel's feed token on first
// use, and returns the bindings in order.
//
// The targets are the calling service actor's own channel (an organization
// API key bound to a site, feature #536, spec 22 §2.2) followed by the
// request's channelIds (spec 28 §3.4 — the Telegram event-center bot acts as
// a human operator and has no key, so it names the channels explicitly;
// resolveChannelIDs already proved every one belongs to the organization).
//
// It answers (nil, nil) — no publication, no error — for every case that is
// not "publish:true and the event really is published": a bundle without
// publish:true, a caller with no target at all, and a publish that the AB-42
// gate refused (applyPublish already warned about that one; binding a draft
// event into a public feed would be worse than not binding it).
//
// A key with no channel and no channelIds is a supported configuration, not
// an error — it just cannot receive webhooks, so it gets
// WarnChannelPublicationSkipped.
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
) ([]ImportPublication, error) {
	if !plan.Request.Publish {
		return nil, nil
	}
	targets := make([]uuid.UUID, 0, len(plan.ChannelIDs)+1)
	seen := make(map[uuid.UUID]bool, len(plan.ChannelIDs)+1)
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			targets = append(targets, id)
		}
	}
	if actor, ok := auth.ActorFromContext(ctx); ok && actor.Type == auth.ActorTypeService {
		if actor.ChannelID != nil {
			add(*actor.ChannelID)
		} else if len(plan.ChannelIDs) == 0 {
			warnings.add(WarnChannelPublicationSkipped,
				"the event was published but this API key is not bound to a sales channel, "+
					"so it was not published into any channel and no site webhook will be delivered; "+
					"bind the key to the site channel to receive event.created")
			return nil, nil
		}
	}
	for _, id := range plan.ChannelIDs {
		add(id)
	}
	if len(targets) == 0 {
		return nil, nil
	}

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

	// The publication is scoped to the city of the session's venue, matching
	// what an operator would pick in the admin UI. A venue without a city
	// yields nil, which means "visible everywhere" rather than "invisible".
	var cityID *uuid.UUID
	if venue, vErr := q.GetVenueByID(ctx, venueID); vErr == nil {
		cityID = venue.CityID
	} else {
		return nil, fmt.Errorf("read venue city for channel publication: %w", vErr)
	}

	out := make([]ImportPublication, 0, len(targets))
	for _, channelID := range targets {
		token, err := h.ensureChannelFeedToken(ctx, q, channelID)
		if err != nil {
			return nil, err
		}
		pub, err := q.PublishEvent(ctx, eventID, token.ID, cityID)
		if err != nil {
			return nil, fmt.Errorf("publish event into channel %s: %w", channelID, err)
		}
		out = append(out, ImportPublication{
			ChannelID:     channelID,
			FeedTokenID:   token.ID,
			PublicationID: pub.ID,
		})
	}
	return out, nil
}

// resolveChannelIDs parses the request's channelIds and proves each one is a
// sales channel of orgID. It runs before the import transaction, so a caller
// error costs no write. A nil Handler.queries (unit tests without a
// database) accepts an empty list and refuses a non-empty one.
func (h *Handler) resolveChannelIDs(ctx context.Context, orgID uuid.UUID, req bil24compat.ImportSessionRequest) ([]uuid.UUID, error) {
	if len(req.ChannelIDs) == 0 {
		return nil, nil
	}
	out := make([]uuid.UUID, 0, len(req.ChannelIDs))
	seen := make(map[uuid.UUID]bool, len(req.ChannelIDs))
	for _, raw := range req.ChannelIDs {
		raw = trimSpace(raw)
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_channel",
				"channelIds entry "+raw+" is not a UUID")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if h.queries == nil {
			return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_channel",
				"channelIds cannot be verified without a database")
		}
		if _, err := h.queries.GetSalesChannelByID(ctx, id, orgID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_channel",
					"channelIds entry "+raw+" is not a sales channel of this organization")
			}
			return nil, fmt.Errorf("read sales channel %s: %w", id, err)
		}
		out = append(out, id)
	}
	return out, nil
}

// firstPublication keeps the response's singular `publication` field: the
// first binding, or nil when none happened.
func firstPublication(pubs []ImportPublication) *ImportPublication {
	if len(pubs) == 0 {
		return nil
	}
	first := pubs[0]
	return &first
}

// nonNilPublications makes the response's `publications` an array even when
// nothing was bound (the contract promises an array, never null).
func nonNilPublications(pubs []ImportPublication) []ImportPublication {
	if pubs == nil {
		return []ImportPublication{}
	}
	return pubs
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
