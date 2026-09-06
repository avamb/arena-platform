// types.go holds the response DTOs for the api-keys HTTP surface. None of
// these types include key_hash — the bcrypt digest never leaves the
// database boundary.
package hapikeys

import (
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// APIKeyResponse is the shape returned by list and revoke. It deliberately
// omits key_hash.
type APIKeyResponse struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	ChannelID  *uuid.UUID `json:"channel_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  uuid.UUID  `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// APIKeyFromRow projects a gen.APIKeyRow into the wire response shape,
// dropping KeyHash.
func APIKeyFromRow(row gen.APIKeyRow) APIKeyResponse {
	return APIKeyResponse{
		ID:         row.ID,
		OrgID:      row.OrgID,
		ChannelID:  row.ChannelID,
		Name:       row.Name,
		KeyPrefix:  row.KeyPrefix,
		Scopes:     row.Scopes,
		CreatedBy:  row.CreatedBy,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
		ExpiresAt:  row.ExpiresAt,
		RevokedAt:  row.RevokedAt,
	}
}

// CreateAPIKeyResponse is the one-shot response returned by POST. `APIKey`
// is the ONLY moment the raw wire token is visible: it is never persisted
// and no later GET/LIST response ever includes it again.
type CreateAPIKeyResponse struct {
	APIKeyResponse
	APIKey string `json:"api_key"`
}

// createAPIKeyRequest is the strictly-decoded POST request body.
type createAPIKeyRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ChannelID *uuid.UUID `json:"channel_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}
