// Package eventpublication binds a published event to the sales channel it is
// sold through (UXH-22).
//
// An event is public only while it has an event_publications row: the hosted
// page, the widget feed and the site webhook fan-out all walk
//
//	event_publications -> agent_feed_tokens -> sales_channels
//
// The Telegram bot's event-bundle import creates that row itself (it names
// channelIds). The web event center saves the event with publish:false and
// flips the status later through POST .../events/{id}/status, which used to
// create nothing, so a "published" event answered 404 on its own page.
// BindOnPublish closes that gap inside the status transition's transaction.
//
// Every function takes a *gen.Queries already bound to the caller's
// transaction and returns every error: a failed statement poisons a pgx
// transaction, so nothing here is best effort (AGENTS.md).
package eventpublication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// AutoFeedLabel is the label stamped on a feed token minted automatically,
// recognisable in the admin UI so an operator can tell it from a hand-issued
// one and revoke it (the next publish then mints a fresh one). The name avoids
// the word "token" on purpose: gosec's G101 reads any `...Token... = "literal"`
// const as a hardcoded credential.
const AutoFeedLabel = "auto:event-bundle"

// Binding is one publication written by BindOnPublish.
type Binding struct {
	ChannelID     uuid.UUID
	FeedTokenID   uuid.UUID
	PublicationID uuid.UUID
	// HostedPageEnabled reports that this call switched the channel's hosted
	// page on (it was off before).
	HostedPageEnabled bool
}

// BindOnPublish gives an event that is being published a sales channel when it
// has none. q must be bound to the transaction that flips the event's status.
//
// It writes nothing, and reports no binding, when the event already has any
// publication (unpublishing keeps the rows, so publishing again restores the
// same channels) or when the organization has no channel that can take it.
//
// Eligible channels are the organization's live channels WITHOUT a gateway
// credential: a channel with one is a WordPress site selling through the
// Bil24 gateway, and an event is never pushed into a live client's site
// automatically. One eligible channel is used; of several, the ones whose
// hosted page is already on, else the oldest. The chosen channel's hosted page
// is switched on, an active feed token is ensured and the event is published
// into it, scoped to the city of the event's first session's venue.
func BindOnPublish(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID) ([]Binding, error) {
	existing, err := q.ListPublicationsByEvent(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list publications of event %s: %w", eventID, err)
	}
	if len(existing) > 0 {
		return nil, nil
	}
	channels, err := q.ListBindableChannelsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list channels of organization %s: %w", orgID, err)
	}
	targets := ChooseChannels(channels)
	if len(targets) == 0 {
		return nil, nil
	}

	cityID, err := eventCityID(ctx, q, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]Binding, 0, len(targets))
	for _, channelID := range targets {
		enabled, err := q.EnableChannelHostedPage(ctx, channelID, orgID)
		if err != nil {
			return nil, fmt.Errorf("enable hosted page for channel %s: %w", channelID, err)
		}
		token, err := EnsureChannelFeedToken(ctx, q, channelID)
		if err != nil {
			return nil, err
		}
		pub, err := q.PublishEvent(ctx, eventID, token.ID, cityID)
		if err != nil {
			return nil, fmt.Errorf("publish event into channel %s: %w", channelID, err)
		}
		out = append(out, Binding{
			ChannelID:         channelID,
			FeedTokenID:       token.ID,
			PublicationID:     pub.ID,
			HostedPageEnabled: enabled,
		})
	}
	return out, nil
}

// ChooseChannels applies the selection rule to the eligible channels, which
// must arrive oldest first: none -> none, one -> that one, several -> those
// whose hosted page is on, and when none is on the oldest.
func ChooseChannels(eligible []gen.BindableChannel) []uuid.UUID {
	switch len(eligible) {
	case 0:
		return nil
	case 1:
		return []uuid.UUID{eligible[0].ID}
	}
	var enabled []uuid.UUID
	for _, c := range eligible {
		if c.HostedPageEnabled {
			enabled = append(enabled, c.ID)
		}
	}
	if len(enabled) > 0 {
		return enabled
	}
	return []uuid.UUID{eligible[0].ID}
}

// eventCityID is the city of the venue of the event's first session, matching
// what an operator picks in the admin UI. A venue without a city, or an event
// with no session, yields nil, which means "visible everywhere".
func eventCityID(ctx context.Context, q *gen.Queries, eventID uuid.UUID) (*uuid.UUID, error) {
	sessions, err := q.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list sessions of event %s: %w", eventID, err)
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	venue, err := q.GetVenueByID(ctx, sessions[0].VenueID)
	if err != nil {
		return nil, fmt.Errorf("read venue city for event publication: %w", err)
	}
	return venue.CityID, nil
}

// EnsureChannelFeedToken returns the channel's usable feed token, creating one
// when the channel has none. "Usable" means active and not revoked, the same
// predicate ListWPSubscribersForEvent applies. The oldest usable token wins
// (ListFeedTokensByChannel orders by created_at), so a repeated call reuses the
// token the first one minted instead of stacking a new one.
func EnsureChannelFeedToken(ctx context.Context, q *gen.Queries, channelID uuid.UUID) (gen.FeedTokenRow, error) {
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
	created, err := q.InsertFeedToken(ctx, value, channelID, AutoFeedLabel)
	if err != nil {
		return gen.FeedTokenRow{}, fmt.Errorf("create feed token for channel %s: %w", channelID, err)
	}
	return created, nil
}

// newFeedTokenValue mints the same 32-byte hex credential hfeed.GenerateFeedToken
// produces.
func newFeedTokenValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
