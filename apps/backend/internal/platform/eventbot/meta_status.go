package eventbot

// meta_status.go - the Meta connection line of the Settings section: one line
// with the state and a link to the web event center screen where the owner
// connects Meta. The bot has no Meta login. Nothing is shown when the feature
// is off, the person may not read it (403) or the API fails.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// MetaIntegration is the part of GET /v1/organizations/{org_id}/integrations/meta
// the bot reads.
type MetaIntegration struct {
	Enabled    bool `json:"enabled"`
	Connection *struct {
		Status string `json:"status"`
	} `json:"connection"`
}

// MetaIntegration reads the organization's Meta connection with the person's token.
func (c *ArenaClient) MetaIntegration(ctx context.Context, jwt string, orgID uuid.UUID) (MetaIntegration, error) {
	var out MetaIntegration
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/integrations/meta", jwt, nil, &out)
	return out, err
}

// metaSettingsBlock answers the status line and the link row, or "" and nil
// when nothing is to be shown.
func (b *Bot) metaSettingsBlock(ctx context.Context, loc string, id *Identity, jwt string) (string, []button) {
	base := strings.TrimRight(b.eventCenterURL, "/")
	if b.arena == nil || id == nil || id.Current == nil || id.Superadmin || !isOwner(id) || jwt == "" || base == "" {
		return "", nil
	}
	m, err := b.arena.MetaIntegration(ctx, jwt, id.Current.OrgID)
	if err != nil {
		if !IsAPIError(err, http.StatusForbidden) && b.logger != nil {
			b.logger.Warn("eventbot: meta integration read failed", slog.String("error", err.Error()))
		}
		return "", nil
	}
	if !m.Enabled {
		return "", nil
	}
	key := "bot.mn.meta_off"
	if m.Connection != nil {
		switch m.Connection.Status {
		case "active":
			key = "bot.mn.meta_ok"
		case "error":
			key = "bot.mn.meta_error"
		}
	}
	row := []models.InlineKeyboardButton{{
		Text: b.texts.T(loc, "bot.mn.meta_btn", nil),
		URL:  base + "/o/" + id.Current.OrgID.String() + "/ads/meta",
	}}
	return b.texts.T(loc, key, nil), row
}
