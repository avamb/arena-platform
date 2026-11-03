package eventbot

// arena_client_smart_price.go — the calls behind the bot's smart price screen
// (spec 08_architecture/51_4_2_smart_price_ru.md). arena-api builds every step
// and every price (himports/smart_price.go): the bot only carries the answers.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func smartPricePath(orgID, sessionID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/smart-price"
}

// SmartPriceRules lists the session's presets, with where an active one stands now.
func (c *ArenaClient) SmartPriceRules(ctx context.Context, jwt string, orgID, sessionID uuid.UUID) ([]openapi.SmartPriceRule, error) {
	var out openapi.SmartPriceRulesResponse
	err := c.do(ctx, http.MethodGet, smartPricePath(orgID, sessionID), jwt, nil, &out)
	return out.Rules, err
}

// SmartPricePreview asks the steps or windows a preset would make. It writes nothing.
func (c *ArenaClient) SmartPricePreview(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, body openapi.SmartPriceRequest) (openapi.SmartPricePreviewResponse, error) {
	var out openapi.SmartPricePreviewResponse
	err := c.do(ctx, http.MethodPost, smartPricePath(orgID, sessionID)+"/preview", jwt, body, &out)
	return out, err
}

// SmartPriceEnable turns a preset on: the steps or windows are written through the event import.
func (c *ArenaClient) SmartPriceEnable(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, body openapi.SmartPriceRequest) ([]openapi.SmartPriceRule, error) {
	var out openapi.SmartPriceRulesResponse
	err := c.do(ctx, http.MethodPost, smartPricePath(orgID, sessionID), jwt, body, &out)
	return out.Rules, err
}

// SmartPriceStop stops a preset: the price stays where it is now.
func (c *ArenaClient) SmartPriceStop(ctx context.Context, jwt string, orgID, sessionID, ruleID uuid.UUID) error {
	return c.do(ctx, http.MethodPost, smartPricePath(orgID, sessionID)+"/"+ruleID.String()+"/stop", jwt, map[string]any{}, nil)
}
