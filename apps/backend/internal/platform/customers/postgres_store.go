// postgres_store.go — the gen.Queries-backed Store implementation.
//
// This is the ONLY file in this package that imports the postgres/gen
// wrappers. Callers that already hold a *gen.Queries (usually built with
// gen.New(pool) or q.WithTx(tx)) construct a Store via NewStoreFromQueries
// and hand it to Resolve / Touch / MarkVerified / LinkOrg.

package customers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// NewStoreFromQueries wraps a *gen.Queries as a Store. The returned
// Store executes every query against whatever DBTX q was constructed with
// (pool or tx) — no fresh acquisition happens here.
func NewStoreFromQueries(q *gen.Queries) Store {
	return &queriesStore{q: q}
}

type queriesStore struct {
	q *gen.Queries
}

func (s *queriesStore) GetIdentityByStrong(ctx context.Context, kind IdentityKind, value string) (Identity, error) {
	row, err := s.q.GetCustomerIdentityByStrongKey(ctx, string(kind), value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

func (s *queriesStore) GetIdentityByWeak(ctx context.Context, kind IdentityKind, value string, channelID uuid.UUID) (Identity, error) {
	row, err := s.q.GetCustomerIdentityByWeakKey(ctx, string(kind), value, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

func (s *queriesStore) InsertCustomer(ctx context.Context, displayName, locale string) (Customer, error) {
	var dn, loc *string
	if displayName != "" {
		dn = &displayName
	}
	if locale != "" {
		loc = &locale
	}
	row, err := s.q.InsertCustomer(ctx, dn, loc)
	if err != nil {
		return Customer{}, err
	}
	return customerFromRow(row), nil
}

func (s *queriesStore) InsertIdentity(
	ctx context.Context,
	customerID uuid.UUID,
	kind IdentityKind,
	value string,
	channelID *uuid.UUID,
	source string,
	verifiedAt *time.Time,
) (Identity, error) {
	// Strong identities MUST NOT have a channel_id; the partial unique
	// index does not scope them. Weak identities require one.
	if kind.IsStrong() && channelID != nil {
		channelID = nil
	}
	if kind.IsWeak() && channelID == nil {
		return Identity{}, ErrChannelRequiredForWeak
	}
	if source == "" {
		source = SourceLive
	}
	row, err := s.q.InsertCustomerIdentity(ctx, customerID, string(kind), value, channelID, verifiedAt, source)
	if err != nil {
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

func (s *queriesStore) UpdateDisplayName(ctx context.Context, customerID uuid.UUID, displayName string) error {
	return s.q.UpdateCustomerDisplayName(ctx, customerID, displayName)
}

func (s *queriesStore) TouchIdentity(ctx context.Context, id uuid.UUID) error {
	return s.q.TouchCustomerIdentityLastSeen(ctx, id)
}

func (s *queriesStore) MarkIdentityVerified(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.q.MarkCustomerIdentityVerified(ctx, id, at)
}

func (s *queriesStore) UpsertOrgLink(ctx context.Context, customerID, orgID uuid.UUID, source string) error {
	return s.q.UpsertCustomerOrgLink(ctx, customerID, orgID, source)
}

func (s *queriesStore) InsertMergeCandidate(ctx context.Context, a, b uuid.UUID, reason string) error {
	_, err := s.q.InsertCustomerMergeCandidate(ctx, a, b, reason)
	return err
}

func (s *queriesStore) InsertAttribute(ctx context.Context, customerID uuid.UUID, orgID *uuid.UUID, key, valueJSON, source string) error {
	return s.q.InsertCustomerAttribute(ctx, customerID, orgID, key, valueJSON, source)
}

func (s *queriesStore) GetCustomer(ctx context.Context, id uuid.UUID) (Customer, error) {
	row, err := s.q.GetCustomerByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Customer{}, ErrNotFound
		}
		return Customer{}, err
	}
	return customerFromRow(row), nil
}

func customerFromRow(r gen.CustomerRow) Customer {
	c := Customer{
		ID:        r.ID,
		SystemID:  r.SystemID,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.DisplayName != nil {
		c.DisplayName = *r.DisplayName
	}
	if r.Locale != nil {
		c.Locale = *r.Locale
	}
	return c
}

func identityFromRow(r gen.CustomerIdentityRow) Identity {
	return Identity{
		ID:              r.ID,
		CustomerID:      r.CustomerID,
		Kind:            IdentityKind(r.Kind),
		ValueNormalized: r.ValueNormalized,
		ChannelID:       r.ChannelID,
		VerifiedAt:      r.VerifiedAt,
		FirstSeenAt:     r.FirstSeenAt,
		LastSeenAt:      r.LastSeenAt,
		Source:          r.Source,
	}
}
