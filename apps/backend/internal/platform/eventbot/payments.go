package eventbot

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// PaymentsConnected reports whether the organization has at least one active
// payment-provider config whose credential the provider has accepted. known is
// false when the lookup is not allowed or fails (a manager without the
// payments permission, an API hiccup): the menu then says nothing rather than
// a wrong thing.
func (c *ArenaClient) PaymentsConnected(ctx context.Context, jwt string, orgID uuid.UUID) (connected, known bool) {
	var out struct {
		Configs []struct {
			IsActive           bool   `json:"is_active"`
			VerificationStatus string `json:"verification_status"`
		} `json:"payment_configs"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/payment-configs", jwt, nil, &out); err != nil {
		return false, false
	}
	for _, cfg := range out.Configs {
		if cfg.IsActive && cfg.VerificationStatus == "ok" {
			return true, true
		}
	}
	return false, true
}

// paymentsNote is the line an owner sees under the menu title while the
// organization cannot take money yet. A new organization starts that way: the
// operator connects its payment provider after the application is approved.
// Empty for everyone else, and whenever the state is not known.
func (b *Bot) paymentsNote(ctx context.Context, id *Identity, jwt string) string {
	if !isOwner(id) || jwt == "" {
		return ""
	}
	connected, known := b.arena.PaymentsConnected(ctx, jwt, id.Current.OrgID)
	if !known || connected {
		return ""
	}
	return "\n\n" + b.texts.T(id.Locale(), "bot.pay.not_connected", nil)
}
