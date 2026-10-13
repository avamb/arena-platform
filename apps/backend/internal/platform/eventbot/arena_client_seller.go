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

// PatchSellerProfile switches the Telegram leg of the sales page's contact
// form (PATCH /v1/organizations/{org_id}/seller-profile) and answers the
// profile as stored afterwards.
func (c *ArenaClient) PatchSellerProfile(ctx context.Context, jwt string, orgID uuid.UUID, toTelegram bool) (openapi.SellerProfile, error) {
	var out openapi.SellerProfileEnvelope
	body := map[string]any{"contact_form_to_telegram": toTelegram}
	err := c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/seller-profile", jwt, body, &out)
	return out.SellerProfile, err
}

// PatchOrganization sends the given keys to PATCH /v1/organizations/{id}: a
// missing key keeps the stored value, nil or "" clears it.
func (c *ArenaClient) PatchOrganization(ctx context.Context, jwt string, orgID uuid.UUID, body map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String(), jwt, body, nil)
}

// PatchSellerDisplay sends the given keys to PATCH
// .../channels/{channel_id}/seller-display and answers the profile as stored
// afterwards.
func (c *ArenaClient) PatchSellerDisplay(ctx context.Context, jwt string, orgID, channelID uuid.UUID, body map[string]any) (openapi.SellerProfile, error) {
	var out openapi.SellerProfileEnvelope
	err := c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/seller-display", jwt, body, &out)
	return out.SellerProfile, err
}
