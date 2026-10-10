package eventbot

// arena_client_categories.go — the calls behind the bot's category screens
// (EC-15, spec 35 §6.6). The rules live in arena-api (hcatalog/ticket_tiers.go,
// price_schedule.go, gaquota): a quantity never goes below what is sold or
// held (409 tier.quantity_below_used), a seated category's quantity is
// read-only (409 tier.seated_category), a window must end after it starts.
// The bot only carries the answers.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The API's codes for a refused category edit.
const (
	tierCodeBelowUsed     = "tier.quantity_below_used"
	tierCodeSeated        = "tier.seated_category"
	tierCodeInvalidWindow = "tier.invalid_sale_window"
	tierCodeInvalidCap    = "tier.invalid_capacity"
	tierCodeCapRequired   = "tier.capacity_required"
	tierCodeInvalidPrice  = "tier.invalid_price_amount"
)

func tiersPath(orgID, eventID, sessionID uuid.UUID) string {
	return sessionPath(orgID, eventID, sessionID) + "/tiers"
}

// PatchTier sends the given keys to PATCH .../tiers/{id}: a missing key keeps
// the stored value. The answer is the category as stored afterwards.
func (c *ArenaClient) PatchTier(ctx context.Context, jwt string, orgID, eventID, sessionID, tierID uuid.UUID, body map[string]any) (openapi.TicketTierItem, error) {
	var out openapi.TicketTierEnvelope
	err := c.do(ctx, http.MethodPatch, tiersPath(orgID, eventID, sessionID)+"/"+tierID.String(), jwt, body, &out)
	return out.Tier, err
}

// TierScheduleOf returns a category's price schedule: its base price, the price
// buyers pay now and the scheduled windows.
func (c *ArenaClient) TierScheduleOf(ctx context.Context, jwt string, orgID, eventID, sessionID, tierID uuid.UUID) (openapi.TierPriceSchedule, error) {
	var out openapi.TierPriceScheduleEnvelope
	err := c.do(ctx, http.MethodGet, tiersPath(orgID, eventID, sessionID)+"/"+tierID.String()+"/price-schedule", jwt, nil, &out)
	return out.PriceSchedule, err
}

// PutTierSchedule replaces a category's whole schedule with the given windows.
func (c *ArenaClient) PutTierSchedule(ctx context.Context, jwt string, orgID, eventID, sessionID, tierID uuid.UUID, windows []openapi.TierPriceWindow) error {
	in := make([]map[string]any, 0, len(windows))
	for _, w := range windows {
		item := map[string]any{"valid_from": w.ValidFrom.UTC().Format(time.RFC3339), "price_amount": w.PriceAmount}
		if w.ValidTo != nil {
			item["valid_to"] = w.ValidTo.UTC().Format(time.RFC3339)
		}
		in = append(in, item)
	}
	return c.do(ctx, http.MethodPut, tiersPath(orgID, eventID, sessionID)+"/"+tierID.String()+"/price-schedule", jwt, map[string]any{"windows": in}, nil)
}
