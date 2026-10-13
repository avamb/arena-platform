package eventbot

// arena_client_seller.go — the calls behind the owner's "Organization data"
// screens (HP-4a): the seller profile, the legal fields (PATCH
// /v1/organizations/{id}, which owns their formats) and the per-channel
// seller block. Nothing here logs a body: it carries an organization's legal
// data and its public contact.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// SellerProfile reads GET /v1/organizations/{org_id}/seller-profile.
func (c *ArenaClient) SellerProfile(ctx context.Context, jwt string, orgID uuid.UUID) (openapi.SellerProfile, error) {
	var out openapi.SellerProfileEnvelope
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/seller-profile", jwt, nil, &out)
	return out.SellerProfile, err
}

// PatchOrganization sends the given keys to PATCH /v1/organizations/{id}: a
// missing key keeps the stored value, nil or "" clears it. It answers how many
// sales pages had their seller block turned off because a required field is
// now empty (seller_display_hidden).
func (c *ArenaClient) PatchOrganization(ctx context.Context, jwt string, orgID uuid.UUID, body map[string]any) (int, error) {
	var out struct {
		Hidden []string `json:"seller_display_hidden"`
	}
	err := c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String(), jwt, body, &out)
	return len(out.Hidden), err
}

// PatchSellerDisplay sends the given keys to PATCH
// .../channels/{channel_id}/seller-display and answers the profile as stored
// afterwards.
func (c *ArenaClient) PatchSellerDisplay(ctx context.Context, jwt string, orgID, channelID uuid.UUID, body map[string]any) (openapi.SellerProfile, error) {
	var out openapi.SellerProfileEnvelope
	err := c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/seller-display", jwt, body, &out)
	return out.SellerProfile, err
}
