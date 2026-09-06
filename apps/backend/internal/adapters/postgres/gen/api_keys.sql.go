// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: api_keys.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// APIKeyRow — shared result type for all api_keys queries
// ─────────────────────────────────────────────────────────────────────────────

// APIKeyRow is the result type returned by all api_keys queries. KeyHash is
// the bcrypt digest of the secret half of the wire token; the raw secret
// itself is never persisted and is not part of this row.
type APIKeyRow struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	ChannelID  *uuid.UUID `json:"channel_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	KeyHash    string     `json:"key_hash"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  uuid.UUID  `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// scanAPIKeyRow scans a single api_keys row.
func scanAPIKeyRow(row interface {
	Scan(dest ...any) error
}) (APIKeyRow, error) {
	var k APIKeyRow
	err := row.Scan(
		&k.ID,
		&k.OrgID,
		&k.ChannelID,
		&k.Name,
		&k.KeyPrefix,
		&k.KeyHash,
		&k.Scopes,
		&k.CreatedBy,
		&k.CreatedAt,
		&k.LastUsedAt,
		&k.ExpiresAt,
		&k.RevokedAt,
	)
	return k, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertAPIKey
// ─────────────────────────────────────────────────────────────────────────────

const insertAPIKey = `-- name: InsertAPIKey :one
INSERT INTO api_keys (
    org_id, channel_id, name, key_prefix, key_hash, scopes, created_by, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
          created_at, last_used_at, expires_at, revoked_at`

// InsertAPIKey creates a new api_keys row. channelID and expiresAt accept nil.
func (q *Queries) InsertAPIKey(
	ctx context.Context,
	orgID uuid.UUID,
	channelID *uuid.UUID,
	name, keyPrefix, keyHash string,
	scopes []string,
	createdBy uuid.UUID,
	expiresAt *time.Time,
) (APIKeyRow, error) {
	row := q.db.QueryRow(ctx, insertAPIKey,
		orgID, channelID, name, keyPrefix, keyHash, scopes, createdBy, expiresAt,
	)
	return scanAPIKeyRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetAPIKeyByPrefix
// ─────────────────────────────────────────────────────────────────────────────

const getAPIKeyByPrefix = `-- name: GetAPIKeyByPrefix :one
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  key_prefix = $1`

// GetAPIKeyByPrefix looks up a key by its 12-char lookup prefix. Returns
// pgx.ErrNoRows when no key has that prefix.
func (q *Queries) GetAPIKeyByPrefix(ctx context.Context, keyPrefix string) (APIKeyRow, error) {
	row := q.db.QueryRow(ctx, getAPIKeyByPrefix, keyPrefix)
	return scanAPIKeyRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetAPIKeyByID
// ─────────────────────────────────────────────────────────────────────────────

const getAPIKeyByID = `-- name: GetAPIKeyByID :one
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  id = $1
  AND  org_id = $2`

// GetAPIKeyByID fetches a key by its UUID PK, scoped to the given org.
func (q *Queries) GetAPIKeyByID(ctx context.Context, id, orgID uuid.UUID) (APIKeyRow, error) {
	row := q.db.QueryRow(ctx, getAPIKeyByID, id, orgID)
	return scanAPIKeyRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListAPIKeysByOrg
// ─────────────────────────────────────────────────────────────────────────────

const listAPIKeysByOrg = `-- name: ListAPIKeysByOrg :many
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  org_id = $1
ORDER  BY created_at DESC, id ASC`

// ListAPIKeysByOrg returns every key (active, expired, or revoked) for the
// given org, newest first.
func (q *Queries) ListAPIKeysByOrg(ctx context.Context, orgID uuid.UUID) ([]APIKeyRow, error) {
	rows, err := q.db.Query(ctx, listAPIKeysByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKeyRow
	for rows.Next() {
		k, err := scanAPIKeyRow(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// TouchAPIKeyLastUsed
// ─────────────────────────────────────────────────────────────────────────────

const touchAPIKeyLastUsed = `-- name: TouchAPIKeyLastUsed :exec
UPDATE api_keys
SET    last_used_at = $2
WHERE  id = $1`

// TouchAPIKeyLastUsed sets last_used_at unconditionally; throttling to
// "at most once a minute" is the caller's (apikeys package) responsibility.
func (q *Queries) TouchAPIKeyLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := q.db.Exec(ctx, touchAPIKeyLastUsed, id, at)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// RevokeAPIKey
// ─────────────────────────────────────────────────────────────────────────────

const revokeAPIKey = `-- name: RevokeAPIKey :exec
UPDATE api_keys
SET    revoked_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  revoked_at IS NULL`

// RevokeAPIKey marks a key revoked. A no-op (no error) if already revoked or
// not found in this org.
func (q *Queries) RevokeAPIKey(ctx context.Context, id, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, revokeAPIKey, id, orgID)
	return err
}
