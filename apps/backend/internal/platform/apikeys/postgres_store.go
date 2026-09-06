// postgres_store.go — the gen.Queries-backed Store implementation.
//
// This is the ONLY file in this package that imports the postgres/gen
// wrappers, following the same split used by internal/platform/customers.
package apikeys

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// NewStoreFromQueries wraps a *gen.Queries as a Store. The returned Store
// executes every query against whatever DBTX q was constructed with (pool
// or tx) — no fresh acquisition happens here.
func NewStoreFromQueries(q *gen.Queries) Store {
	return &queriesStore{q: q}
}

type queriesStore struct {
	q *gen.Queries
}

func (s *queriesStore) InsertAPIKey(
	ctx context.Context,
	orgID uuid.UUID,
	channelID *uuid.UUID,
	name, keyPrefix, keyHash string,
	scopes []string,
	createdBy uuid.UUID,
	expiresAt *time.Time,
) (APIKey, error) {
	row, err := s.q.InsertAPIKey(ctx, orgID, channelID, name, keyPrefix, keyHash, scopes, createdBy, expiresAt)
	if err != nil {
		return APIKey{}, err
	}
	return apiKeyFromRow(row), nil
}

func (s *queriesStore) GetAPIKeyByPrefix(ctx context.Context, keyPrefix string) (APIKey, error) {
	row, err := s.q.GetAPIKeyByPrefix(ctx, keyPrefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, ErrNotFound
		}
		return APIKey{}, err
	}
	return apiKeyFromRow(row), nil
}

func (s *queriesStore) TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.q.TouchAPIKeyLastUsed(ctx, id, at)
}

func apiKeyFromRow(r gen.APIKeyRow) APIKey {
	return APIKey{
		ID:         r.ID,
		OrgID:      r.OrgID,
		ChannelID:  r.ChannelID,
		Name:       r.Name,
		KeyPrefix:  r.KeyPrefix,
		KeyHash:    r.KeyHash,
		Scopes:     r.Scopes,
		CreatedBy:  r.CreatedBy,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: r.LastUsedAt,
		ExpiresAt:  r.ExpiresAt,
		RevokedAt:  r.RevokedAt,
	}
}
