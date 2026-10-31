// Package invitations holds the pieces of staff invitations with seats on
// the plan (spec 08_architecture/46_staff_invitations_seated_ru.md) that are
// not HTTP: the hidden "Invitations" sales channel every invitation order is
// written on.
package invitations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// InternalChannelName is the name of the hidden channel. Nobody outside the
// platform sees it: every organizer-, site- and buyer-facing channel read
// filters kind = 'internal' out (gen.GetSalesChannelByID and friends).
const InternalChannelName = "Invitations"

// internalChannelQueries is what EnsureInternalChannel needs; *gen.Queries
// implements it, bound to a pool or to the caller's transaction.
type internalChannelQueries interface {
	InsertInternalChannel(ctx context.Context, orgID uuid.UUID, name string) (uuid.UUID, error)
	GetInternalChannelID(ctx context.Context, orgID uuid.UUID) (uuid.UUID, error)
}

var _ internalChannelQueries = (*gen.Queries)(nil)

// EnsureInternalChannel returns the organization's internal "Invitations"
// channel and creates it the first time (spec 46, section 3.3): no provider
// account, no gateway credential, no hosted page, no feed token, fee 0.
//
// It reads first and inserts only when there is no channel yet: the insert
// evaluates the display-number sequence's nextval() even when ON CONFLICT
// then drops the row, so inserting on every invitation burnt one channel
// number (the site-visible fid) per issue. The insert itself stays race-safe:
// it is ON CONFLICT DO NOTHING on the partial unique index "one internal
// channel per organization", so two first invitations at once end with ONE
// channel, and the loser reads the winner's id. Inside a transaction the
// loser's insert waits for the winner's commit, so call it on the issuing
// transaction itself.
func EnsureInternalChannel(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (uuid.UUID, error) {
	return ensureInternalChannel(ctx, q, orgID)
}

func ensureInternalChannel(ctx context.Context, q internalChannelQueries, orgID uuid.UUID) (uuid.UUID, error) {
	if q == nil {
		return uuid.Nil, errors.New("invitations: no queries")
	}
	if orgID == uuid.Nil {
		return uuid.Nil, errors.New("invitations: organization id is required")
	}
	id, err := q.GetInternalChannelID(ctx, orgID)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("invitations: read internal channel: %w", err)
	}
	id, err = q.InsertInternalChannel(ctx, orgID, InternalChannelName)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("invitations: create internal channel: %w", err)
	}
	// Lost the race: the winner's row is visible now.
	id, err = q.GetInternalChannelID(ctx, orgID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invitations: read internal channel: %w", err)
	}
	return id, nil
}
