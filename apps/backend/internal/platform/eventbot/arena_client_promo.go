package eventbot

// arena_client_promo.go — the calls behind the bot's promo-code screens (EC-11,
// spec 35 §6.2). The rules live in arena-api (hcheckout/promo_codes.go): the
// discount arithmetic, the scope check (a session of ANOTHER organization is a
// 422 promo.invalid_session), the usage limits and the soft delete. The bot
// only shows the answers. The usage report carries buyers' names and e-mails:
// nothing here logs a body.

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The discount types as the API spells them.
const (
	promoTypePercent = "percent"
	promoTypeFixed   = "fixed_amount"
)

// The code statuses as the API spells them (migration 0110 added paused).
const (
	promoStatusActive = "active"
	promoStatusPaused = "paused"
)

// PromoCreate is the body of a new code. A nil slice and nil limits mean "no
// restriction"; the server normalises them.
type PromoCreate struct {
	Code               string
	DiscountType       string
	DiscountValue      int64 // percent 1-100, or minor units of Currency
	Currency           string
	SessionIDs         []uuid.UUID // empty = every session, including future ones
	MaxUses            *int32
	MaxUsesPerCustomer *int32
	ValidUntil         *time.Time
	Status             string
}

func promoPath(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/promo-codes"
}

// ListPromoCodes returns the organization's codes, newest first, each with its
// usage so far (uses, discount_total, last_used_at).
func (c *ArenaClient) ListPromoCodes(ctx context.Context, jwt string, orgID uuid.UUID) ([]openapi.PromoCodeItem, error) {
	var out openapi.PromoCodeListResponse
	if err := c.do(ctx, http.MethodGet, promoPath(orgID), jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.PromoCodes, nil
}

// GetPromoCode returns one code with its usage; another organization's code is
// the route's own 404 promo.not_found.
func (c *ArenaClient) GetPromoCode(ctx context.Context, jwt string, orgID, codeID uuid.UUID) (openapi.PromoCodeItem, error) {
	var out openapi.PromoCodeEnvelope
	err := c.do(ctx, http.MethodGet, promoPath(orgID)+"/"+codeID.String(), jwt, nil, &out)
	return out.PromoCode, err
}

// CreatePromoCode creates a code. A duplicate name is 409 promo.duplicate, a
// bad value one of the 400 promo.invalid_* codes, a foreign session 422
// promo.invalid_session.
func (c *ArenaClient) CreatePromoCode(ctx context.Context, jwt string, orgID uuid.UUID, in PromoCreate) (openapi.PromoCodeItem, error) {
	sessions := make([]string, 0, len(in.SessionIDs))
	for _, id := range in.SessionIDs {
		sessions = append(sessions, id.String())
	}
	body := map[string]any{
		"code":                   in.Code,
		"discount_type":          in.DiscountType,
		"discount_value":         in.DiscountValue,
		"applies_to_session_ids": sessions,
		"max_uses":               in.MaxUses,
		"max_uses_per_customer":  in.MaxUsesPerCustomer,
		"status":                 in.Status,
	}
	if in.Currency != "" {
		body["currency"] = in.Currency
	}
	if in.ValidUntil != nil {
		body["valid_until"] = in.ValidUntil.UTC().Format(time.RFC3339)
	}
	var out openapi.PromoCodeEnvelope
	err := c.do(ctx, http.MethodPost, promoPath(orgID), jwt, body, &out)
	return out.PromoCode, err
}

// SetPromoStatus pauses or activates a code (PATCH with only the status).
func (c *ArenaClient) SetPromoStatus(ctx context.Context, jwt string, orgID, codeID uuid.UUID, status string) (openapi.PromoCodeItem, error) {
	var out openapi.PromoCodeEnvelope
	err := c.do(ctx, http.MethodPatch, promoPath(orgID)+"/"+codeID.String(), jwt, map[string]any{"status": status}, &out)
	return out.PromoCode, err
}

// SetPromoSessions replaces the sessions a code is for. An EMPTY list is "every
// session of the organization, including future ones" (the club code): the
// body always names the array, because an absent or null one leaves the stored
// value alone.
func (c *ArenaClient) SetPromoSessions(ctx context.Context, jwt string, orgID, codeID uuid.UUID, sessionIDs []uuid.UUID) (openapi.PromoCodeItem, error) {
	ids := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		ids = append(ids, id.String())
	}
	var out openapi.PromoCodeEnvelope
	err := c.do(ctx, http.MethodPatch, promoPath(orgID)+"/"+codeID.String(), jwt, map[string]any{"applies_to_session_ids": ids}, &out)
	return out.PromoCode, err
}

// DeletePromoCode soft-deletes a code. The name stays reserved in the
// organization (UNIQUE (org_id, code) counts deleted rows), and the code
// leaves the list and its usage export.
func (c *ArenaClient) DeletePromoCode(ctx context.Context, jwt string, orgID, codeID uuid.UUID) error {
	return c.do(ctx, http.MethodDelete, promoPath(orgID)+"/"+codeID.String(), jwt, nil, nil)
}

// PromoRedemptions returns the uses of one code, newest first, each joined to
// the order it paid for. The report is not paged by the API; the screen pages
// it five rows at a time.
func (c *ArenaClient) PromoRedemptions(ctx context.Context, jwt string, orgID, codeID uuid.UUID) ([]openapi.PromoRedemptionItem, error) {
	var out openapi.PromoRedemptionListResponse
	path := "/v1/organizations/" + orgID.String() + "/promo-code-redemptions?promo_code_id=" + url.QueryEscape(codeID.String())
	if err := c.do(ctx, http.MethodGet, path, jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Redemptions, nil
}

// PromoRedemptionsCSV downloads the usage file of one code (EC-07).
func (c *ArenaClient) PromoRedemptionsCSV(ctx context.Context, jwt string, orgID, codeID uuid.UUID, locale string) (CSVFile, error) {
	return c.DownloadCSV(ctx, jwt, promoPath(orgID)+"/"+codeID.String()+"/redemptions.csv", locale)
}
