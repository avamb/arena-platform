package eventbot

// arena_client_promoters.go — the calls behind the bot's "Promoters" screens
// (EC-14, spec 35 §6.5). The rules live in arena-api (hcatalog/promoters.go):
// the name's uniqueness among active promoters, the page address' alphabet and
// its platform-wide uniqueness (shared with organization slugs), the website
// and address limits. The bot only carries the answers. A promoter's e-mail
// and phone are contact data of a third party: nothing here logs a body.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The API's codes for a refused promoter edit.
const (
	promoterCodeDuplicateName = "promoter.duplicate_name"
	promoterCodeDuplicateSlug = "promoter.duplicate_slug"
	promoterCodeInvalidSlug   = "promoter.invalid_slug"
	promoterCodeInvalidName   = "promoter.invalid_name"
	promoterCodeInvalidWeb    = "promoter.invalid_website"
	promoterCodeInvalidAddr   = "promoter.invalid_address"
)

// PromoterList returns the organization's ACTIVE promoters ordered by name,
// each with every field the card shows.
func (c *ArenaClient) PromoterList(ctx context.Context, jwt string, orgID uuid.UUID) ([]openapi.Promoter, error) {
	var out openapi.PromoterListEnvelope
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/promoters", jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Promoters, nil
}

// PatchPromoter sends the given keys to PATCH .../promoters/{id}: a missing
// key keeps the stored value, a nil value clears it (name cannot be cleared),
// "archived" archives. The answer is the promoter as stored afterwards.
func (c *ArenaClient) PatchPromoter(ctx context.Context, jwt string, orgID, promoterID uuid.UUID, body map[string]any) (openapi.Promoter, error) {
	var out openapi.PromoterEnvelope
	err := c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/promoters/"+promoterID.String(), jwt, body, &out)
	return out.Promoter, err
}
